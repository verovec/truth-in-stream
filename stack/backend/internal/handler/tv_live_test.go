package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// tvHubBehaviorServer mounts the feed and viewer sockets over a real hub driven
// by analyzer, behind the permissive Identity middleware, so a dial with an admin
// Bearer reaches the admin-gated feed and a guest Bearer reaches the viewer. The
// production access_token / RequireIdentity gate is exercised separately through
// NewMux by TestTVHubSocketsRoleGating.
func tvHubBehaviorServer(t *testing.T, analyzer service.LiveRunner) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hub, err := service.NewTVHub(analyzer, logger)
	if err != nil {
		t.Fatalf("NewTVHub: %v", err)
	}
	mux := http.NewServeMux()
	id := middleware.Identity(stubVerifier{})
	mux.Handle("GET /api/tv/channels/{id}/feed", id(middleware.RequireAdmin(tvFeedHandler(hub, nil, logger))))
	mux.Handle("GET /api/tv/channels/{id}/live", id(tvViewerHandler(hub, nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func guestDialOptions() *websocket.DialOptions {
	return &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + testGuestToken}}}
}

// dialAuthedWS dials a WebSocket, closing the handshake response body, and fails the
// test if the dial is rejected.
func dialAuthedWS(ctx context.Context, t *testing.T, url string, opts *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.Dial(ctx, url, opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return conn
}

// TestTVHubFeedToViewer drives the whole path over real sockets: a publisher
// streams PCM into the feed socket, and a viewer receives the resulting subtitle
// event and then an off_air frame when the feed disconnects.
func TestTVHubFeedToViewer(t *testing.T) {
	t.Parallel()
	analyzer := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "s1", Segment: domain.Segment{Text: "bonjour"}},
	}}
	base := tvHubBehaviorServer(t, analyzer)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	viewer := dialAuthedWS(ctx, t, base+"/api/tv/channels/chan1/live", guestDialOptions())
	defer func() { _ = viewer.CloseNow() }()

	feed := dialAuthedWS(ctx, t, base+"/api/tv/channels/chan1/feed", adminDialOptions())
	defer func() { _ = feed.CloseNow() }()

	// One 100 ms PCM frame (16 kHz mono s16le = 3200 bytes) triggers the analyzer.
	if err := feed.Write(ctx, websocket.MessageBinary, make([]byte, 3200)); err != nil {
		t.Fatalf("feed write: %v", err)
	}

	frame := readFrame(ctx, t, viewer)
	if frame.Type != "subtitle" || frame.Text != "bonjour" {
		t.Fatalf("viewer frame = %+v, want subtitle bonjour", frame)
	}

	// The publisher disconnecting takes the viewer off air.
	_ = feed.Close(websocket.StatusNormalClosure, "")
	off := readFrame(ctx, t, viewer)
	if off.Type != offAirType {
		t.Fatalf("viewer frame = %+v, want off_air", off)
	}
}

// TestTVHubRejectsSecondFeed proves single-publisher enforcement over the wire: a
// second feed for a channel that already has one is closed with a policy
// violation.
func TestTVHubRejectsSecondFeed(t *testing.T) {
	t.Parallel()
	analyzer := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "s1", Segment: domain.Segment{Text: "en direct"}},
	}}
	base := tvHubBehaviorServer(t, analyzer)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	viewer := dialAuthedWS(ctx, t, base+"/api/tv/channels/chan1/live", guestDialOptions())
	defer func() { _ = viewer.CloseNow() }()

	feed1 := dialAuthedWS(ctx, t, base+"/api/tv/channels/chan1/feed", adminDialOptions())
	defer func() { _ = feed1.CloseNow() }()
	// Confirm feed1 owns the channel before the second feed connects.
	if err := feed1.Write(ctx, websocket.MessageBinary, make([]byte, 3200)); err != nil {
		t.Fatalf("feed1 write: %v", err)
	}
	if frame := readFrame(ctx, t, viewer); frame.Type != "subtitle" {
		t.Fatalf("viewer frame = %+v, want subtitle", frame)
	}

	feed2 := dialAuthedWS(ctx, t, base+"/api/tv/channels/chan1/feed", adminDialOptions())
	defer func() { _ = feed2.CloseNow() }()
	if _, _, err := feed2.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("second feed close = %v, want policy violation", err)
	}
}

// tvHubMuxServer wires the TV sockets through the real NewMux (behind
// RequireIdentity), so the access_token query-parameter auth path can be
// exercised end to end.
func tvHubMuxServer(t *testing.T) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hc := service.NewHealthChecker(fakePinger{})
	mux := NewMux(hc, &fakeVideoService{}, &fakeVideoAnalysisService{}, &fakeDocumentService{}, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, &fakeTVChannelService{}, &fakeTVRecordingService{}, testTVHub(), stubLiveAnalyzer{}, nil, nil, nil, false, nil, "", globalTestAuth, logger)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestTVHubSocketsRoleGating proves the auth contract through NewMux: the feed is
// admin-only (a guest or anonymous caller is rejected before the handler), while
// the viewer serves any authenticated user (a guest connects, anonymous is
// rejected). The token rides the access_token query parameter, the only path a
// browser WebSocket has.
func TestTVHubSocketsRoleGating(t *testing.T) {
	t.Parallel()
	base := tvHubMuxServer(t)

	tests := []struct {
		name     string
		path     string
		token    string
		wantOK   bool
		wantCode int
	}{
		{name: "admin opens feed", path: "/api/tv/channels/c1/feed", token: testAdminToken, wantOK: true},
		{name: "guest denied feed", path: "/api/tv/channels/c1/feed", token: testGuestToken, wantOK: false, wantCode: http.StatusForbidden},
		{name: "anonymous denied feed", path: "/api/tv/channels/c1/feed", token: "", wantOK: false, wantCode: http.StatusUnauthorized},
		{name: "guest opens viewer", path: "/api/tv/channels/c1/live", token: testGuestToken, wantOK: true},
		{name: "anonymous denied viewer", path: "/api/tv/channels/c1/live", token: "", wantOK: false, wantCode: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			url := base + tc.path
			if tc.token != "" {
				url += "?access_token=" + tc.token
			}
			conn, resp, err := websocket.Dial(ctx, url, nil)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if tc.wantOK {
				if err != nil {
					t.Fatalf("dial should succeed, got %v", err)
				}
				_ = conn.CloseNow()
				return
			}
			if err == nil {
				_ = conn.CloseNow()
				t.Fatal("dial should have been rejected")
			}
			if resp == nil || resp.StatusCode != tc.wantCode {
				got := 0
				if resp != nil {
					got = resp.StatusCode
				}
				t.Fatalf("reject status = %d, want %d", got, tc.wantCode)
			}
		})
	}
}
