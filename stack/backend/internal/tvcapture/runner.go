package tvcapture

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// processGrace is how long Stop waits after signaling before killing the
// process tree.
const processGrace = 5 * time.Second

// captureSpec is everything the runner needs to start one channel's capture.
type captureSpec struct {
	Channel        Channel
	WorkDir        string
	Segment        time.Duration
	Archive        bool
	StreamlinkPath string
	FFmpegPath     string
}

// captureProcess is a running capture: PCM exposes the audio stream, Wait blocks
// until the process exits, Stop asks it to terminate.
type captureProcess interface {
	PCM() io.Reader
	Wait() error
	Stop()
}

// processRunner starts a capture process from a spec.
type processRunner interface {
	Start(ctx context.Context, spec captureSpec) (captureProcess, error)
}

// execRunner starts the real streamlink|ffmpeg (or ffmpeg-only) process tree.
type execRunner struct {
	logger *slog.Logger
}

func newExecRunner(logger *slog.Logger) *execRunner { return &execRunner{logger: logger} }

// execProcess owns the child processes for one capture. ffmpeg's stdout is the
// PCM stream; when the source is YouTube a streamlink child feeds ffmpeg's stdin.
type execProcess struct {
	logger     *slog.Logger
	slug       string
	streamlink *exec.Cmd
	ffmpeg     *exec.Cmd
	pcm        io.Reader

	// exited is closed when ffmpeg (the primary output) has been reaped by Wait,
	// letting Stop escalate to SIGKILL only when a graceful stop overruns.
	exited   chan struct{}
	waitOnce sync.Once
	stopOnce sync.Once
}

func (r *execRunner) Start(ctx context.Context, spec captureSpec) (captureProcess, error) {
	kind := sourceKind(spec.Channel.SourceKind)
	ffArgs := ffmpegArgs(spec.Channel.Slug, spec.Channel.SourceRef, kind, spec.Archive, spec.Segment, spec.WorkDir)

	ffmpeg := exec.CommandContext(ctx, spec.FFmpegPath, ffArgs...)
	ffmpeg.Env = utcEnv()

	pcm, err := ffmpeg.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("tvcapture: ffmpeg stdout pipe: %w", err)
	}
	ffStderr, err := ffmpeg.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("tvcapture: ffmpeg stderr pipe: %w", err)
	}

	p := &execProcess{
		logger: r.logger,
		slug:   spec.Channel.Slug,
		ffmpeg: ffmpeg,
		pcm:    pcm,
		exited: make(chan struct{}),
	}

	if kind == sourceYouTube {
		streamlink := exec.CommandContext(ctx, spec.StreamlinkPath, streamlinkArgs(spec.Channel.SourceRef)...)
		streamlink.Env = utcEnv()
		slStdout, slErr := streamlink.StdoutPipe()
		if slErr != nil {
			return nil, fmt.Errorf("tvcapture: streamlink stdout pipe: %w", slErr)
		}
		slStderr, slErr := streamlink.StderrPipe()
		if slErr != nil {
			return nil, fmt.Errorf("tvcapture: streamlink stderr pipe: %w", slErr)
		}
		ffmpeg.Stdin = slStdout
		p.streamlink = streamlink

		if slErr := streamlink.Start(); slErr != nil {
			return nil, fmt.Errorf("tvcapture: start streamlink: %w", slErr)
		}
		go p.drainStderr("streamlink", slStderr)
	}

	if err := ffmpeg.Start(); err != nil {
		if p.streamlink != nil {
			_ = p.streamlink.Process.Kill()
		}
		return nil, fmt.Errorf("tvcapture: start ffmpeg: %w", err)
	}
	go p.drainStderr("ffmpeg", ffStderr)

	return p, nil
}

func (p *execProcess) PCM() io.Reader { return p.pcm }

// Wait blocks until ffmpeg exits and returns its exit error, then reaps the
// streamlink child (if any). Each child's Wait runs exactly once, here. Closing
// exited unblocks a concurrent Stop that is waiting out the grace period.
func (p *execProcess) Wait() error {
	err := p.ffmpeg.Wait()
	p.waitOnce.Do(func() { close(p.exited) })
	if p.streamlink != nil {
		if slErr := p.streamlink.Wait(); slErr != nil {
			p.logger.Debug("tvcapture: streamlink exited", slog.String("slug", p.slug), slog.Any("err", slErr))
		}
	}
	return err
}

// Stop signals ffmpeg (and streamlink) to terminate, then force-kills the tree
// if ffmpeg has not exited within processGrace. Reaping stays with Wait. It is
// idempotent.
func (p *execProcess) Stop() {
	p.stopOnce.Do(func() {
		signalTerm(p.ffmpeg)
		signalTerm(p.streamlink)
		select {
		case <-p.exited:
		case <-time.After(processGrace):
			killProcess(p.ffmpeg)
			killProcess(p.streamlink)
		}
	})
}

func (p *execProcess) drainStderr(name string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		p.logger.Debug("tvcapture: "+name+" stderr",
			slog.String("slug", p.slug),
			slog.String("line", scanner.Text()))
	}
}
