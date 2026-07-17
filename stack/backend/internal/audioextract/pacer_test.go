package audioextract

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPacerWait(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		factor float64
		// steps alternate: wait for a frame of frameMs, then advance the
		// clock by advance (simulated downstream processing time).
		steps []struct {
			frameMs int
			advance time.Duration
		}
		wantSleeps []time.Duration
	}{
		{
			name:   "realtime paces at frame duration",
			factor: 1.0,
			steps: []struct {
				frameMs int
				advance time.Duration
			}{{100, 0}, {100, 0}, {100, 0}},
			wantSleeps: []time.Duration{100 * time.Millisecond, 100 * time.Millisecond},
		},
		{
			name:   "half factor doubles the spacing",
			factor: 0.5,
			steps: []struct {
				frameMs int
				advance time.Duration
			}{{100, 0}, {100, 0}},
			wantSleeps: []time.Duration{200 * time.Millisecond},
		},
		{
			name:   "double factor halves the spacing",
			factor: 2.0,
			steps: []struct {
				frameMs int
				advance time.Duration
			}{{100, 0}, {100, 0}},
			wantSleeps: []time.Duration{50 * time.Millisecond},
		},
		{
			name:   "processing time is absorbed not added",
			factor: 1.0,
			steps: []struct {
				frameMs int
				advance time.Duration
			}{{100, 60 * time.Millisecond}, {100, 0}},
			wantSleeps: []time.Duration{40 * time.Millisecond},
		},
		{
			name:   "slower-than-realtime consumer never sleeps",
			factor: 1.0,
			steps: []struct {
				frameMs int
				advance time.Duration
			}{{100, 150 * time.Millisecond}, {100, 150 * time.Millisecond}, {100, 0}},
			wantSleeps: nil,
		},
		{
			name:   "short tail frame paces by its own duration",
			factor: 1.0,
			steps: []struct {
				frameMs int
				advance time.Duration
			}{{100, 0}, {50, 0}, {100, 0}},
			wantSleeps: []time.Duration{100 * time.Millisecond, 50 * time.Millisecond},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clk := &fakeClock{now: time.Unix(0, 0)}
			p := newPacer(clk, tc.factor)
			for _, step := range tc.steps {
				if err := p.wait(t.Context(), step.frameMs); err != nil {
					t.Fatalf("wait: %v", err)
				}
				clk.now = clk.now.Add(step.advance)
			}
			if len(clk.sleeps) != len(tc.wantSleeps) {
				t.Fatalf("sleeps = %v, want %v", clk.sleeps, tc.wantSleeps)
			}
			for i, d := range clk.sleeps {
				if d != tc.wantSleeps[i] {
					t.Errorf("sleep %d = %v, want %v", i, d, tc.wantSleeps[i])
				}
			}
		})
	}
}

func TestPacerWaitPropagatesCancellation(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	p := newPacer(clk, 1.0)
	if err := p.wait(t.Context(), 100); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.wait(ctx, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v, want context.Canceled", err)
	}
}

func TestRealClockSleepReturnsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if err := (realClock{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sleep blocked %v on a canceled context", elapsed)
	}
}
