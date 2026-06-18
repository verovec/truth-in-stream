package schedule

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// slogDiscard returns a logger that drops every record, keeping test output clean.
func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeProducer is a controllable crawlnotify.Producer for tests: it counts runs,
// optionally blocks until released (to exercise overlap-skip), and can return a
// fixed error. No real network or queue work happens.
type fakeProducer struct {
	name  string
	scope string
	runs  atomic.Int64
	// release, when non-nil, makes Run block until it is closed. ignoreCancel
	// controls whether the block also honors ctx: false (default) lets a
	// canceled context unblock Run, true makes Run finish only on release - a
	// producer that must complete its publish even as the scheduler shuts down.
	release      chan struct{}
	ignoreCancel bool
	err          error
}

func (p *fakeProducer) Name() string  { return p.name }
func (p *fakeProducer) Scope() string { return p.scope }

func (p *fakeProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	p.runs.Add(1)
	if p.release != nil {
		if p.ignoreCancel {
			<-p.release
		} else {
			select {
			case <-p.release:
			case <-ctx.Done():
				return crawlnotify.Stats{}, ctx.Err()
			}
		}
	}
	return crawlnotify.Stats{New: 1}, p.err
}

// everyMinute fires at the top of each minute, the simplest schedule for the
// synthetic-time tests.
type everyMinute struct{}

func (everyMinute) Next(t time.Time) time.Time {
	return t.Truncate(time.Minute).Add(time.Minute)
}

// recordingNotifier captures every event message in order so tests can assert the
// start/finish/fail alert sequence without a live webhook.
type recordingNotifier struct {
	mu     sync.Mutex
	events []crawlnotify.CrawlEvent
}

func (n *recordingNotifier) Notify(_ context.Context, event crawlnotify.CrawlEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
	return nil
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

func (n *recordingNotifier) snapshot() []crawlnotify.CrawlEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]crawlnotify.CrawlEvent, len(n.events))
	copy(out, n.events)
	return out
}

func TestRegistryParseRejectsBadSpec(t *testing.T) {
	t.Parallel()
	var reg Registry
	if err := reg.Register("wikipedia", "not a cron spec", &fakeProducer{name: "wikipedia"}); err == nil {
		t.Fatal("expected an error for an invalid cron spec, got nil")
	}
}

func TestRegistryParseAcceptsStandardSpec(t *testing.T) {
	t.Parallel()
	var reg Registry
	if err := reg.Register("wikipedia", "*/5 * * * *", &fakeProducer{name: "wikipedia"}); err != nil {
		t.Fatalf("expected a valid standard spec to register, got %v", err)
	}
	if got := reg.Len(); got != 1 {
		t.Fatalf("registry length = %d, want 1", got)
	}
}

func TestRegistryRejectsNilProducer(t *testing.T) {
	t.Parallel()
	var reg Registry
	if err := reg.Register("wikipedia", "*/5 * * * *", nil); err == nil {
		t.Fatal("expected an error for a nil producer, got nil")
	}
}

func TestRegistryRejectsDuplicateSource(t *testing.T) {
	t.Parallel()
	var reg Registry
	if err := reg.Register("wikipedia", "*/5 * * * *", &fakeProducer{name: "wikipedia"}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := reg.Register("wikipedia", "*/5 * * * *", &fakeProducer{name: "wikipedia"}); err == nil {
		t.Fatal("expected an error registering a duplicate source, got nil")
	}
}

func TestSchedulerFiresOnSchedule(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		producer := &fakeProducer{name: "wikipedia", scope: "Category:Physics"}
		reg := newTestRegistry(t, producer)
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		// Advance three full minutes; everyMinute fires once per minute.
		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()

		if got := producer.runs.Load(); got != 3 {
			t.Fatalf("producer ran %d times, want 3", got)
		}
		cancel()
		<-done
	})
}

func TestSchedulerSkipsOverlappingRun(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// release is never closed, so the first run blocks indefinitely; the
		// next tick must be skipped, not stacked.
		producer := &fakeProducer{name: "wikipedia", release: make(chan struct{})}
		reg := newTestRegistry(t, producer)
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		// Two ticks fire while the first run is still blocked in Run.
		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()

		if got := producer.runs.Load(); got != 1 {
			t.Fatalf("producer ran %d times while a run was in flight, want 1 (overlap skipped)", got)
		}
		cancel()
		<-done
	})
}

