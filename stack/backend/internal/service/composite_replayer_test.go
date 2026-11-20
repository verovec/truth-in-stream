package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// stubSnapshotSource is a scripted SnapshotSource that records whether it was
// consulted.
type stubSnapshotSource struct {
	events []LiveEvent
	found  bool
	err    error
	calls  int
}

func (s *stubSnapshotSource) Snapshot(context.Context, string) ([]LiveEvent, bool, error) {
	s.calls++
	return s.events, s.found, s.err
}

func TestCompositeReplayerPrecedence(t *testing.T) {
	t.Parallel()
	postgresEvents := []LiveEvent{{Kind: LiveEventSubtitle, ID: "pg"}}
	redisEvents := []LiveEvent{{Kind: LiveEventSubtitle, ID: "redis"}}

	tests := []struct {
		name          string
		first, second *stubSnapshotSource
		wantEvents    []LiveEvent
		wantFound     bool
		wantSecond    int
	}{
		{
			name:       "first source wins and the second is never consulted",
			first:      &stubSnapshotSource{events: postgresEvents, found: true},
			second:     &stubSnapshotSource{events: redisEvents, found: true},
			wantEvents: postgresEvents,
			wantFound:  true,
			wantSecond: 0,
		},
		{
			name:       "first miss falls through to the second",
			first:      &stubSnapshotSource{},
			second:     &stubSnapshotSource{events: redisEvents, found: true},
			wantEvents: redisEvents,
			wantFound:  true,
			wantSecond: 1,
		},
		{
			name:       "first error is absorbed and the second still serves",
			first:      &stubSnapshotSource{err: errors.New("boom")},
			second:     &stubSnapshotSource{events: redisEvents, found: true},
			wantEvents: redisEvents,
			wantFound:  true,
			wantSecond: 1,
		},
		{
			name:       "all misses report one clean miss",
			first:      &stubSnapshotSource{},
			second:     &stubSnapshotSource{},
			wantFound:  false,
			wantSecond: 1,
		},
		{
			name:       "all errors degrade to a clean miss",
			first:      &stubSnapshotSource{err: errors.New("boom")},
			second:     &stubSnapshotSource{err: errors.New("boom")},
			wantFound:  false,
			wantSecond: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, err := NewCompositeReplayer(discardLogger(), tc.first, tc.second)
			if err != nil {
				t.Fatalf("NewCompositeReplayer: %v", err)
			}
			events, found, err := c.Snapshot(t.Context(), "v1")
			if err != nil {
				t.Fatalf("Snapshot must degrade, not fail: %v", err)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if diff := cmp.Diff(tc.wantEvents, events); diff != "" {
				t.Errorf("events mismatch (-want +got):\n%s", diff)
			}
			if tc.first.calls != 1 {
				t.Errorf("first source consulted %d times, want 1", tc.first.calls)
			}
			if tc.second.calls != tc.wantSecond {
				t.Errorf("second source consulted %d times, want %d", tc.second.calls, tc.wantSecond)
			}
		})
	}
}

func TestNewCompositeReplayerValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewCompositeReplayer(discardLogger()); err == nil {
		t.Error("no sources should be rejected")
	}
	if _, err := NewCompositeReplayer(discardLogger(), &stubSnapshotSource{}, nil); err == nil {
		t.Error("a nil source should be rejected")
	}
}
