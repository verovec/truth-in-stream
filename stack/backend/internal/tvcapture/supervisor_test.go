package tvcapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// fakeProcess is a scripted capture process. PCM is fed through a pipe the
// behavior writes to; the process stays alive until end (Stop, watchdog, or a
// scripted unexpected exit) closes it.
type fakeProcess struct {
	pr   *io.PipeReader
	pw   *io.PipeWriter
	done chan struct{}
	err  error
	once sync.Once

	mu      sync.Mutex
	stopped bool
}

func (p *fakeProcess) PCM() io.Reader { return p.pr }
func (p *fakeProcess) Wait() error    { <-p.done; return p.err }

func (p *fakeProcess) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.end(nil)
}

func (p *fakeProcess) wasStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// end terminates the process with err, closing the PCM writer so the pump sees
// EOF and unblocking Wait.
func (p *fakeProcess) end(err error) {
	p.once.Do(func() {
		p.err = err
		_ = p.pw.Close()
		close(p.done)
	})
}

type fakeRunner struct {
	behavior func(attempt int, p *fakeProcess, spec captureSpec)

	mu     sync.Mutex
	starts int
	procs  []*fakeProcess
}

func (r *fakeRunner) Start(_ context.Context, spec captureSpec) (captureProcess, error) {
	r.mu.Lock()
	r.starts++
	attempt := r.starts
	pr, pw := io.Pipe()
	p := &fakeProcess{pr: pr, pw: pw, done: make(chan struct{})}
	r.procs = append(r.procs, p)
	r.mu.Unlock()

	if r.behavior != nil {
		go r.behavior(attempt, p, spec)
	}
	return p, nil
}

func (r *fakeRunner) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func (r *fakeRunner) proc(i int) *fakeProcess {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.procs) {
		return nil
	}
	return r.procs[i]
}

type fakeSink struct {
	mu     sync.Mutex
	frames [][]byte
	closed bool
}

func (s *fakeSink) Send(_ context.Context, frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, append([]byte(nil), frame...))
	return nil
}

func (s *fakeSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeSink) frameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

type fakeFeed struct{ sink *fakeSink }

func (f *fakeFeed) Connect(context.Context, string) (frameSink, error) { return f.sink, nil }

type fakeArchiver struct {
	mu       sync.Mutex
	archived []string
}

func (a *fakeArchiver) archive(_ context.Context, _ Channel, tsPath string) error {
	a.mu.Lock()
	a.archived = append(a.archived, filepath.Base(tsPath))
	a.mu.Unlock()
	_ = os.Remove(tsPath)
	return nil
}

func (a *fakeArchiver) salvage(ctx context.Context, ch Channel, dir string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "*"+segmentExt))
	for _, f := range files {
		_ = a.archive(ctx, ch, f)
	}
	return nil
}

func (a *fakeArchiver) archivedList() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.archived...)
}

type fakeNotifier struct {
	mu     sync.Mutex
	events []crawlnotify.CrawlEvent
}

func (n *fakeNotifier) Notify(_ context.Context, e crawlnotify.CrawlEvent) error {
	n.mu.Lock()
	n.events = append(n.events, e)
	n.mu.Unlock()
	return nil
}

func (n *fakeNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

func (n *fakeNotifier) first() crawlnotify.CrawlEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.events) == 0 {
		return nil
	}
	return n.events[0]
}

func testSupCfg(workDir string) supervisorConfig {
	return supervisorConfig{
		WorkDir:              workDir,
		Segment:              time.Hour,
		FeedStall:            40 * time.Millisecond,
		SegmentPoll:          5 * time.Millisecond,
		WatchdogTick:         5 * time.Millisecond,
		BackoffBase:          time.Millisecond,
		BackoffMax:           5 * time.Millisecond,
		HealthyReset:         time.Hour,
		MaxAttempts:          3,
		FinalArchiveTimeout:  time.Second,
		FeedReconnectBackoff: time.Millisecond,
	}
}

