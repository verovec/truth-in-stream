package crawlnotify

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingNotifier captures every event it is handed, in order, along with the
// liveness of the context each Notify ran under, so a test can assert both the
// exact start-then-(finish|fail) sequence and that the outcome alert ran on a
// live (non-canceled) context.
type recordingNotifier struct {
	events  []CrawlEvent
	ctxErrs []error
	err     error
}

func (r *recordingNotifier) Notify(ctx context.Context, e CrawlEvent) error {
	r.events = append(r.events, e)
	r.ctxErrs = append(r.ctxErrs, ctx.Err())
	return r.err
}

// fakeProducer is a Producer whose run result and error are fixed by the test.
type fakeProducer struct {
	name  string
	scope string
	stats Stats
	err   error
	runs  int
}

func (f *fakeProducer) Name() string  { return f.name }
func (f *fakeProducer) Scope() string { return f.scope }

func (f *fakeProducer) Run(context.Context) (Stats, error) {
	f.runs++
	return f.stats, f.err
}

func TestRunWithAlerts(t *testing.T) {
	t.Parallel()

	runErr := errors.New("boom")

	tests := []struct {
		name      string
		producer  *fakeProducer
		wantStats Stats
		wantErr   error
		// wantEvents is the ordered sequence of event types the notifier must see.
		wantEvents []CrawlEvent
	}{
		{
			name: "success emits start then finish",
			producer: &fakeProducer{
				name:  "wikipedia",
				scope: "category:Physics",
				stats: Stats{New: 3, Updated: 2, Skipped: 1},
			},
			wantStats: Stats{New: 3, Updated: 2, Skipped: 1},
			wantEvents: []CrawlEvent{
				RunStarted{Source: "wikipedia", Scope: "category:Physics"},
				RunFinished{Source: "wikipedia", Scope: "category:Physics", New: 3, Updated: 2, Skipped: 1},
			},
		},
		{
			name: "failure emits start then fail",
			producer: &fakeProducer{
				name:  "scrutins",
				scope: "legislature:17",
				err:   runErr,
			},
			wantErr: runErr,
			wantEvents: []CrawlEvent{
				RunStarted{Source: "scrutins", Scope: "legislature:17"},
				RunFailed{Source: "scrutins", Scope: "legislature:17", Err: runErr},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingNotifier{}
			stats, err := RunWithAlerts(t.Context(), rec, tc.producer)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RunWithAlerts err = %v, want %v", err, tc.wantErr)
			}
			if stats != tc.wantStats {
				t.Errorf("stats = %+v, want %+v", stats, tc.wantStats)
			}
			if tc.producer.runs != 1 {
				t.Errorf("producer ran %d times, want 1", tc.producer.runs)
			}
			assertEventsEqual(t, rec.events, tc.wantEvents)
		})
	}
}

// assertEventsEqual compares two event slices by type and field, ignoring the
// finish event's measured Duration (which is wall-clock and not deterministic).
func assertEventsEqual(t *testing.T, got, want []CrawlEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		switch w := want[i].(type) {
		case RunStarted:
			g, ok := got[i].(RunStarted)
			if !ok || g != w {
				t.Errorf("event %d = %#v, want %#v", i, got[i], w)
			}
		case RunFailed:
			g, ok := got[i].(RunFailed)
			if !ok || g.Source != w.Source || g.Scope != w.Scope || !errors.Is(g.Err, w.Err) {
				t.Errorf("event %d = %#v, want %#v", i, got[i], w)
			}
		case RunFinished:
			g, ok := got[i].(RunFinished)
			if !ok || g.Source != w.Source || g.Scope != w.Scope ||
				g.New != w.New || g.Updated != w.Updated || g.Skipped != w.Skipped {
				t.Errorf("event %d = %#v, want %#v", i, got[i], w)
			}
		default:
			t.Fatalf("unexpected want event type %T", want[i])
		}
	}
}

// gatedProducer blocks in Run until released, so a test can prove the measured
// duration brackets a real, observable elapsed window without a time.Sleep.
type gatedProducer struct {
	release <-chan struct{}
}

func (gatedProducer) Name() string  { return "wikipedia" }
func (gatedProducer) Scope() string { return "category:Physics" }

func (g gatedProducer) Run(context.Context) (Stats, error) {
	<-g.release
	return Stats{}, nil
}

// TestRunWithAlertsMeasuresDuration proves the finish event carries the run's
// real elapsed time: the producer is held until a known wall-clock window has
// passed, so the reported Duration must be at least that window.
func TestRunWithAlertsMeasuresDuration(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	rec := &recordingNotifier{}

	const minElapsed = 20 * time.Millisecond
	go func() {
		<-time.After(minElapsed)
		close(release)
	}()

	if _, err := RunWithAlerts(t.Context(), rec, gatedProducer{release: release}); err != nil {
		t.Fatalf("RunWithAlerts: %v", err)
	}
	fin, ok := rec.events[len(rec.events)-1].(RunFinished)
	if !ok {
		t.Fatalf("last event = %#v, want RunFinished", rec.events[len(rec.events)-1])
	}
	if fin.Duration < minElapsed {
		t.Errorf("Duration = %v, want >= %v", fin.Duration, minElapsed)
	}
}

