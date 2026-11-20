package crawlnotify

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunConsumerWithAlerts(t *testing.T) {
	t.Parallel()

	runErr := errors.New("broker gone")

	tests := []struct {
		name       string
		stats      ConsumerStats
		runErr     error
		wantErr    error
		wantEvents []CrawlEvent
	}{
		{
			name:  "clean drain emits start then stopped",
			stats: ConsumerStats{Processed: 42, ParkedToDLQ: 3},
			wantEvents: []CrawlEvent{
				ConsumerStarted{Consumer: "embedding", Queue: "embedding.jobs.v1"},
				ConsumerStopped{Consumer: "embedding", Queue: "embedding.jobs.v1", Processed: 42, ParkedToDLQ: 3},
			},
		},
		{
			name:    "failure emits start then failed with partial counts",
			stats:   ConsumerStats{Processed: 5, ParkedToDLQ: 1},
			runErr:  runErr,
			wantErr: runErr,
			wantEvents: []CrawlEvent{
				ConsumerStarted{Consumer: "scrutins", Queue: "scrutins.jobs.v1"},
				ConsumerFailed{Consumer: "scrutins", Queue: "scrutins.jobs.v1", Processed: 5, ParkedToDLQ: 1, Err: runErr},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recordingNotifier{}
			consumer, queue := tc.wantEvents[0].(ConsumerStarted).Consumer, tc.wantEvents[0].(ConsumerStarted).Queue
			runs := 0
			run := func(context.Context) (ConsumerStats, error) {
				runs++
				return tc.stats, tc.runErr
			}

			stats, err := RunConsumerWithAlerts(t.Context(), rec, consumer, queue, run)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RunConsumerWithAlerts err = %v, want %v", err, tc.wantErr)
			}
			if stats != tc.stats {
				t.Errorf("stats = %+v, want %+v", stats, tc.stats)
			}
			if runs != 1 {
				t.Errorf("drain ran %d times, want 1", runs)
			}
			assertConsumerEventsEqual(t, rec.events, tc.wantEvents)
		})
	}
}

// TestRunConsumerWithAlertsCancelledStillAlerts proves the stop alert lands even
// when the drain returns because its own context was canceled (a SIGTERM): the
// outcome alert runs on a context detached from the drain's.
func TestRunConsumerWithAlertsCancelledStillAlerts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec := &recordingNotifier{}
	run := func(context.Context) (ConsumerStats, error) {
		return ConsumerStats{Processed: 9, ParkedToDLQ: 2}, nil
	}

	stats, err := RunConsumerWithAlerts(ctx, rec, "embedding", "embedding.jobs.v1", run)
	if err != nil {
		t.Fatalf("RunConsumerWithAlerts err = %v, want nil", err)
	}
	if stats != (ConsumerStats{Processed: 9, ParkedToDLQ: 2}) {
		t.Errorf("stats = %+v", stats)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + stopped", len(rec.events))
	}
	if _, ok := rec.events[1].(ConsumerStopped); !ok {
		t.Fatalf("second event = %#v, want ConsumerStopped", rec.events[1])
	}
	// The stop alert (second Notify) must run on a live context even though the
	// drain's context was already canceled.
	if rec.ctxErrs[1] != nil {
		t.Errorf("stop-alert context was already done (err=%v); the outcome alert must run on a live context", rec.ctxErrs[1])
	}
}

// TestRunConsumerWithAlertsMeasuresDuration proves the stopped event carries the
// drain's real elapsed time.
func TestRunConsumerWithAlertsMeasuresDuration(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	const minElapsed = 20 * time.Millisecond
	run := func(context.Context) (ConsumerStats, error) {
		<-time.After(minElapsed)
		return ConsumerStats{}, nil
	}

	if _, err := RunConsumerWithAlerts(t.Context(), rec, "embedding", "embedding.jobs.v1", run); err != nil {
		t.Fatalf("RunConsumerWithAlerts: %v", err)
	}
	stopped, ok := rec.events[len(rec.events)-1].(ConsumerStopped)
	if !ok {
		t.Fatalf("last event = %#v, want ConsumerStopped", rec.events[len(rec.events)-1])
	}
	if stopped.Duration < minElapsed {
		t.Errorf("Duration = %v, want >= %v", stopped.Duration, minElapsed)
	}
}

func TestConsumerEventMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event CrawlEvent
		want  string
	}{
		{
			name:  "started",
			event: ConsumerStarted{Consumer: "embedding", Queue: "embedding.jobs.v1"},
			want:  "Consumer started: embedding (embedding.jobs.v1)",
		},
		{
			name:  "stopped",
			event: ConsumerStopped{Consumer: "embedding", Queue: "embedding.jobs.v1", Processed: 120, ParkedToDLQ: 4, Duration: 90 * time.Second},
			want:  "Consumer stopped: embedding (embedding.jobs.v1) - 120 processed, 4 parked to DLQ in 1m30s",
		},
		{
			name:  "failed",
			event: ConsumerFailed{Consumer: "scrutins", Queue: "scrutins.jobs.v1", Processed: 5, ParkedToDLQ: 1, Err: errors.New("broker gone")},
			want:  "Consumer FAILED: scrutins (scrutins.jobs.v1) - 5 processed, 1 parked to DLQ - broker gone",
		},
		{
			name:  "failed with nil error",
			event: ConsumerFailed{Consumer: "scrutins", Queue: "scrutins.jobs.v1"},
			want:  "Consumer FAILED: scrutins (scrutins.jobs.v1) - 0 processed, 0 parked to DLQ - unknown error",
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

// assertConsumerEventsEqual compares two event slices by type and field,
// ignoring a stopped event's measured Duration (wall-clock, not deterministic).
func assertConsumerEventsEqual(t *testing.T, got, want []CrawlEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		switch w := want[i].(type) {
		case ConsumerStarted:
			g, ok := got[i].(ConsumerStarted)
			if !ok || g != w {
				t.Errorf("event %d = %#v, want %#v", i, got[i], w)
			}
		case ConsumerStopped:
			g, ok := got[i].(ConsumerStopped)
			if !ok || g.Consumer != w.Consumer || g.Queue != w.Queue ||
				g.Processed != w.Processed || g.ParkedToDLQ != w.ParkedToDLQ {
				t.Errorf("event %d = %#v, want %#v", i, got[i], w)
			}
		case ConsumerFailed:
			g, ok := got[i].(ConsumerFailed)
			if !ok || g.Consumer != w.Consumer || g.Queue != w.Queue ||
				g.Processed != w.Processed || g.ParkedToDLQ != w.ParkedToDLQ || !errors.Is(g.Err, w.Err) {
				t.Errorf("event %d = %#v, want %#v", i, got[i], w)
			}
		default:
			t.Fatalf("unexpected want event type %T", want[i])
		}
	}
}
