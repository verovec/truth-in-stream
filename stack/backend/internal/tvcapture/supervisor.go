package tvcapture

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// frameSink is one channel's live publisher connection: PCM frames go out via
// Send, and Close ends the analysis session.
type frameSink interface {
	Send(ctx context.Context, frame []byte) error
	Close() error
}

// feedConnector opens a publisher frameSink for a channel.
type feedConnector interface {
	Connect(ctx context.Context, channelID string) (frameSink, error)
}

// segmentArchiver archives completed TS segments; *archiver implements it.
type segmentArchiver interface {
	archive(ctx context.Context, ch Channel, tsPath string) error
	salvage(ctx context.Context, ch Channel, dir string) error
}

// alertTimeout bounds a capture-death Slack post so an unreachable webhook
// cannot pin the restart loop.
const alertTimeout = 10 * time.Second

// supervisorConfig holds one channel's capture tunables plus the restart/timing
// knobs. Zero-valued knobs are defaulted by newSupervisor; tests set tiny values
// to keep runs fast and deterministic.
type supervisorConfig struct {
	WorkDir        string
	Segment        time.Duration
	FeedStall      time.Duration
	StreamlinkPath string
	FFmpegPath     string

	SegmentPoll         time.Duration
	WatchdogTick        time.Duration
	BackoffBase         time.Duration
	BackoffMax          time.Duration
	HealthyReset        time.Duration
	MaxAttempts         int
	FinalArchiveTimeout time.Duration
}

func (c supervisorConfig) withDefaults() supervisorConfig {
	if c.SegmentPoll <= 0 {
		c.SegmentPoll = 3 * time.Second
	}
	if c.WatchdogTick <= 0 {
		c.WatchdogTick = c.FeedStall / 3
	}
	if c.WatchdogTick <= 0 {
		c.WatchdogTick = 5 * time.Second
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 2 * time.Minute
	}
	if c.HealthyReset <= 0 {
		c.HealthyReset = 5 * time.Minute
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.FinalArchiveTimeout <= 0 {
		c.FinalArchiveTimeout = 2 * time.Minute
	}
	return c
}

// channelSupervisor is the manager's handle on a running per-channel supervisor.
type channelSupervisor interface {
	start(parent context.Context)
	stop()
}

// supervisor runs and keeps alive the capture pipeline for one channel: it
// starts the process, pumps PCM to the live feed, archives segments, watches for
// a stalled feed, and restarts with backoff on an unexpected exit.
type supervisor struct {
	channel  Channel
	runner   processRunner
	feed     feedConnector
	arch     segmentArchiver
	clock    clock
	cfg      supervisorConfig
	logger   *slog.Logger
	notifier crawlnotify.Notifier

	cancel context.CancelFunc
	done   chan struct{}
}

func newSupervisor(
	ch Channel,
	runner processRunner,
	feed feedConnector,
	arch segmentArchiver,
	clk clock,
	cfg supervisorConfig,
	logger *slog.Logger,
	notifier crawlnotify.Notifier,
) *supervisor {
	return &supervisor{
		channel:  ch,
		runner:   runner,
		feed:     feed,
		arch:     arch,
		clock:    clk,
		cfg:      cfg.withDefaults(),
		logger:   logger.With(slog.String("channel_id", ch.ID), slog.String("slug", ch.Slug)),
		notifier: notifier,
	}
}

// start launches the run loop on a context derived from parent; stop cancels it.
func (s *supervisor) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		s.run(ctx)
	}()
}

// stop cancels the supervisor's context and waits for run to return.
func (s *supervisor) stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.done != nil {
		<-s.done
	}
}

func (s *supervisor) run(ctx context.Context) {
	attempt := 0
	alerted := false
	for {
		if ctx.Err() != nil {
			return
		}
		started := s.clock.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			// Graceful shutdown: runOnce already archived the final segment.
			return
		}

		if s.clock.Now().Sub(started) >= s.cfg.HealthyReset {
			attempt = 0
			alerted = false
		}
		attempt++
		s.logger.Warn("tvcapture: capture exited, restarting",
			slog.Int("attempt", attempt), slog.Any("err", err))

		if attempt >= s.cfg.MaxAttempts && !alerted {
			s.alertDeath(ctx, err)
			alerted = true
		}

		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(computeBackoff(attempt, s.cfg.BackoffBase, s.cfg.BackoffMax)):
		}
	}
}

