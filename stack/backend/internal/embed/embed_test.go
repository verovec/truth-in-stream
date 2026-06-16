package embed

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const testDim = 4

// embedServer returns an httptest server that records the last request body and
// header, and replies with the embeddings produced by reply(req).
func embedServer(t *testing.T, status int, reply func(req embedRequest) any) (*httptest.Server, *embedRequest, *string) {
	t.Helper()
	var gotReq embedRequest
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := reply(gotReq)
		if s, ok := body.(string); ok {
			_, _ = w.Write([]byte(s))
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &gotAuth
}

// okReply echoes one deterministic embedding per input, in REVERSE index order,
// to prove the client reorders by the index field.
func okReply(req embedRequest) any {
	data := make([]embedData, len(req.Input))
	for i := range req.Input {
		idx := len(req.Input) - 1 - i
		vec := make([]float32, testDim)
		vec[0] = float32(idx)
		data[i] = embedData{Index: idx, Embedding: vec}
	}
	return embedResponse{Data: data}
}

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(Config{
		APIKey:     "test-key",
		Model:      "voyage-4",
		Dim:        testDim,
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
}

func TestEmbedDocumentsSendsRequestAndOrdersByIndex(t *testing.T) {
	t.Parallel()
	srv, gotReq, gotAuth := embedServer(t, http.StatusOK, okReply)
	client := newTestClient(t, srv)

	got, err := client.EmbedDocuments(t.Context(), []string{"first", "second", "third"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}

	if *gotAuth != "Bearer test-key" {
		t.Errorf("auth header = %q, want %q", *gotAuth, "Bearer test-key")
	}
	if gotReq.Model != "voyage-4" || gotReq.InputType != "document" || gotReq.OutputDimension != testDim || gotReq.OutputDtype != "float" {
		t.Errorf("request = %+v, want voyage-4/document/%d/float", *gotReq, testDim)
	}
	if diff := cmp.Diff([]string{"first", "second", "third"}, gotReq.Input); diff != "" {
		t.Errorf("input mismatch (-want +got):\n%s", diff)
	}
	// Reordered back to input order: out[i][0] == i.
	for i, vec := range got {
		if vec[0] != float32(i) {
			t.Errorf("embedding %d not in input order: got marker %v", i, vec[0])
		}
	}
}

func TestEmbedQueriesSetsQueryInputType(t *testing.T) {
	t.Parallel()
	srv, gotReq, _ := embedServer(t, http.StatusOK, okReply)
	client := newTestClient(t, srv)

	if _, err := client.EmbedQueries(t.Context(), []string{"q"}); err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if gotReq.InputType != "query" {
		t.Errorf("input_type = %q, want query", gotReq.InputType)
	}
}

func TestEmbedEmptyMakesNoCall(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server should not be called for empty input")
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, srv)

	got, err := client.EmbedDocuments(t.Context(), nil)
	if err != nil {
		t.Fatalf("EmbedDocuments(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d embeddings, want 0", len(got))
	}
}

func TestEmbedAPIErrorIsClassified(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t, http.StatusTooManyRequests, func(embedRequest) any {
		return `{"detail":"rate limited"}`
	})
	client := newTestClient(t, srv)

	_, err := client.EmbedDocuments(t.Context(), []string{"x"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", apiErr.StatusCode)
	}
}

func TestEmbedRejectsWrongDimension(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t, http.StatusOK, func(_ embedRequest) any {
		return embedResponse{Data: []embedData{{Index: 0, Embedding: []float32{1, 2}}}}
	})
	client := newTestClient(t, srv)

	if _, err := client.EmbedDocuments(t.Context(), []string{"x"}); err == nil {
		t.Fatal("want dimension error, got nil")
	}
}

func TestEmbedRejectsCountMismatch(t *testing.T) {
	t.Parallel()
	srv, _, _ := embedServer(t, http.StatusOK, func(_ embedRequest) any {
		return embedResponse{Data: []embedData{{Index: 0, Embedding: make([]float32, testDim)}}}
	})
	client := newTestClient(t, srv)

	if _, err := client.EmbedDocuments(t.Context(), []string{"a", "b"}); err == nil {
		t.Fatal("want count-mismatch error, got nil")
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "30", 30 * time.Second},
		{"seconds with spaces", "  5 ", 5 * time.Second},
		{"zero seconds", "0", 0},
		{"negative seconds", "-3", 0},
		{"garbage", "soon", 0},
		{"future http date", now.Add(45 * time.Second).UTC().Format(http.TimeFormat), 45 * time.Second},
		{"past http date", now.Add(-time.Minute).UTC().Format(http.TimeFormat), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRetryAfter(tc.header, now); got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.header, got, tc.want)
			}
		})
	}
}

func TestEmbedDocumentsPopulatesRetryAfterOn429(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	t.Cleanup(srv.Close)
	client := newTestClient(t, srv)

	_, err := client.EmbedDocuments(t.Context(), []string{"x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", apiErr.StatusCode)
	}
	if apiErr.RetryAfter != 12*time.Second {
		t.Errorf("RetryAfter = %s, want 12s", apiErr.RetryAfter)
	}
}
