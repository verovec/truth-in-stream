package embed

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
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

// RetryClient decorates a document embedder with exponential backoff on
// transient failures: Voyage rate-limit (HTTP 429) responses and network
// timeouts (a single slow request must not abort a long resumable run). Every
// other error - including other 4xx and 5xx statuses - is returned immediately,
// since only throttling and timeouts are safely retriable for an idempotent
// embed without risking a wrong-cause retry. Cancellation or expiry of the
// caller's context outranks any retry and is surfaced at once.
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

// EmbedDocuments calls the wrapped embedder, retrying on transient failures
// (rate-limit responses and network timeouts) until it succeeds, the attempts
// are exhausted, or the context is canceled.
func (r *RetryClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		out, err := r.inner.EmbedDocuments(ctx, texts)
		if err == nil {
			return out, nil
		}
		// An expired or canceled caller context (a -max-duration budget or an
		// interrupt) outranks any retry: a request timeout that fires because the
		// parent deadline already passed must surface as the context error, not be
		// retried into a dead context.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isRetriable(err) {
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
	return nil, fmt.Errorf("embed: giving up after %d attempts: %w", r.cfg.MaxAttempts, lastErr)
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

// isRetriable reports whether err is a transient failure worth another attempt:
// a Voyage rate-limit response or a network timeout. The embed call is
// idempotent, so retrying a timeout risks only wasted spend, never bad data.
func isRetriable(err error) bool {
	return isRateLimited(err) || isTimeout(err)
}

// isRateLimited reports whether err is a Voyage 429 response.
func isRateLimited(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
}

// isTimeout reports whether err is a network timeout, including the
// http.Client.Timeout that fires while awaiting response headers. Detecting it
// via net.Error.Timeout is robust across Go versions, where such errors also
// satisfy errors.Is(err, context.DeadlineExceeded). A timeout produced because
// the caller's context already expired also matches here, so this MUST run
// after the ctx.Err() guard in EmbedDocuments, which short-circuits that case.
func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
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
