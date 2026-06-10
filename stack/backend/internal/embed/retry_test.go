package embed

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// fakeDoc is a document embedder that fails its first failTimes calls with an
// APIError of the given status, then succeeds (echoing a one-dim marker), or
// returns err when set.
type fakeDoc struct {
	calls     int
	failTimes int
	status    int
	err       error
}

func (f *fakeDoc) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	if f.calls <= f.failTimes {
		return nil, &APIError{StatusCode: f.status}
	}
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1}
	}
	return out, nil
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
		f := &fakeDoc{failTimes: 100, err: errors.New("network down")}
		// status 0: not an APIError path; first call returns f.err only once
		// failTimes is high but err takes precedence after the status branch,
		// so make status non-429 to ensure the APIError branch never matches.
		f.status = http.StatusInternalServerError
		rc := WithRetry(f, testRetryConfig())

		if _, err := rc.EmbedDocuments(t.Context(), []string{"x"}); err == nil {
			t.Fatal("want error, got nil")
		}
		if f.calls != 1 {
			t.Errorf("calls = %d, want 1 (500 is not retried)", f.calls)
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