func TestSchedulerAlertsEachRun(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		producer := &fakeProducer{name: "wikipedia", scope: "Category:Physics"}
		reg := newTestRegistry(t, producer)
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		time.Sleep(time.Minute + time.Second)
		synctest.Wait()

		// One run => exactly a RunStarted then a RunFinished.
		events := notifier.snapshot()
		if len(events) != 2 {
			t.Fatalf("got %d alert events, want 2 (start + finish)", len(events))
		}
		if _, ok := events[0].(crawlnotify.RunStarted); !ok {
			t.Fatalf("first event = %T, want crawlnotify.RunStarted", events[0])
		}
		if _, ok := events[1].(crawlnotify.RunFinished); !ok {
			t.Fatalf("second event = %T, want crawlnotify.RunFinished", events[1])
		}
		cancel()
		<-done
	})
}

func TestSchedulerAlertsFailure(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		producer := &fakeProducer{name: "wikipedia", err: errors.New("boom")}
		reg := newTestRegistry(t, producer)
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		time.Sleep(time.Minute + time.Second)
		synctest.Wait()

		events := notifier.snapshot()
		if len(events) != 2 {
			t.Fatalf("got %d alert events, want 2 (start + fail)", len(events))
		}
		if _, ok := events[1].(crawlnotify.RunFailed); !ok {
			t.Fatalf("second event = %T, want crawlnotify.RunFailed", events[1])
		}
		cancel()
		<-done
	})
}

func TestSchedulerShutsDownCleanly(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		producer := &fakeProducer{name: "wikipedia"}
		reg := newTestRegistry(t, producer)
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		// Cancel before any tick; Run must return promptly.
		cancel()
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Fatal("scheduler did not shut down within a minute of cancellation")
		}
		if notifier.count() != 0 {
			t.Fatalf("expected no alerts before any tick, got %d", notifier.count())
		}
	})
}

func TestSchedulerWaitsForInFlightRunOnShutdown(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		producer := &fakeProducer{name: "wikipedia", release: release, ignoreCancel: true}
		reg := newTestRegistry(t, producer)
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		// Let the first run start and block in Run.
		time.Sleep(time.Minute + time.Second)
		synctest.Wait()
		if got := producer.runs.Load(); got != 1 {
			t.Fatalf("producer ran %d times, want 1 before shutdown", got)
		}

		cancel()
		// Run is still blocked, so the scheduler must not have returned yet.
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("scheduler returned while a run was still in flight")
		default:
		}

		close(release) // let the in-flight run finish
		<-done
	})
}

func TestSchedulerWithNoSourcesIdlesUntilCanceled(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		s := New(Registry{}, &recordingNotifier{}, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		// An empty registry must not let Run return on its own.
		time.Sleep(time.Hour)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("scheduler returned before cancellation with no sources")
		default:
		}

		cancel()
		<-done
	})
}

func TestSchedulerRunsMultipleSourcesIndependently(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		wiki := &fakeProducer{name: "wikipedia"}
		scrutins := &fakeProducer{name: "scrutins"}
		var reg Registry
		if err := reg.RegisterSchedule("wikipedia", everyMinute{}, 0, wiki); err != nil {
			t.Fatalf("register wikipedia: %v", err)
		}
		if err := reg.RegisterSchedule("scrutins", everyMinute{}, 0, scrutins); err != nil {
			t.Fatalf("register scrutins: %v", err)
		}
		notifier := &recordingNotifier{}

		s := New(reg, notifier, slogDiscard())
		ctx, cancel := context.WithCancel(context.Background())
		done := runInBackground(ctx, s)

		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()

		if got := wiki.runs.Load(); got != 2 {
			t.Fatalf("wikipedia ran %d times, want 2", got)
		}
		if got := scrutins.runs.Load(); got != 2 {
			t.Fatalf("scrutins ran %d times, want 2", got)
		}
		cancel()
		<-done
	})
}

// newTestRegistry builds a single-source ("wikipedia") registry firing every
// minute with no jitter, the common setup for the scheduler tests.
func newTestRegistry(t *testing.T, p crawlnotify.Producer) Registry {
	t.Helper()
	var reg Registry
	if err := reg.RegisterSchedule("wikipedia", everyMinute{}, 0, p); err != nil {
		t.Fatalf("register wikipedia: %v", err)
	}
	return reg
}

// runInBackground starts the scheduler in a goroutine and returns a channel that
// closes when Run returns.
func runInBackground(ctx context.Context, s *Scheduler) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	return done
}
