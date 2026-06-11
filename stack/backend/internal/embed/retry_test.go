package embed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"
	"time"
)

// fakeDoc is a document embedder that fails its first failTimes calls, then
// succeeds (echoing a one-dim marker). The failing calls return failErr when
// set, otherwise an APIError of the given status.
type fakeDoc struct {
	calls     int
	failTimes int
	status    int
	failErr   error
}

func (f *fakeDoc) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.calls <= f.failTimes {
		if f.failErr != nil {
			return nil, f.failErr
		}
		return nil, &APIError{StatusCode: f.status}
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
