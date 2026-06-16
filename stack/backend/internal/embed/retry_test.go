package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// fakeDoc is a document embedder that fails its first failTimes calls, then
// succeeds (echoing a one-dim marker). The failing calls return failErr when
// set, otherwise an APIError of the given status.
type fakeDoc struct {
	calls      int
	failTimes  int
	status     int
	failErr    error
	retryAfter time.Duration
}

func (f *fakeDoc) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.calls <= f.failTimes {
		if f.failErr != nil {
			return nil, f.failErr
		}
		return nil, &APIError{StatusCode: f.status, RetryAfter: f.retryAfter}
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
}

// timeoutErr is a net.Error reporting a timeout, mimicking the leaf of an
// http.Client.Timeout failure ("Client.Timeout exceeded while awaiting
// headers").
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "Client.Timeout exceeded while awaiting headers" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// voyageTimeout returns the error the Voyage client surfaces when an HTTP
// request times out: a *url.Error wrapping a timeout, wrapped again by the
// client's "do request" annotation.
func voyageTimeout() error {
	return fmt.Errorf("voyage: do request: %w", &url.Error{
		Op:  "Post",
		URL: "https://api.voyageai.com/v1/embeddings",
		Err: timeoutErr{},
	})
}

func testRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Minute}
}

func TestRetryRecoversFromRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeDoc{failTimes: 2, status: http.StatusTooManyRequests}
		rc := WithRetry(f, testRetryConfig())

		got, err := rc.EmbedDocuments(t.Context(), []string{"x"})
		if err != nil {
			t.Fatalf("EmbedDocuments: %v", err)
		}
		if f.calls != 3 {
			t.Errorf("calls = %d, want 3 (two 429s then success)", f.calls)
		}
		if len(got) != 1 {
			t.Errorf("got %d embeddings, want 1", len(got))
		}
	})
}

func TestRetryExhaustsAttemptsOnPersistentRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeDoc{failTimes: 100, status: http.StatusTooManyRequests}
		rc := WithRetry(f, testRetryConfig())

		_, err := rc.EmbedDocuments(t.Context(), []string{"x"})
		if err == nil {
			t.Fatal("want error after exhausting attempts, got nil")
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
			t.Errorf("error = %v, want wrapped 429 APIError", err)
		}
		if f.calls != 5 {
			t.Errorf("calls = %d, want 5 (MaxAttempts)", f.calls)
		}
	})
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The provider returns a 429 with a Retry-After far longer than the
		// exponential base delay would pick. The decorator must wait the
		// server-requested back-off, not its own guess, so the elapsed time equals
		// Retry-After exactly under the fake clock.
		const retryAfter = 25 * time.Second
		f := &fakeDoc{failTimes: 1, status: http.StatusTooManyRequests, retryAfter: retryAfter}
		rc := WithRetry(f, RetryConfig{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Minute})

		start := time.Now()
		if _, err := rc.EmbedDocuments(t.Context(), []string{"x"}); err != nil {
			t.Fatalf("EmbedDocuments: %v", err)
		}
		if elapsed := time.Since(start); elapsed != retryAfter {
			t.Errorf("waited %s, want exactly the Retry-After %s", elapsed, retryAfter)
		}
		if f.calls != 2 {
			t.Errorf("calls = %d, want 2 (one 429 then success)", f.calls)
		}
	})
}

func TestRetryCapsRetryAfterAtMaxDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A Retry-After beyond MaxDelay is clamped to MaxDelay so a hostile or
		// stuck header cannot stall the run past its ceiling.
		f := &fakeDoc{failTimes: 1, status: http.StatusTooManyRequests, retryAfter: time.Hour}
		rc := WithRetry(f, RetryConfig{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second})

		start := time.Now()
		if _, err := rc.EmbedDocuments(t.Context(), []string{"x"}); err != nil {
			t.Fatalf("EmbedDocuments: %v", err)
		}
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Errorf("waited %s, want MaxDelay 30s", elapsed)
		}
	})
}

func TestRetryDoesNotRetryNonRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeDoc{failTimes: 100, status: http.StatusBadRequest}
		rc := WithRetry(f, testRetryConfig())

		if _, err := rc.EmbedDocuments(t.Context(), []string{"x"}); err == nil {
			t.Fatal("want error, got nil")
		}
		if f.calls != 1 {
			t.Errorf("calls = %d, want 1 (4xx other than 429 is not retried)", f.calls)
		}
	})
}

func TestRetryDoesNotRetryArbitraryError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A plain error that is neither a 429 nor a net.Error timeout is not
		// retriable: it is returned on the first attempt.
		f := &fakeDoc{failTimes: 100, failErr: errors.New("network down")}
		rc := WithRetry(f, testRetryConfig())

		if _, err := rc.EmbedDocuments(t.Context(), []string{"x"}); err == nil {
			t.Fatal("want error, got nil")
		}
		if f.calls != 1 {
			t.Errorf("calls = %d, want 1 (an arbitrary error is not retried)", f.calls)
		}
	})
}

func TestRetryRecoversFromTransientTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeDoc{failTimes: 2, failErr: voyageTimeout()}
		rc := WithRetry(f, testRetryConfig())

		got, err := rc.EmbedDocuments(t.Context(), []string{"x"})
		if err != nil {
			t.Fatalf("EmbedDocuments: %v", err)
		}
		if f.calls != 3 {
			t.Errorf("calls = %d, want 3 (two timeouts then success)", f.calls)
		}
		if len(got) != 1 {
			t.Errorf("got %d embeddings, want 1", len(got))
		}
	})
}

func TestRetryExhaustsAttemptsOnPersistentTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeDoc{failTimes: 100, failErr: voyageTimeout()}
		rc := WithRetry(f, testRetryConfig())

		_, err := rc.EmbedDocuments(t.Context(), []string{"x"})
		if err == nil {
			t.Fatal("want error after exhausting attempts, got nil")
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Errorf("error = %v, want a wrapped net.Error timeout", err)
		}
		if f.calls != 5 {
			t.Errorf("calls = %d, want 5 (MaxAttempts)", f.calls)
		}
	})
}

func TestRetryStopsWhenParentContextExpired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A genuine -max-duration budget has expired the shared context. Even
		// though the inner error looks like a retriable timeout, the expired parent
		// outranks it: stop at once and surface the context error so the caller
		// treats the stop as clean and resumable rather than retrying into a dead
		// context.
		f := &fakeDoc{failTimes: 100, failErr: voyageTimeout()}
		rc := WithRetry(f, testRetryConfig())

		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := rc.EmbedDocuments(ctx, []string{"x"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want context.DeadlineExceeded", err)
		}
		if f.calls != 1 {
			t.Errorf("calls = %d, want 1 (expired parent context is not retried)", f.calls)
		}
	})
}

// TestRetryRetriesRealClientTimeout drives a real *Client whose http.Client
// timeout is shorter than the server's response, reproducing the
// "Client.Timeout exceeded while awaiting headers" failure the bulk embed hit.
// It guards the load-bearing assumption of the fix: the genuine stdlib timeout
// error - not just a hand-built net.Error - is classified as retriable and the
// request is attempted again rather than aborting the run on the first stall.
func TestRetryRetriesRealClientTimeout(t *testing.T) {
	t.Parallel()
	var hits int32
	// The handler records the hit, then blocks past the client timeout until the
	// test releases it - no fixed sleep to race the timeout, so the only timing
	// assumption is that localhost setup fits inside the generous 100ms ceiling.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release
	}))
	// Cleanups run LIFO: unblock the handlers first, then Close waits them out.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	client := New(Config{
		APIKey:     "k",
		Model:      "voyage-4",
		Dim:        1,
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Timeout: 100 * time.Millisecond},
	})
	rc := WithRetry(client, RetryConfig{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})

	_, err := rc.EmbedDocuments(t.Context(), []string{"x"})
	if err == nil {
		t.Fatal("want a timeout error, got nil")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("error = %v, want a net.Error timeout", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want 2 (the real timeout was retried)", got)
	}
}

func TestRetryClampsZeroDelaysInsteadOfSpinning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A misconfigured zero BaseDelay/MaxDelay must not collapse backoff into a
		// busy loop; WithRetry clamps it. Persistent 429 should still stop at
		// MaxAttempts after real (clamped) waits, not spin instantly.
		f := &fakeDoc{failTimes: 100, status: http.StatusTooManyRequests}
		rc := WithRetry(f, RetryConfig{MaxAttempts: 3})

		_, err := rc.EmbedDocuments(t.Context(), []string{"x"})
		if err == nil {
			t.Fatal("want error after exhausting attempts, got nil")
		}
		if f.calls != 3 {
			t.Errorf("calls = %d, want 3 (MaxAttempts), spinning would not change this but backoff must be non-zero", f.calls)
		}
		if rc.cfg.BaseDelay <= 0 || rc.cfg.MaxDelay < rc.cfg.BaseDelay {
			t.Errorf("delays not clamped: base=%v max=%v", rc.cfg.BaseDelay, rc.cfg.MaxDelay)
		}
	})
}

func TestRetryLogsBackoffBeforeRetrying(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Two 429s then success: each backoff must surface a WARN line so a
		// throttled run is visible instead of silently waiting.
		f := &fakeDoc{failTimes: 2, status: http.StatusTooManyRequests}
		var buf bytes.Buffer
		cfg := testRetryConfig()
		cfg.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		rc := WithRetry(f, cfg)

		if _, err := rc.EmbedDocuments(t.Context(), []string{"x"}); err != nil {
			t.Fatalf("EmbedDocuments: %v", err)
		}

		var backoffs int
		for line := range bytes.Lines(buf.Bytes()) {
			var rec struct {
				Level   string `json:"level"`
				Msg     string `json:"msg"`
				Reason  string `json:"reason"`
				Elapsed *int64 `json:"elapsed"`
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatalf("log line is not JSON: %q: %v", line, err)
			}
			if rec.Msg != "embedding request failed, backing off before retry" {
				continue
			}
			backoffs++
			if rec.Level != "WARN" {
				t.Errorf("backoff log level = %q, want WARN", rec.Level)
			}
			if rec.Reason != "rate_limited" {
				t.Errorf("backoff reason = %q, want rate_limited", rec.Reason)
			}
			if rec.Elapsed == nil {
				t.Error("backoff line missing elapsed; the failed request's duration must be logged")
			}
		}
		// Three attempts, two of them retried, so two backoff lines.
		if backoffs != 2 {
			t.Errorf("backoff log lines = %d, want 2", backoffs)
		}
	})
}

func TestRetryHonorsContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeDoc{failTimes: 100, status: http.StatusTooManyRequests}
		rc := WithRetry(f, testRetryConfig())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := rc.EmbedDocuments(ctx, []string{"x"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})
}
