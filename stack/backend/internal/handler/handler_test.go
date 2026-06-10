package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(_ context.Context) error { return f.err }

func newTestServer(storeErr error, transcriber *stubTranscriber) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hc := service.NewHealthChecker(fakePinger{err: storeErr})
	return NewMux(hc, transcriber, &fakeProcessing{}, "", logger)
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantCode int
	}{
		{name: "healthy", storeErr: nil, wantCode: http.StatusOK},
		{name: "store down", storeErr: errors.New("down"), wantCode: http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.storeErr, &stubTranscriber{})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("GET /healthz = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := newTestServer(nil, &stubTranscriber{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", rec.Code)
	}
}
