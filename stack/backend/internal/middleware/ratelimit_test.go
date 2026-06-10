package middleware

import (
	"strconv"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestRateLimiterAllow(t *testing.T) {
	t.Parallel()
	// One token every 10 minutes, burst of 2: the first two calls pass, the
	// third is throttled well before any refill can land.
	rl := NewRateLimiter(rate.Every(10*time.Minute), 2)

	if !rl.Allow("1.2.3.4") {
		t.Fatal("first attempt should be allowed")
	}
	if !rl.Allow("1.2.3.4") {
		t.Fatal("second attempt within burst should be allowed")
	}
	if rl.Allow("1.2.3.4") {
		t.Fatal("third attempt should be throttled")
	}
}

func TestRateLimiterFailsClosedWhenFull(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(rate.Every(10*time.Minute), 1)

	if !rl.Allow("known") {
		t.Fatal("known key should be allowed before the map fills")
	}
	for i := range 10_000 {
		rl.Allow("filler-" + strconv.Itoa(i))
	}
	if rl.Allow("fresh-after-full") {
		t.Fatal("a new key must be denied while the map is full")
	}
	if rl.Allow("known") {
		t.Fatal("the known key must keep its exhausted throttle state; a key flood must not reset it")
	}
}

func TestRateLimiterEvictsRefilledBucketsWhenFull(t *testing.T) {
	t.Parallel()
	// With an infinite refill rate every bucket is always fully refilled, so
	// it carries no throttle state worth keeping and a newcomer may evict it.
	rl := NewRateLimiter(rate.Inf, 1)
	for i := range 10_000 {
		rl.Allow("filler-" + strconv.Itoa(i))
	}
	if !rl.Allow("newcomer") {
		t.Fatal("a new key must evict a fully refilled bucket instead of being denied")
	}
}

func TestRateLimiterIsolatesKeys(t *testing.T) {
	t.Parallel()
	rl := NewRateLimiter(rate.Every(10*time.Minute), 1)

	if !rl.Allow("1.2.3.4") {
		t.Fatal("first key should be allowed")
	}
	if rl.Allow("1.2.3.4") {
		t.Fatal("first key should now be throttled")
	}
	if !rl.Allow("5.6.7.8") {
		t.Fatal("a different key must have its own bucket")
	}
}
