package embed

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"
)

// RetryConfig tunes the rate-limit backoff. MaxAttempts counts the first try,
// so a value of 1 disables retrying. BaseDelay is the first backoff; each
// subsequent attempt doubles it up to MaxDelay, with full jitter applied so
// concurrent workers do not retry in lockstep.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// docEmbedder is the document-embedding surface RetryClient decorates; *Client
// satisfies it. Keeping it unexported keeps the decorator's seam internal while
// callers still pass a concrete *Client.
type docEmbedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// RetryClient decorates a document embedder with exponential backoff on Voyage
// rate-limit (HTTP 429) responses. Every other error - including other 4xx and
// 5xx statuses - is returned immediately, since only throttling is safely
// retriable without risking duplicate or wasted spend.
type RetryClient struct {
	inner docEmbedder
	cfg   RetryConfig
}

// defaultRetryBaseDelay is the floor for a misconfigured non-positive BaseDelay,
// keeping backoff from collapsing into a busy retry loop.
const defaultRetryBaseDelay = time.Second

// WithRetry wraps a document embedder so rate-limited calls are retried with
// jittered exponential backoff. The config is clamped to safe values: a
// non-positive MaxAttempts becomes one, a non-positive BaseDelay takes a
// one-second floor, and MaxDelay is never below BaseDelay - otherwise the
// doubling-then-cap would yield a zero delay and spin.
func WithRetry(inner docEmbedder, cfg RetryConfig) *RetryClient {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = defaultRetryBaseDelay
	}
	if cfg.MaxDelay < cfg.BaseDelay {
		cfg.MaxDelay = cfg.BaseDelay
	}
	return &RetryClient{inner: inner, cfg: cfg}
}

// EmbedDocuments calls the wrapped embedder, retrying on rate-limit responses
// until it succeeds, the attempts are exhausted, or the context is canceled.
func (r *RetryClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		out, err := r.inner.EmbedDocuments(ctx, texts)
		if err == nil {
			return out, nil
		}
		if !isRateLimited(err) {
			return nil, err
		}
		lastErr = err
		if attempt == r.cfg.MaxAttempts {
			break
		}
		if err := sleep(ctx, r.backoff(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("embed: rate-limited after %d attempts: %w", r.cfg.MaxAttempts, lastErr)
}

// backoff returns the jittered delay before the attempt+1 try. The base delay
// doubles per attempt, capped at MaxDelay, then full jitter picks a point in
// [0, delay].
func (r *RetryClient) backoff(attempt int) time.Duration {
	delay := r.cfg.BaseDelay
	for range attempt - 1 {
		delay *= 2
		if delay >= r.cfg.MaxDelay {
			delay = r.cfg.MaxDelay
			break
		}
	}
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay) + 1))
}

// isRateLimited reports whether err is a Voyage 429 response.
func isRateLimited(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
}

// sleep waits for d or until ctx is canceled, returning the context error in
// the latter case.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