// runOnce runs a single capture lifecycle: salvage leftovers, start the process,
// connect the feed, pump PCM + archive segments + watch the feed until the
// process exits or the context is canceled, then archive the final segment.
func (s *supervisor) runOnce(ctx context.Context) error {
	ch := s.channel
	dir := filepath.Join(s.cfg.WorkDir, ch.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tvcapture: create workdir: %w", err)
	}
	if ch.ArchiveEnabled {
		_ = s.arch.salvage(ctx, ch, dir)
	}

	// The process runs on its own context so shutdown is a graceful Stop (SIGTERM)
	// rather than the SIGKILL an exec.CommandContext cancel would deliver.
	procCtx, procCancel := context.WithCancel(context.Background())
	defer procCancel()

	proc, err := s.runner.Start(procCtx, s.spec())
	if err != nil {
		return fmt.Errorf("tvcapture: start capture: %w", err)
	}

	sink, err := s.feed.Connect(ctx, ch.ID)
	if err != nil {
		proc.Stop()
		_ = proc.Wait()
		return fmt.Errorf("tvcapture: connect feed: %w", err)
	}

	activity := &activityTracker{}
	activity.mark(s.clock.Now())

	var wg sync.WaitGroup
	stopWorkers := make(chan struct{})
	procDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.pumpPCM(procCtx, proc.PCM(), sink, activity)
	}()

	if ch.ArchiveEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.watchSegments(dir, ch, stopWorkers)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.watchdog(proc, activity, stopWorkers)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			proc.Stop()
		case <-procDone:
		}
	}()

	waitErr := proc.Wait()
	close(procDone)
	close(stopWorkers)
	procCancel()
	wg.Wait()
	_ = sink.Close()

	if ch.ArchiveEnabled {
		finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.FinalArchiveTimeout)
		_ = s.arch.salvage(finalCtx, ch, dir)
		cancel()
	}

	if ctx.Err() != nil {
		return nil
	}
	return waitErr
}

func (s *supervisor) spec() captureSpec {
	return captureSpec{
		Channel:        s.channel,
		WorkDir:        s.cfg.WorkDir,
		Segment:        s.cfg.Segment,
		Archive:        s.channel.ArchiveEnabled,
		StreamlinkPath: s.cfg.StreamlinkPath,
		FFmpegPath:     s.cfg.FFmpegPath,
	}
}

// pumpPCM reads the process's PCM stream in ~100ms frames and forwards each to
// the feed, recording the time of the last bytes read for the watchdog. It ends
// when the stream closes (process exit) or a Send fails.
func (s *supervisor) pumpPCM(ctx context.Context, r io.Reader, sink frameSink, activity *activityTracker) {
	buf := make([]byte, pcmFrameBytes)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			activity.mark(s.clock.Now())
			frame := make([]byte, n)
			copy(frame, buf[:n])
			if serr := sink.Send(ctx, frame); serr != nil {
				s.logger.Warn("tvcapture: feed send failed", slog.Any("err", serr))
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// watchSegments polls the channel's segment dir; when a newer segment appears
// every older one is complete and gets archived. The newest (still-open) segment
// is left for the final archive on stop.
func (s *supervisor) watchSegments(dir string, ch Channel, stop <-chan struct{}) {
	ticker := s.clock.NewTicker(s.cfg.SegmentPoll)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C():
			s.archiveClosed(dir, ch)
		}
	}
}

func (s *supervisor) archiveClosed(dir string, ch Channel) {
	files, err := filepath.Glob(filepath.Join(dir, "*"+segmentExt))
	if err != nil || len(files) <= 1 {
		return
	}
	sort.Strings(files)
	// Archive on a fresh short-lived context so a stuck upload cannot wedge the
	// poll loop; the watcher keeps polling regardless.
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.FinalArchiveTimeout)
	defer cancel()
	for _, ts := range files[:len(files)-1] {
		if err := s.arch.archive(ctx, ch, ts); err != nil {
			s.logger.Warn("tvcapture: archive segment failed",
				slog.String("segment", filepath.Base(ts)), slog.Any("err", err))
		}
	}
}

// watchdog stops the process if no PCM bytes have arrived within FeedStall, so a
// silently wedged upstream is torn down and restarted rather than hanging.
func (s *supervisor) watchdog(proc captureProcess, activity *activityTracker, stop <-chan struct{}) {
	ticker := s.clock.NewTicker(s.cfg.WatchdogTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C():
			if s.clock.Now().Sub(activity.last()) >= s.cfg.FeedStall {
				s.logger.Warn("tvcapture: feed stalled, stopping capture",
					slog.Duration("stall", s.cfg.FeedStall))
				proc.Stop()
				return
			}
		}
	}
}

func (s *supervisor) alertDeath(ctx context.Context, cause error) {
	alertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertTimeout)
	defer cancel()
	_ = s.notifier.Notify(alertCtx, crawlnotify.RunFailed{
		Source: "tvcapture",
		Scope:  s.channel.Slug,
		Err:    fmt.Errorf("capture died after %d attempts: %w", s.cfg.MaxAttempts, cause),
	})
}

// activityTracker records the wall time of the last PCM bytes seen, read by the
// watchdog. It is safe for concurrent use.
type activityTracker struct{ ns atomic.Int64 }

func (a *activityTracker) mark(t time.Time) { a.ns.Store(t.UnixNano()) }
func (a *activityTracker) last() time.Time  { return time.Unix(0, a.ns.Load()) }

// computeBackoff is base doubled per attempt, capped at capDur. attempt 1 ->
// base.
func computeBackoff(attempt int, base, capDur time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= capDur || d <= 0 {
			return capDur
		}
	}
	if d > capDur {
		return capDur
	}
	return d
}