func newTestSupervisor(t *testing.T, ch Channel, runner processRunner, sink *fakeSink, arch segmentArchiver, notifier crawlnotify.Notifier) *supervisor {
	t.Helper()
	return newSupervisor(ch, runner, &fakeFeed{sink: sink}, arch, realClock{},
		testSupCfg(t.TempDir()), discardLogger(), notifier)
}

func TestSupervisorPumpsPCMAndArchivesFinalSegmentOnCleanStop(t *testing.T) {
	t.Parallel()
	ch := Channel{ID: "c1", Slug: "tf1", Enabled: true, ArchiveEnabled: true}
	sink := &fakeSink{}
	arch := &fakeArchiver{}
	notifier := &fakeNotifier{}

	runner := &fakeRunner{
		behavior: func(_ int, p *fakeProcess, spec captureSpec) {
			// Three full PCM frames, then drop the (still-open) segment file and
			// keep the process alive until Stop.
			_, _ = p.pw.Write(make([]byte, pcmFrameBytes*3))
			dir := filepath.Join(spec.WorkDir, spec.Channel.Slug)
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "20260101_000000.ts"), []byte("ts"), 0o600)
		},
	}

	sup := newTestSupervisor(t, ch, runner, sink, arch, notifier)
	sup.start(context.Background())

	waitFor(t, func() bool { return sink.frameCount() == 3 }, "3 PCM frames delivered")

	sup.stop()

	if got := sink.frameCount(); got != 3 {
		t.Fatalf("frames = %d, want 3", got)
	}
	if !sink.closed {
		t.Fatal("sink not closed on stop")
	}
	if !contains(arch.archivedList(), "20260101_000000.ts") {
		t.Fatalf("final segment not archived: %v", arch.archivedList())
	}
	if notifier.count() != 0 {
		t.Fatalf("clean stop must not alert, got %d events", notifier.count())
	}
}

func TestSupervisorArchivesClosedSegmentWhileRunning(t *testing.T) {
	t.Parallel()
	ch := Channel{ID: "c1", Slug: "tf1", Enabled: true, ArchiveEnabled: true}
	sink := &fakeSink{}
	arch := &fakeArchiver{}

	runner := &fakeRunner{
		behavior: func(_ int, _ *fakeProcess, spec captureSpec) {
			dir := filepath.Join(spec.WorkDir, spec.Channel.Slug)
			_ = os.MkdirAll(dir, 0o755)
			// Two segments: the older is closed and must be archived by the watcher.
			_ = os.WriteFile(filepath.Join(dir, "20260101_000000.ts"), []byte("ts"), 0o600)
			_ = os.WriteFile(filepath.Join(dir, "20260101_010000.ts"), []byte("ts"), 0o600)
		},
	}

	sup := newTestSupervisor(t, ch, runner, sink, arch, &fakeNotifier{})
	sup.start(context.Background())

	waitFor(t, func() bool { return contains(arch.archivedList(), "20260101_000000.ts") },
		"closed segment archived by watcher")

	sup.stop()
	if !contains(arch.archivedList(), "20260101_010000.ts") {
		t.Fatalf("final segment not archived on stop: %v", arch.archivedList())
	}
}

func TestSupervisorWatchdogStopsStalledProcess(t *testing.T) {
	t.Parallel()
	ch := Channel{ID: "c1", Slug: "tf1", Enabled: true}
	runner := &fakeRunner{
		behavior: func(_ int, _ *fakeProcess, _ captureSpec) {
			// Emit no PCM; the feed is stalled and the watchdog must stop it.
		},
	}
	sup := newTestSupervisor(t, ch, runner, &fakeSink{}, &fakeArchiver{}, &fakeNotifier{})
	sup.start(context.Background())

	waitFor(t, func() bool {
		p := runner.proc(0)
		return p != nil && p.wasStopped()
	}, "watchdog stopped stalled process")

	sup.stop()
}

func TestSupervisorRestartsOnUnexpectedExit(t *testing.T) {
	t.Parallel()
	ch := Channel{ID: "c1", Slug: "tf1", Enabled: true}
	runner := &fakeRunner{
		behavior: func(_ int, p *fakeProcess, _ captureSpec) {
			p.end(errors.New("boom"))
		},
	}
	sup := newTestSupervisor(t, ch, runner, &fakeSink{}, &fakeArchiver{}, &fakeNotifier{})
	sup.start(context.Background())

	waitFor(t, func() bool { return runner.startCount() >= 2 }, "process restarted after unexpected exit")
	sup.stop()
}

