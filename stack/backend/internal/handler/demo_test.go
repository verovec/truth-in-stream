package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

func newDemoServer(dir string) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hc := service.NewHealthChecker(fakePinger{})
	return NewMux(hc, &stubTranscriber{}, &fakeProcessing{}, dir, logger)
}

func TestDemoMediaServesBundledFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "great-myths.m4a"), []byte("MEDIA-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newDemoServer(dir)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/great-myths.m4a", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /demo/great-myths.m4a = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "MEDIA-BYTES" {
		t.Errorf("body = %q, want the media bytes", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") == "" {
		t.Error("expected a Content-Type header")
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("expected range support for media seeking")
	}
}

func TestDemoMediaUnknownFileIs404(t *testing.T) {
	srv := newDemoServer(t.TempDir())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/absent.m4a", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /demo/absent.m4a = %d, want 404", rec.Code)
	}
}

func TestDemoMediaRejectsDisallowedExtension(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newDemoServer(dir)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/secret.env", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /demo/secret.env = %d, want 404 (extension not allowed)", rec.Code)
	}
	if rec.Body.String() == "SECRET=1" {
		t.Error("must never serve a disallowed file's contents")
	}
}
