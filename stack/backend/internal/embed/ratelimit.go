package embed

import (
	"context"
	"fmt"

	"golang.org/x/time/rate"
)

// RateLimitedClient paces document-embedding requests through a token-bucket
// limiter so a constrained provider tier is not overrun. It decorates a
// document embedder; the bulk pipeline places it beneath the retry decorator so
// every attempt - including retries - waits its turn, and a shared limiter paces
// all concurrent workers to one combined request budget.
type RateLimitedClient struct {
	inner   docEmbedder
	limiter *rate.Limiter
}

// WithRateLimit paces inner to at most requestsPerMinute outbound calls. A
// non-positive limit returns inner unwrapped, so pacing is strictly opt-in and a
// paid tier whose limit is thousands of RPM pays nothing for it.
func WithRateLimit(inner docEmbedder, requestsPerMinute int) docEmbedder {
	if requestsPerMinute <= 0 {
		return inner
	}
	// Per-minute to per-second; burst 1 enforces a steady cadence rather than
	// letting a whole minute's budget fire at once and trip the very limit being
	// paced under.
	limit := rate.Limit(float64(requestsPerMinute) / 60.0)
	return &RateLimitedClient{inner: inner, limiter: rate.NewLimiter(limit, 1)}
}

// EmbedDocuments blocks until the limiter admits the call, then delegates. A
// canceled or expired context surfaces at once, before any embedding work, so a
// -max-duration budget stops a paced run cleanly.
func (r *RateLimitedClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("embed: rate limit wait: %w", err)
	}
	return r.inner.EmbedDocuments(ctx, texts)
}
