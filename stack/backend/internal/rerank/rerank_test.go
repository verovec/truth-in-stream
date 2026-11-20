package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc, retries int) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{APIKey: "test-key", Model: "rerank-2.5", BaseURL: server.URL, MaxRetries: retries})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func respond(t *testing.T, w http.ResponseWriter, data []map[string]any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestRerankOrdersByRelevance(t *testing.T) {
	t.Parallel()
	var gotBody rerankRequest
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want bearer test key", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		respond(t, w, []map[string]any{
			{"index": 2, "relevance_score": 0.9},
			{"index": 0, "relevance_score": 0.5},
			{"index": 1, "relevance_score": 0.1},
		})
	}, 1)

	order, err := c.Rank(context.Background(), "le chomage a baisse", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Rank: %v", err)
	}
	if want := []int{2, 0, 1}; !equalInts(order, want) {
		t.Errorf("Rank order = %v, want %v", order, want)
	}
	if gotBody.Model != "rerank-2.5" || !gotBody.Truncation || gotBody.ReturnDocuments {
		t.Errorf("request carried model=%q truncation=%v return_documents=%v; want rerank-2.5, true, false",
			gotBody.Model, gotBody.Truncation, gotBody.ReturnDocuments)
	}
}

func TestRerankRetriesRateLimitThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		respond(t, w, []map[string]any{
			{"index": 1, "relevance_score": 0.8},
			{"index": 0, "relevance_score": 0.2},
		})
	}, 1)

	order, err := c.Rank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Rank after 429: %v", err)
	}
	if want := []int{1, 0}; !equalInts(order, want) {
		t.Errorf("Rank order = %v, want %v", order, want)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("api calls = %d, want 2 (one retry)", got)
	}
}

func TestRerankClientErrorIsTerminal(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}, 3)

	if _, err := c.Rank(context.Background(), "q", []string{"a"}); err == nil {
		t.Fatal("Rank on 400 returned nil error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("api calls = %d, want 1 (4xx must not retry)", got)
	}
}

func TestRerankHonorsContextDeadline(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		respond(t, w, []map[string]any{{"index": 0, "relevance_score": 1.0}})
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Rank(ctx, "q", []string{"a"}); err == nil {
		t.Fatal("Rank past deadline returned nil error")
	}
}

func TestRerankRejectsMalformedResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []map[string]any
	}{
		{"missing result", []map[string]any{{"index": 0, "relevance_score": 0.5}}},
		{"out of range index", []map[string]any{
			{"index": 0, "relevance_score": 0.5}, {"index": 7, "relevance_score": 0.4},
		}},
		{"duplicate index", []map[string]any{
			{"index": 0, "relevance_score": 0.5}, {"index": 0, "relevance_score": 0.4},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				respond(t, w, tc.data)
			}, 0)
			if _, err := c.Rank(context.Background(), "q", []string{"a", "b"}); err == nil {
				t.Fatal("Rank on malformed results returned nil error")
			}
		})
	}
}

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Model: "rerank-2.5"}); err == nil {
		t.Error("New without api key returned nil error")
	}
	if _, err := New(Config{APIKey: "k"}); err == nil {
		t.Error("New without model returned nil error")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
