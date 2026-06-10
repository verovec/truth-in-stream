package middleware

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// staleAfter is how long an idle key keeps its bucket before the sweep drops
// it. One sweep interval at the login rate keeps the map bounded by the
// number of distinct clients seen per interval.
const staleAfter = 15 * time.Minute

// maxBuckets caps the key map between time-based sweeps. When a flood of
// distinct keys fills the map, a newcomer may only evict a bucket whose
// tokens are fully refilled - one carrying no throttle state - and is denied
// otherwise. An attacker can therefore neither reset their own exhausted
// bucket by spraying fresh keys nor permanently lock new clients out by
// keeping the map full of idle entries.
const maxBuckets = 10_000

// evictSample bounds how many map entries one overflow insert may inspect;
// Go's randomized map iteration keeps the sample fair.
const evictSample = 64

// RateLimiter throttles attempts per key (a client IP) with a token bucket
// per key. Idle buckets are swept so the map cannot grow without bound under
// address-rotating abuse.
type RateLimiter struct {
	limit rate.Limit
	burst int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter builds a limiter where each key refills at limit and may
// burst up to burst attempts.
func NewRateLimiter(limit rate.Limit, burst int) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		burst:   burst,
		buckets: make(map[string]*bucket),
	}
}

// Allow reports whether key may make another attempt now. Only allowed
// attempts refresh a bucket's idle clock: denied traffic must not keep a
// bucket alive, or a flood could hold the whole map warm forever.
func (r *RateLimiter) Allow(key string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweep(now)
	b, ok := r.buckets[key]
	if !ok {
		if len(r.buckets) >= maxBuckets && !r.evictRefilled() {
			return false
		}
		b = &bucket{limiter: rate.NewLimiter(r.limit, r.burst)}
		r.buckets[key] = b
	}
	if !b.limiter.Allow() {
		return false
	}
	b.lastSeen = now
	return true
}

// evictRefilled drops one sampled bucket whose tokens are back to the full
// burst, reporting whether it made room. A fully refilled bucket holds no
// throttle history, so evicting it never weakens an active limit.
func (r *RateLimiter) evictRefilled() bool {
	inspected := 0
	for key, b := range r.buckets {
		if b.limiter.Tokens() >= float64(r.burst) {
			delete(r.buckets, key)
			return true
		}
		inspected++
		if inspected >= evictSample {
			break
		}
	}
	return false
}

// sweep drops buckets idle past staleAfter. Called with the mutex held; runs
// at most once per staleAfter so steady-state cost stays O(1) per Allow.
func (r *RateLimiter) sweep(now time.Time) {
	if now.Sub(r.lastSweep) < staleAfter {
		return
	}
	r.lastSweep = now
	for key, b := range r.buckets {
		if now.Sub(b.lastSeen) >= staleAfter {
			delete(r.buckets, key)
		}
	}
}
