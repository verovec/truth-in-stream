package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedDoer returns a pre-programmed sequence of outcomes, one per call, so a
// retry sequence is exercised without a live server.
type scriptedDoer struct {
	outcomes []outcome
	calls    int
}

type outcome struct {
	status int // 0 means "return err instead of a response"
	err    error
	header http.Header
}

// recordingBody notes whether it was closed, so a test can prove a discarded
// response body is drained and closed before a retry.
type recordingBody struct {
	r      io.Reader
	closed bool
}

func (b *recordingBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *recordingBody) Close() error               { b.closed = true; return nil }

func (d *scriptedDoer) Do(_ *http.Request) (*http.Response, error) {
	o := d.outcomes[min(d.calls, len(d.outcomes)-1)]
	d.calls++
	if o.status == 0 {
		return nil, o.err
	}
	h := o.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: o.status, Header: h, Body: &recordingBody{r: strings.NewReader("body")}}, nil
}

// newTestClient builds a RetryClient with deterministic seams: the sleep records
// each wait instead of blocking, jitter is the identity (so backoff is the
// exponential ceiling), and now is fixed for Retry-After date parsing.
func newTestClient(doer Doer, cfg RetryConfig) (*RetryClient, *[]time.Duration) {
	c := NewRetryClient(doer, cfg)
	var mu sync.Mutex
	waits := &[]time.Duration{}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		mu.Lock()
		*waits = append(*waits, d)
		mu.Unlock()
		return nil
	}
	c.jitter = func(d time.Duration) time.Duration { return d }
	c.now = func() time.Time { return time.Unix(1_000_000, 0) }
	return c, waits
}

func mustReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.test/x", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

func TestRetryClientRetriesTransientOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		outcomes  []outcome
		wantCalls int
		wantCode  int
		wantErr   bool
	}{
		{name: "429 then 200", outcomes: []outcome{{status: 429}, {status: 200}}, wantCalls: 2, wantCode: 200},
		{name: "500 twice then 200", outcomes: []outcome{{status: 500}, {status: 500}, {status: 200}}, wantCalls: 3, wantCode: 200},
		{name: "503 then 200", outcomes: []outcome{{status: 503}, {status: 200}}, wantCalls: 2, wantCode: 200},
		{name: "transport error then 200", outcomes: []outcome{{err: errors.New("conn reset")}, {status: 200}}, wantCalls: 2, wantCode: 200},
		{name: "501 not retried", outcomes: []outcome{{status: 501}}, wantCalls: 1, wantCode: 501},
		{name: "400 not retried", outcomes: []outcome{{status: 400}}, wantCalls: 1, wantCode: 400},
		{name: "200 no retry", outcomes: []outcome{{status: 200}}, wantCalls: 1, wantCode: 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doer := &scriptedDoer{outcomes: tc.outcomes}
			c, _ := newTestClient(doer, RetryConfig{MaxRetries: 4})
			resp, err := c.Do(mustReq(t))
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if doer.calls != tc.wantCalls {
				t.Fatalf("doer called %d times, want %d", doer.calls, tc.wantCalls)
			}
			if resp != nil && resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}
		})
	}
}

func TestRetryClientExhaustsBudgetAndReturnsLastResponse(t *testing.T) {
	t.Parallel()
	// Always 500: with MaxRetries=2 the doer is called 3 times (1 + 2 retries) and
	// the final 500 is returned to the caller intact rather than swallowed.
	doer := &scriptedDoer{outcomes: []outcome{{status: 500}}}
	c, waits := newTestClient(doer, RetryConfig{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: time.Minute})
	resp, err := c.Do(mustReq(t))
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if resp.StatusCode != 500 {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if doer.calls != 3 {
		t.Fatalf("doer called %d times, want 3 (1 + 2 retries)", doer.calls)
	}
	// Exponential backoff (identity jitter): 1s then 2s.
	if len(*waits) != 2 || (*waits)[0] != time.Second || (*waits)[1] != 2*time.Second {
		t.Fatalf("waits = %v, want [1s 2s]", *waits)
	}
}

func TestRetryClientHonorsRetryAfterFloor(t *testing.T) {
	t.Parallel()
	// A Retry-After of 10s dwarfs the 1s exponential base, so the wait is raised to
	// the server's request.
	h := http.Header{"Retry-After": []string{"10"}}
	doer := &scriptedDoer{outcomes: []outcome{{status: 429, header: h}, {status: 200}}}
	c, waits := newTestClient(doer, RetryConfig{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: time.Minute})
	resp, err := c.Do(mustReq(t))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(*waits) != 1 || (*waits)[0] != 10*time.Second {
		t.Fatalf("waits = %v, want [10s] (Retry-After floor)", *waits)
	}
}

func TestRetryClientCapsRetryAfterAtMaxDelay(t *testing.T) {
	t.Parallel()
	h := http.Header{"Retry-After": []string{"3600"}} // 1h, far above the cap
	doer := &scriptedDoer{outcomes: []outcome{{status: 503, header: h}, {status: 200}}}
	c, waits := newTestClient(doer, RetryConfig{MaxRetries: 3, BaseDelay: time.Second, MaxDelay: 30 * time.Second})
	resp, err := c.Do(mustReq(t))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(*waits) != 1 || (*waits)[0] != 30*time.Second {
		t.Fatalf("waits = %v, want [30s] (Retry-After capped at MaxDelay)", *waits)
	}
}

func TestRetryClientStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	// The context is canceled before the call, so no retry happens even on a 500.
	doer := &scriptedDoer{outcomes: []outcome{{status: 500}}}
	c, _ := newTestClient(doer, RetryConfig{MaxRetries: 5})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.test/x", nil)
	resp, err := c.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Do() error = %v (a pre-canceled ctx returns the outcome, not an error, since the request itself ran)", err)
	}
	if doer.calls != 1 {
		t.Fatalf("doer called %d times, want 1 (no retry under a canceled context)", doer.calls)
	}
}

func TestRetryClientDrainsDiscardedBody(t *testing.T) {
	t.Parallel()
	doer := &scriptedDoer{outcomes: []outcome{{status: 500}, {status: 200}}}
	c, _ := newTestClient(doer, RetryConfig{MaxRetries: 2})
	resp, err := c.Do(mustReq(t))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	// The final (returned) body is not closed by the helper; the caller owns it.
	rb, ok := resp.Body.(*recordingBody)
	defer func() { _ = resp.Body.Close() }()
	if ok && rb.closed {
		t.Fatal("final response body was closed by the helper; the caller must own it")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	t.Parallel()
	c := NewRetryClient(nil, RetryConfig{BaseDelay: time.Second, MaxDelay: 8 * time.Second})
	c.jitter = func(d time.Duration) time.Duration { return d } // ceiling, no jitter
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for attempt, w := range want {
		if got := c.backoff(attempt, nil); got != w {
			t.Errorf("backoff(%d) = %v, want %v", attempt, got, w)
		}
	}
}
