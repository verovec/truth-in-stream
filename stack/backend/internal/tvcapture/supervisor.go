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

	SegmentPoll           time.Duration
	WatchdogTick          time.Duration
	BackoffBase           time.Duration
	BackoffMax            time.Duration
	HealthyReset          time.Duration
	MaxAttempts           int
	SegmentArchiveTimeout time.Duration
	FinalArchiveTimeout   time.Duration
	FeedReconnectBackoff  time.Duration
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
	if c.SegmentArchiveTimeout <= 0 {
		// Normal per-segment archiving: generous, because a full stream-copied hour
		// can be multiple GB and legitimately takes minutes to upload on a modest
		// uplink. This bounds the upload (the HTTP upload client itself is
		// unbounded); a segment that overruns it stays on disk for a later retry.
		c.SegmentArchiveTimeout = 30 * time.Minute
	}
	if c.FinalArchiveTimeout <= 0 {
		// Shutdown salvage only: bounded below the worker container's
		// stop_grace_period (120s) so a final archive on shutdown finishes (or is
		// abandoned, leaving the .ts for the next startup salvage) before the
		// runtime SIGKILLs the process, rather than being cut off mid-upload.
		c.FinalArchiveTimeout = 90 * time.Second
	}
	if c.FeedReconnectBackoff <= 0 {
		c.FeedReconnectBackoff = 5 * time.Second
	}
	return c
}

// feedFrameBuffer is how many ~100ms PCM frames the reader buffers ahead of the
// feed sender. It absorbs a brief feed hiccup or reconnect without blocking the
// reader; when it fills (feed down or slow) the reader drops frames rather than
// stalling ffmpeg, so archiving is never held up by the analysis feed.
const feedFrameBuffer = 64

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
// then read PCM to the feed, archive segments, and watch for a stall until the
// process exits or the context is canceled, then archive the final segment. The
// feed is decoupled from the reader: a feed drop or reconnect never stops the
// PCM drain, so archiving continues even while live analysis is briefly down.
func (s *supervisor) runOnce(ctx context.Context) error {
	ch := s.channel
	dir := filepath.Join(s.cfg.WorkDir, ch.Slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tvcapture: create workdir: %w", err)
	}
	if ch.ArchiveEnabled {
		// Bound the startup salvage so a single leftover segment whose upload hangs
		// cannot wedge capture startup forever (matching the archiveClosed and final
		// salvage bounds); a segment it does not reach is picked up by the watcher
		// once capture produces a newer one, or by the next startup salvage.
		salvageCtx, cancel := context.WithTimeout(ctx, s.cfg.SegmentArchiveTimeout)
		_ = s.arch.salvage(salvageCtx, ch, dir)
		cancel()
	}

	// The process runs on its own context so shutdown is a graceful Stop (SIGTERM)
	// rather than the SIGKILL an exec.CommandContext cancel would deliver.
	procCtx, procCancel := context.WithCancel(context.Background())
	defer procCancel()

	proc, err := s.runner.Start(procCtx, s.spec())
	if err != nil {
		return fmt.Errorf("tvcapture: start capture: %w", err)
	}

	activity := &activityTracker{}
	activity.mark(s.clock.Now())

	var wg sync.WaitGroup
	stopWorkers := make(chan struct{})
	procDone := make(chan struct{})
	// The reader buffers frames ahead of the sender and drops when full, so a feed
	// outage never blocks ffmpeg's stdout (and thus never stalls archiving). The
	// sender owns the feed connection and reconnects with backoff.
	frames := make(chan []byte, feedFrameBuffer)

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.readPCM(proc.PCM(), frames, activity)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.sendFeed(ctx, ch.ID, frames)
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
	wg.Wait() // readPCM closes frames on EOF; sendFeed drains it and closes the sink

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

// readPCM reads the process's PCM stream in ~100ms frames and hands each to the
// feed sender over frames, recording the time of the last bytes read for the
// watchdog. It always drains the reader (so ffmpeg never blocks on a full stdout
// pipe and archiving is unaffected by the feed): when the buffered channel is
// full - the sender is behind because the feed is down or reconnecting - it drops
// the frame rather than blocking. It closes frames when the stream ends (process
// exit), which stops the sender. Activity is marked on every read, so the
// watchdog trips only on a genuine upstream stall, not on a feed outage.
func (s *supervisor) readPCM(r io.Reader, frames chan<- []byte, activity *activityTracker) {
	defer close(frames)
	buf := make([]byte, pcmFrameBytes)
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			activity.mark(s.clock.Now())
			frame := make([]byte, n)
			copy(frame, buf[:n])
			select {
			case frames <- frame:
			default:
				// Sender is behind (feed down/reconnecting); drop this frame so the
				// read never blocks. Analysis has a gap; the archive does not.
			}
		}
		if err != nil {
			return
		}
	}
}

// sendFeed consumes PCM frames and writes them to the channel's live publisher
// socket, owning the feed connection for the capture's lifetime. It connects
// lazily and, on a send or connect failure, closes the socket and retries with a
// fixed backoff, dropping frames while the feed is down. Decoupling the feed from
// the reader means a hub outage costs only an analysis gap, never a capture or
// archive stall. It returns when frames is closed (process exit), closing the
// socket on the way out.
func (s *supervisor) sendFeed(ctx context.Context, channelID string, frames <-chan []byte) {
	var sink frameSink
	var nextRetry time.Time
	defer func() {
		if sink != nil {
			_ = sink.Close()
		}
	}()
	for frame := range frames {
		if sink == nil {
			if s.clock.Now().Before(nextRetry) {
				continue
			}
			conn, err := s.feed.Connect(ctx, channelID)
			if err != nil {
				s.logger.Warn("tvcapture: feed connect failed, will retry", slog.Any("err", err))
				nextRetry = s.clock.Now().Add(s.cfg.FeedReconnectBackoff)
				continue
			}
			sink = conn
		}
		if err := sink.Send(ctx, frame); err != nil {
			s.logger.Warn("tvcapture: feed send failed, reconnecting", slog.Any("err", err))
			_ = sink.Close()
			sink = nil
			nextRetry = s.clock.Now().Add(s.cfg.FeedReconnectBackoff)
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
	// Each closed segment gets its OWN generously-bounded context, so when several
	// segments have accumulated (worker was behind) a slow early upload does not
	// eat the whole batch's budget and starve the tail; a stuck upload still cannot
	// wedge the loop forever.
	for _, ts := range files[:len(files)-1] {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.SegmentArchiveTimeout)
		if err := s.arch.archive(ctx, ch, ts); err != nil {
			s.logger.Warn("tvcapture: archive segment failed",
				slog.String("segment", filepath.Base(ts)), slog.Any("err", err))
		}
		cancel()
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
