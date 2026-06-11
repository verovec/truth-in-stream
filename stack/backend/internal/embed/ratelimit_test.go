package embed

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestRateLimitDisabledWhenNonPositive(t *testing.T) {
	t.Parallel()
	f := &fakeDoc{}
	// A non-positive limit must not wrap: the bare embedder is returned so a paid
	// tier pays no pacing cost.
	for _, rpm := range []int{0, -1} {
		if got := WithRateLimit(f, rpm); got != docEmbedder(f) {
			t.Errorf("WithRateLimit(f, %d) wrapped the embedder; want it returned unchanged", rpm)
		}
	}
}

func TestRateLimitPacesRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 60 requests/minute = one per second; burst 1. The first call is admitted
		// at once, each subsequent call waits a second, so five calls span 4s.
		f := &fakeDoc{}
		rl := WithRateLimit(f, 60)

		start := time.Now()
		for range 5 {
			if _, err := rl.EmbedDocuments(t.Context(), []string{"x"}); err != nil {
				t.Fatalf("EmbedDocuments: %v", err)
			}
		}
		if elapsed := time.Since(start); elapsed != 4*time.Second {
			t.Errorf("five paced calls took %v, want 4s (1/s after an immediate first)", elapsed)
		}
		if f.calls != 5 {
			t.Errorf("inner called %d times, want 5", f.calls)
		}
	})
}

func TestRateLimitHonorsContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Burst 1 admits the first call; the limiter then makes the second wait. A
		// canceled context must abort that wait and never reach the inner embedder.
		f := &fakeDoc{}
		rl := WithRateLimit(f, 6) // one every ten seconds

		if _, err := rl.EmbedDocuments(t.Context(), []string{"x"}); err != nil {
			t.Fatalf("first call: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := rl.EmbedDocuments(ctx, []string{"x"}); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if f.calls != 1 {
			t.Errorf("inner called %d times, want 1 (the canceled call never ran)", f.calls)
		}
	})
}
