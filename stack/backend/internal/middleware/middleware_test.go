package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestIDSetsHeader(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id header to be set")
	}
}

func TestRecoverTurnsPanicInto500(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := Recover(logger)(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)) // must not panic

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
}

func TestLoggingWriterSupportsDeadlines(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var readErr, writeErr error
	h := Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		deadline := time.Now().Add(time.Minute)
		readErr = rc.SetReadDeadline(deadline)
		writeErr = rc.SetWriteDeadline(deadline)
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()

	if readErr != nil {
		t.Errorf("SetReadDeadline through Logging wrapper: %v", readErr)
	}
	if writeErr != nil {
		t.Errorf("SetWriteDeadline through Logging wrapper: %v", writeErr)
	}
}