func TestSupervisorAlertsOnceAfterExhaustion(t *testing.T) {
	t.Parallel()
	ch := Channel{ID: "c1", Slug: "tf1", Enabled: true}
	notifier := &fakeNotifier{}
	runner := &fakeRunner{
		behavior: func(_ int, p *fakeProcess, _ captureSpec) {
			p.end(errors.New("boom"))
		},
	}
	sup := newTestSupervisor(t, ch, runner, &fakeSink{}, &fakeArchiver{}, notifier)
	sup.start(context.Background())

	// MaxAttempts is 3; the alert fires once when the third consecutive failure
	// lands, and never again without a healthy run in between.
	waitFor(t, func() bool { return notifier.count() >= 1 }, "capture-death alert fired")
	waitFor(t, func() bool { return runner.startCount() >= 6 }, "kept restarting past exhaustion")

	sup.stop()

	if notifier.count() != 1 {
		t.Fatalf("alert must fire once per exhaustion, got %d", notifier.count())
	}
	rf, ok := notifier.first().(crawlnotify.RunFailed)
	if !ok {
		t.Fatalf("event is %T, want crawlnotify.RunFailed", notifier.first())
	}
	if rf.Scope != "tf1" || rf.Source != "tvcapture" {
		t.Fatalf("alert = %+v, want source tvcapture scope tf1", rf)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// failSink is a frameSink whose every Send fails, standing in for a dropped feed.
type failSink struct{}

func (failSink) Send(context.Context, []byte) error { return errors.New("feed down") }
func (failSink) Close() error                       { return nil }

// flakyFeed fails the first connection's sends, then hands out a healthy sink on
// reconnect, so a test can prove the sender reconnects and the reader keeps
// draining meanwhile.
type flakyFeed struct {
	mu       sync.Mutex
	connects int
	healthy  *fakeSink
}

func (f *flakyFeed) Connect(context.Context, string) (frameSink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connects++
	if f.connects == 1 {
		return failSink{}, nil
	}
	return f.healthy, nil
}

func (f *flakyFeed) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

func TestSupervisorReconnectsFeedWithoutStoppingCapture(t *testing.T) {
	t.Parallel()
	ch := Channel{ID: "c1", Slug: "tf1", Enabled: true, ArchiveEnabled: true}
	healthy := &fakeSink{}
	feed := &flakyFeed{healthy: healthy}
	arch := &fakeArchiver{}
	notifier := &fakeNotifier{}

	runner := &fakeRunner{
		behavior: func(_ int, p *fakeProcess, spec captureSpec) {
			dir := filepath.Join(spec.WorkDir, spec.Channel.Slug)
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "20260101_000000.ts"), []byte("ts"), 0o600)
			// Produce PCM continuously until the process is stopped: the point is
			// that a failing feed must never stall this drain.
			for {
				if _, err := p.pw.Write(make([]byte, pcmFrameBytes)); err != nil {
					return
				}
			}
		},
	}

	sup := newSupervisor(ch, runner, feed, arch, realClock{}, testSupCfg(t.TempDir()), discardLogger(), notifier)
	sup.start(context.Background())

	// The first feed connection fails every Send; the sender must reconnect and
	// deliver to the healthy sink, which proves the capture kept draining and the
	// feed recovered on its own.
	waitFor(t, func() bool { return healthy.frameCount() > 0 }, "frames delivered after feed reconnect")
	if feed.connectCount() < 2 {
		t.Fatalf("feed did not reconnect: %d connects", feed.connectCount())
	}

	sup.stop()

	// A feed outage is not a capture death: no alert fires, and the final segment
	// is still archived because the reader never stalled.
	if notifier.count() != 0 {
		t.Fatalf("feed outage must not alert, got %d events", notifier.count())
	}
	if !contains(arch.archivedList(), "20260101_000000.ts") {
		t.Fatalf("final segment not archived after feed outage: %v", arch.archivedList())
	}
}
