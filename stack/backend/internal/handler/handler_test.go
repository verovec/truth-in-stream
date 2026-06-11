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
	return newAuthedTestServer(globalTestAuth, storeErr, transcriber, &fakeProcessing{})
}

func newAuthedTestServer(auth AuthConfig, storeErr error, transcriber *stubTranscriber, processing ProcessingService) http.Handler {
	health := service.NewHealthChecker(fakePinger{err: storeErr})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewMux(health, transcriber, processing, &fakeVideoService{}, &fakeYouTubeService{}, "", auth, logger)
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