// TestRunWithAlertsCancelledRunStillAlerts proves the failure alert is delivered
// even when the producer aborts because its own context was canceled: the
// outcome alert runs on a context detached from the producer's, so the operator
// is always told a run failed - the moment an alert matters most.
func TestRunWithAlertsCancelledRunStillAlerts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // already done before the run

	rec := &recordingNotifier{}
	p := cancelAwareProducer{}

	_, err := RunWithAlerts(ctx, rec, p)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + fail", len(rec.events))
	}
	fail, ok := rec.events[1].(RunFailed)
	if !ok {
		t.Fatalf("second event = %#v, want RunFailed", rec.events[1])
	}
	if !errors.Is(fail.Err, context.Canceled) {
		t.Errorf("RunFailed.Err = %v, want context.Canceled", fail.Err)
	}
	// The fail alert (second Notify) must run on a live context even though the
	// producer's run context was already canceled.
	if rec.ctxErrs[1] != nil {
		t.Errorf("fail-alert context was already done (err=%v); the outcome alert must run on a live context", rec.ctxErrs[1])
	}
}

// cancelAwareProducer fails with its run context's error, so a canceled context
// drives a context.Canceled run failure.
type cancelAwareProducer struct{}

func (cancelAwareProducer) Name() string  { return "scrutins" }
func (cancelAwareProducer) Scope() string { return "legislature:17" }

func (cancelAwareProducer) Run(ctx context.Context) (Stats, error) {
	return Stats{}, ctx.Err()
}

// TestRunWithAlertsStartNotifyErrorDoesNotAbort proves a notifier failure on the
// start event never blocks the producer run: alerting is best-effort.
func TestRunWithAlertsStartNotifyErrorDoesNotAbort(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{err: errors.New("slack down")}
	p := &fakeProducer{name: "wikipedia", scope: "category:Physics", stats: Stats{New: 1}}

	stats, err := RunWithAlerts(t.Context(), rec, p)
	if err != nil {
		t.Fatalf("RunWithAlerts err = %v, want nil (notify errors are swallowed)", err)
	}
	if stats != (Stats{New: 1}) {
		t.Errorf("stats = %+v, want New:1", stats)
	}
	if p.runs != 1 {
		t.Errorf("producer ran %d times, want 1", p.runs)
	}
	if len(rec.events) != 2 {
		t.Errorf("got %d events, want 2 (start + finish)", len(rec.events))
	}
}

func TestNewNotifier(t *testing.T) {
	t.Parallel()

	t.Run("empty webhook is a noop", func(t *testing.T) {
		t.Parallel()
		n := NewNotifier("")
		if _, ok := n.(NoopNotifier); !ok {
			t.Fatalf("NewNotifier(\"\") = %T, want NoopNotifier", n)
		}
		// A noop never errors regardless of the event.
		if err := n.Notify(t.Context(), RunStarted{Source: "wikipedia", Scope: "x"}); err != nil {
			t.Errorf("noop Notify err = %v, want nil", err)
		}
	})

	t.Run("set webhook builds a slack notifier", func(t *testing.T) {
		t.Parallel()
		n := NewNotifier("https://hooks.slack.com/services/T/B/X")
		if _, ok := n.(*SlackNotifier); !ok {
			t.Fatalf("NewNotifier(url) = %T, want *SlackNotifier", n)
		}
	})
}

func TestEventMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event CrawlEvent
		want  string
	}{
		{
			name:  "started",
			event: RunStarted{Source: "wikipedia", Scope: "category:Physics"},
			want:  "Ingestion started: wikipedia (category:Physics)",
		},
		{
			name: "finished",
			event: RunFinished{
				Source: "wikipedia", Scope: "category:Physics",
				New: 3, Updated: 2, Skipped: 1, Duration: 90 * time.Second,
			},
			want: "Ingestion finished: wikipedia (category:Physics) - 3 new, 2 updated, 1 skipped in 1m30s",
		},
		{
			name:  "failed",
			event: RunFailed{Source: "scrutins", Scope: "legislature:17", Err: errors.New("download failed")},
			want:  "Ingestion FAILED: scrutins (legislature:17) - download failed",
		},
		{
			name:  "failed with nil error",
			event: RunFailed{Source: "scrutins", Scope: "legislature:17"},
			want:  "Ingestion FAILED: scrutins (legislature:17) - unknown error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.event.message(); got != tc.want {
				t.Errorf("message() = %q, want %q", got, tc.want)
			}
		})
	}
}
