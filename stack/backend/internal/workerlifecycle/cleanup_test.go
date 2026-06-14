package workerlifecycle

import (
	"slices"
	"testing"
	"time"
)

func TestRetirableVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		depths map[string]int64
		base   string
		want   []string
	}{
		{
			name:   "single version never retires",
			depths: map[string]int64{"embedding.jobs.v2": 0},
			base:   "embedding.jobs", want: nil,
		},
		{
			name: "drained old version retires, current excluded",
			depths: map[string]int64{
				"embedding.jobs.v1": 0,
				"embedding.jobs.v2": 5, // newest, current
			},
			base: "embedding.jobs", want: []string{"1"},
		},
		{
			name: "old version with messages is not retired",
			depths: map[string]int64{
				"embedding.jobs.v1": 3, // still draining
				"embedding.jobs.v2": 0,
			},
			base: "embedding.jobs", want: nil, // v1 has messages, v2 is current
		},
		{
			name: "multiple drained old versions",
			depths: map[string]int64{
				"embedding.jobs.v1": 0,
				"embedding.jobs.v2": 0,
				"embedding.jobs.v3": 1, // newest, current
			},
			base: "embedding.jobs", want: []string{"1", "2"},
		},
		{
			name: "current version is empty but never retirable",
			depths: map[string]int64{
				"embedding.jobs.v1": 2,
				"embedding.jobs.v2": 0, // newest and empty, still current
			},
			base: "embedding.jobs", want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RetirableVersions(tc.depths, tc.base)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("RetirableVersions = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSafeToRetire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		primaryRunning int
		desired        int
		want           bool
	}{
		{name: "service scaled to zero is safe", primaryRunning: 0, desired: 0, want: true},
		{name: "primary fully up", primaryRunning: 3, desired: 3, want: true},
		{name: "primary over-provisioned", primaryRunning: 4, desired: 3, want: true},
		{name: "primary still coming up", primaryRunning: 1, desired: 3, want: false},
		{name: "primary down", primaryRunning: 0, desired: 2, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SafeToRetire(tc.primaryRunning, tc.desired); got != tc.want {
				t.Fatalf("SafeToRetire(%d,%d) = %v, want %v", tc.primaryRunning, tc.desired, got, tc.want)
			}
		})
	}
}

func TestRetireReason(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	policy := RetirePolicy{
		MaxAge:            30 * time.Minute,
		SameVersionMinAge: 2 * time.Minute,
		ZombieMinAge:      15 * time.Minute,
	}
	primary := PrimaryTaskSet{
		Version:      "2",
		CreatedAt:    now.Add(-1 * time.Hour), // older than MaxAge
		RunningCount: 3,
	}
	retirable := map[string]bool{"1": true}

	tests := []struct {
		name    string
		ts      TaskSet
		primary PrimaryTaskSet
		want    bool
	}{
		{
			name:    "orphan with no version always retires",
			ts:      TaskSet{ID: "a", Version: "", CreatedAt: now, RunningCount: 1},
			primary: primary, want: true,
		},
		{
			name:    "superseded same-version old enough",
			ts:      TaskSet{ID: "b", Version: "2", CreatedAt: now.Add(-5 * time.Minute), RunningCount: 2},
			primary: primary, want: true,
		},
		{
			name:    "same-version too young is kept",
			ts:      TaskSet{ID: "c", Version: "2", CreatedAt: now.Add(-1 * time.Minute), RunningCount: 2},
			primary: primary, want: false,
		},
		{
			name:    "drained old version with aged primary retires",
			ts:      TaskSet{ID: "d", Version: "1", CreatedAt: now.Add(-40 * time.Minute), RunningCount: 1},
			primary: primary, want: true,
		},
		{
			name: "drained old version but primary too fresh is kept",
			ts:   TaskSet{ID: "e", Version: "1", CreatedAt: now.Add(-40 * time.Minute), RunningCount: 1},
			primary: PrimaryTaskSet{
				Version: "2", CreatedAt: now.Add(-5 * time.Minute), RunningCount: 3,
			},
			want: false,
		},
		{
			name:    "old version not yet drained is kept",
			ts:      TaskSet{ID: "f", Version: "0", CreatedAt: now.Add(-40 * time.Minute), RunningCount: 1},
			primary: primary, want: false, // version 0 absent from retirable
		},
		{
			name:    "zombie with no running tasks retires",
			ts:      TaskSet{ID: "g", Version: "9", CreatedAt: now.Add(-20 * time.Minute), RunningCount: 0},
			primary: primary, want: true,
		},
		{
			name:    "young zombie is kept",
			ts:      TaskSet{ID: "h", Version: "9", CreatedAt: now.Add(-5 * time.Minute), RunningCount: 0},
			primary: primary, want: false,
		},
		{
			name:    "zero CreatedAt never triggers zombie deletion",
			ts:      TaskSet{ID: "i", Version: "9", CreatedAt: time.Time{}, RunningCount: 0},
			primary: primary, want: false,
		},
		{
			name:    "zero CreatedAt never triggers same-version deletion",
			ts:      TaskSet{ID: "j", Version: "2", CreatedAt: time.Time{}, RunningCount: 2},
			primary: primary, want: false,
		},
		{
			name: "zero primary CreatedAt never triggers drain deletion",
			ts:   TaskSet{ID: "k", Version: "1", CreatedAt: now.Add(-40 * time.Minute), RunningCount: 1},
			primary: PrimaryTaskSet{
				Version: "2", CreatedAt: time.Time{}, RunningCount: 3,
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reason, got := RetireReason(tc.ts, tc.primary, retirable, policy, now)
			if got != tc.want {
				t.Fatalf("RetireReason ok = %v, want %v (reason %q)", got, tc.want, reason)
			}
			if got && reason == "" {
				t.Fatal("RetireReason returned true with an empty reason")
			}
			if !got && reason != "" {
				t.Fatalf("RetireReason returned false with a non-empty reason %q", reason)
			}
		})
	}
}
