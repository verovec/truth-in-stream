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

func newTestServer(storeErr error) http.Handler {
	return newAuthedTestServer(globalTestAuth, storeErr)
}

func newAuthedTestServer(auth AuthConfig, storeErr error) http.Handler {
	health := service.NewHealthChecker(fakePinger{err: storeErr})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewMux(health, &fakeVideoService{}, &fakeYouTubeService{}, stubLiveAnalyzer{}, nil, false, nil, "", auth, logger)
}

// newDebugTestServer builds a router with the debug wiki-search route registered
// (a non-nil searcher), so the admin gate on debug endpoints can be exercised end
// to end through NewMux.
func newDebugTestServer() http.Handler {
	health := service.NewHealthChecker(fakePinger{})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewMux(health, &fakeVideoService{}, &fakeYouTubeService{}, stubLiveAnalyzer{}, nil, false, &fakeWikiSearcher{}, "", globalTestAuth, logger)
}

// TestDebugEndpointRequiresAdmin proves the admin gate on a debug endpoint: a
// valid admin claim passes the gate (the WebSocket handler then rejects a plain
// GET with a non-403 status), while a guest, an anonymous caller, and an invalid
// token are all stopped before the handler with 403 or 401. Debug behavior is
// reachable only with a verified admin claim, never a client-supplied flag.
func TestDebugEndpointRequiresAdmin(t *testing.T) {
	srv := newDebugTestServer()
	tests := []struct {
		name        string
		bearer      string
		wantBlocked bool
		wantCode    int
	}{
		{name: "admin passes the admin gate", bearer: testAdminToken, wantBlocked: false},
		{name: "guest is forbidden", bearer: testGuestToken, wantBlocked: true, wantCode: http.StatusForbidden},
		{name: "anonymous is forbidden", bearer: "", wantBlocked: true, wantCode: http.StatusForbidden},
		{name: "invalid token is unauthorized", bearer: "bogus", wantBlocked: true, wantCode: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/debug/wiki-search", nil)
			req.AddCookie(authCookie(t))
			if tc.bearer != "" {
				bearer(req, tc.bearer)
			}
			srv.ServeHTTP(rec, req)
			if tc.wantBlocked {
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
				}
				return
			}
			// Admin cleared the gate; the WebSocket handler rejects a plain GET,
			// but never with the gate's 403/401.
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Fatalf("admin was blocked by the gate: status %d", rec.Code)
			}
		})
	}
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
			srv := newTestServer(tc.storeErr)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("GET /healthz = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", rec.Code)
	}
}
