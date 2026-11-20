package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Retry defaults. Four retries after the first attempt, starting at 500ms and
// doubling, cap any single wait (and any honored Retry-After) at 30s, so an
// upstream throttle or a 5xx blip is ridden out without an unbounded stall.
const (
	defaultRetryMax      = 4
	defaultRetryBase     = 500 * time.Millisecond
	defaultRetryMaxDelay = 30 * time.Second
)

// Doer performs an HTTP request. *http.Client satisfies it, so a RetryClient can
// wrap the standard client (or a test double) transparently.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// RetryConfig tunes a RetryClient. A zero value selects the package defaults.
// MaxRetries is the number of retries after the first attempt: zero selects the
// default, and a negative value disables retries (one attempt only, useful in
// tests). BaseDelay is the first backoff step (doubled each retry); MaxDelay caps
// any single backoff wait and any Retry-After the helper honors.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// RetryClient wraps a Doer and retries a request that fails transiently: a
// transport error, a 429, or a 5xx other than 501 (Not Implemented, which a retry
// cannot fix). Backoff is exponential from BaseDelay with full jitter, floored by a
// Retry-After header when the server sends one and capped at MaxDelay. The request
// context bounds both the request and the backoff wait, so a canceled crawl stops
// promptly instead of retrying a context that will never recover. It never buffers
// a response body: a body it is about to discard before a retry is drained and
// closed for connection reuse, and the final response is returned to the caller
// intact.
//
// Retries assume an idempotent request. A request with a non-rewindable body is not
// retried (the first outcome is returned), so the helper is safe to drop in front
// of the GET-only upstream fetchers without risking a duplicate side effect.
type RetryClient struct {
	doer     Doer
	max      int
	base     time.Duration
	maxDelay time.Duration

	// Seams for deterministic tests; production uses real time and jitter.
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
	now    func() time.Time
}

// NewRetryClient wraps doer with retry/backoff. A nil doer uses http.DefaultClient.
func NewRetryClient(doer Doer, cfg RetryConfig) *RetryClient {
	if doer == nil {
		doer = http.DefaultClient
	}
	maxRetries := cfg.MaxRetries
	switch {
	case maxRetries < 0:
		maxRetries = 0 // explicitly disabled: a single attempt, no retries
	case maxRetries == 0:
		maxRetries = defaultRetryMax
	}
	base := cfg.BaseDelay
	if base <= 0 {
		base = defaultRetryBase
	}
	maxDelay := cfg.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultRetryMaxDelay
	}
	if maxDelay < base {
		maxDelay = base
	}
	return &RetryClient{
		doer:     doer,
		max:      maxRetries,
		base:     base,
		maxDelay: maxDelay,
		sleep:    sleepCtx,
		jitter:   fullJitter,
		now:      time.Now,
	}
}

// Do performs req, retrying transient failures with backoff. It returns the final
// response (or transport error) once the outcome is non-retryable, the retry budget
// is spent, or the request body cannot be rewound for another attempt.
func (c *RetryClient) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	for attempt := 0; ; attempt++ {
		resp, err := c.doer.Do(req)
		if !shouldRetry(ctx, resp, err) || attempt >= c.max || !rewindable(req) {
			return resp, err
		}

		wait := c.backoff(attempt, resp)
		if resp != nil {
			// The response is about to be discarded; drain and close it so the
			// connection can be reused for the retry.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		if serr := c.sleep(ctx, wait); serr != nil {
			return nil, serr
		}
		if rerr := rewind(req); rerr != nil {
			return nil, fmt.Errorf("httpx: rewind request body for retry: %w", rerr)
		}
	}
}

// shouldRetry reports whether an outcome is worth retrying: a transport error or a
// throttling/5xx status. A canceled caller context is never retried. A 501 is
// treated as permanent (the server cannot do the request at all).
func shouldRetry(ctx context.Context, resp *http.Response, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode >= 500 && resp.StatusCode != http.StatusNotImplemented
}

// backoff computes the wait before the next attempt (0-based): BaseDelay doubled per
// attempt and capped at MaxDelay, then full-jittered to [0, that]. A Retry-After
// header on the response raises the wait to at least the server's request (also
// capped at MaxDelay), so the helper never hammers an upstream that asked to wait.
func (c *RetryClient) backoff(attempt int, resp *http.Response) time.Duration {
	ceiling := c.base
	for range attempt {
		ceiling *= 2
		if ceiling <= 0 || ceiling >= c.maxDelay {
			ceiling = c.maxDelay
			break
		}
	}
	wait := c.jitter(ceiling)
	if resp != nil {
		if floor := c.retryAfter(resp.Header); floor > wait {
			wait = floor
		}
	}
	return wait
}

// retryAfter reads a Retry-After header as either whole seconds or an HTTP date,
// returning the delay capped at MaxDelay, or zero when absent or unparseable.
func (c *RetryClient) retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return min(time.Duration(secs)*time.Second, c.maxDelay)
	}
	if t, err := http.ParseTime(v); err == nil {
		d := t.Sub(c.now())
		if d <= 0 {
			return 0
		}
		return min(d, c.maxDelay)
	}
	return 0
}

// rewindable reports whether req can be re-sent: it has no body, or a GetBody to
// produce a fresh one. A consumed, non-rewindable body means a retry would send an
// empty request, so the helper declines to retry instead.
func rewindable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// rewind resets req's body from GetBody for another attempt; it is a no-op for a
// body-less request.
func rewind(req *http.Request) error {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	if req.GetBody == nil {
		return errors.New("request body is not rewindable")
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

// fullJitter returns a random duration in [0, d], the AWS "full jitter" backoff so a
// fleet of clients retrying after the same upstream blip spread out.
func fullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// sleepCtx waits for d or until ctx is canceled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
