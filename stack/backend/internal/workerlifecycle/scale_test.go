package workerlifecycle

import (
	"testing"
	"time"
)

func TestComputeDesiredCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		scaling ServiceScaling
		current int
		backlog int64
		want    int
	}{
		{
			name:    "max zero disables service",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 0},
			current: 5, backlog: 9000, want: 0,
		},
		{
			name:    "non-positive ratio holds current",
			scaling: ServiceScaling{Ratio: 0, Min: 1, Max: 10},
			current: 3, backlog: 9000, want: 3,
		},
		{
			name:    "empty queue clamps to min",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 10},
			current: 4, backlog: 0, want: 2, // halve toward 0, then floor at min 1 -> max(1,2)=2
		},
		{
			name:    "empty queue from zero stays at min",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 10},
			current: 0, backlog: 0, want: 1,
		},
		{
			name:    "ceiling division rounds fractional worker up",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 10},
			current: 5, backlog: 450, want: 5, // ceil(450/100)=5
		},
		{
			name:    "ceiling rounds 401 up to 5",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 10},
			current: 5, backlog: 401, want: 5,
		},
		{
			name:    "scale up at most doubles",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 100},
			current: 2, backlog: 9000, want: 4, // raw 90, stepped min(90, 2*2)=4
		},
		{
			name:    "scale up from zero floors at one then doubles to two",
			scaling: ServiceScaling{Ratio: 100, Min: 0, Max: 100},
			current: 0, backlog: 9000, want: 2, // raw 90, min(90, max(1,0)*2)=2
		},
		{
			name:    "scale down at most halves",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 100},
			current: 10, backlog: 100, want: 5, // raw 1, stepped max(1, 10/2)=5
		},
		{
			name:    "max clamps the step",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 6},
			current: 5, backlog: 9000, want: 6, // raw 90, double to 10, clamp to 6
		},
		{
			name:    "min clamps the step",
			scaling: ServiceScaling{Ratio: 100, Min: 3, Max: 10},
			current: 4, backlog: 0, want: 3, // halve to 2, floor at min 3
		},
		{
			name:    "steady state holds",
			scaling: ServiceScaling{Ratio: 100, Min: 1, Max: 10},
			current: 5, backlog: 500, want: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.scaling.ComputeDesiredCount(tc.current, tc.backlog); got != tc.want {
				t.Fatalf("ComputeDesiredCount(%d, %d) = %d, want %d", tc.current, tc.backlog, got, tc.want)
			}
		})
	}
}

func TestCooldownActive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	scaling := ServiceScaling{Cooldown: 3 * time.Minute}
	tests := []struct {
		name       string
		lastScaled time.Time
		want       bool
	}{
		{name: "never scaled", lastScaled: time.Time{}, want: false},
		{name: "inside cooldown", lastScaled: now.Add(-1 * time.Minute), want: true},
		{name: "exactly at cooldown edge", lastScaled: now.Add(-3 * time.Minute), want: false},
		{name: "past cooldown", lastScaled: now.Add(-5 * time.Minute), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := scaling.CooldownActive(tc.lastScaled, now); got != tc.want {
				t.Fatalf("CooldownActive(%v) = %v, want %v", tc.lastScaled, got, tc.want)
			}
		})
	}
}

func TestNewestVersionedDepth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		depths map[string]int64
		base   string
		want   int64
		ok     bool
	}{
		{
			name:   "single version",
			depths: map[string]int64{"embedding.jobs.v1": 42},
			base:   "embedding.jobs", want: 42, ok: true,
		},
		{
			name: "newest version's depth drives scaling",
			depths: map[string]int64{
				"embedding.jobs.v1": 999, // old, draining
				"embedding.jobs.v2": 30,  // newest, what PRIMARY consumes
			},
			base: "embedding.jobs", want: 30, ok: true,
		},
		{
			name: "numeric newest not lexicographic",
			depths: map[string]int64{
				"embedding.jobs.v2":  5,
				"embedding.jobs.v10": 7,
			},
			base: "embedding.jobs", want: 7, ok: true,
		},
		{
			name:   "no versioned queue",
			depths: map[string]int64{"embedding.jobs": 5, "other.v1": 9},
			base:   "embedding.jobs", want: 0, ok: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := NewestVersionedDepth(tc.depths, tc.base)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("NewestVersionedDepth = (%d,%v), want (%d,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
