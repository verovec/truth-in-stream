package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// fakeWikiSearcher returns a fixed result (or error) and records the queries it
// was asked, so a test can assert what the handler forwarded.
type fakeWikiSearcher struct {
	hits []service.WikiHit
	err  error

	gotQueries chan string
}

func (f *fakeWikiSearcher) Search(_ context.Context, query string) ([]service.WikiHit, error) {
	if f.gotQueries != nil {
		f.gotQueries <- query
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func debugSearchTestServer(t *testing.T, searcher WikiSearcher, origins []string) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/debug/wiki-search", debugSearchHandler(searcher, origins, logger))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/debug/wiki-search"
}

// debugResultsWire decodes a results frame for assertions.
type debugResultsWire struct {
	Type string `json:"type"`
	Seq  int    `json:"seq"`
	Hits []struct {
		Title      string  `json:"title"`
		URL        string  `json:"url"`
		Snippet    string  `json:"snippet"`
		Similarity float64 `json:"similarity"`
	} `json:"hits"`
	Error string `json:"error"`
}

func dialDebug(t *testing.T, wsURL string, header http.Header) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{HTTPHeader: header}
	conn, resp, err := websocket.Dial(ctx, wsURL, opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func readDebugResults(ctx context.Context, t *testing.T, conn *websocket.Conn) debugResultsWire {
	t.Helper()
	var frame debugResultsWire
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatalf("read results: %v", err)
	}
	return frame
}

func TestDebugSearchHandlerReturnsHitsEchoingSeq(t *testing.T) {
	t.Parallel()
	searcher := &fakeWikiSearcher{hits: []service.WikiHit{
		{Title: "Red fox", URL: "https://en.wikipedia.org/wiki/Red_fox", Content: "foxes are fast", Similarity: 0.91},
	}}
	wsURL := debugSearchTestServer(t, searcher, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn := dialDebug(t, wsURL, nil)
	defer func() { _ = conn.CloseNow() }()

	if err := wsjson.Write(ctx, conn, debugQueryFrame{Q: "foxes", Seq: 7}); err != nil {
		t.Fatalf("write query: %v", err)
	}

	got := readDebugResults(ctx, t, conn)
	if got.Type != "results" || got.Seq != 7 {
		t.Errorf("frame type/seq = %q/%d, want results/7", got.Type, got.Seq)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want one", got.Hits)
	}
	if got.Hits[0].Title != "Red fox" || got.Hits[0].URL != "https://en.wikipedia.org/wiki/Red_fox" {
		t.Errorf("hit attribution = %+v", got.Hits[0])
	}
	if got.Hits[0].Snippet != "foxes are fast" || got.Hits[0].Similarity != 0.91 {
		t.Errorf("hit body = %+v", got.Hits[0])
	}
	if got.Error != "" {
		t.Errorf("unexpected error field %q", got.Error)
	}
}

func TestDebugSearchHandlerTruncatesLongSnippet(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", debugMaxSnippetRunes+50)
	searcher := &fakeWikiSearcher{hits: []service.WikiHit{
		{Title: "Long", URL: "https://example.test", Content: long, Similarity: 0.5},
	}}
	wsURL := debugSearchTestServer(t, searcher, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn := dialDebug(t, wsURL, nil)
	defer func() { _ = conn.CloseNow() }()

	if err := wsjson.Write(ctx, conn, debugQueryFrame{Q: "a", Seq: 1}); err != nil {
		t.Fatalf("write query: %v", err)
	}
	got := readDebugResults(ctx, t, conn)
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want one", got.Hits)
	}
	snip := []rune(got.Hits[0].Snippet)
	// debugMaxSnippetRunes content runes plus the appended ellipsis.
	if len(snip) != debugMaxSnippetRunes+1 || !strings.HasSuffix(got.Hits[0].Snippet, "…") {
		t.Errorf("snippet len = %d, want %d with trailing ellipsis", len(snip), debugMaxSnippetRunes+1)
	}
}

func TestDebugSearchHandlerReportsSearchErrorInBand(t *testing.T) {
	t.Parallel()
	// A search failure must not drop the socket: the client gets an empty-hit
	// error frame and the session stays open for the next query.
	searcher := &fakeWikiSearcher{err: errors.New("corpus offline")}
	wsURL := debugSearchTestServer(t, searcher, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn := dialDebug(t, wsURL, nil)
	defer func() { _ = conn.CloseNow() }()

	if err := wsjson.Write(ctx, conn, debugQueryFrame{Q: "foxes", Seq: 3}); err != nil {
		t.Fatalf("write query: %v", err)
	}
	got := readDebugResults(ctx, t, conn)
	if got.Seq != 3 {
		t.Errorf("seq = %d, want 3", got.Seq)
	}
	if got.Error == "" {
		t.Error("expected an error field on a failed search")
	}
	if len(got.Hits) != 0 {
		t.Errorf("failed search hits = %+v, want empty", got.Hits)
	}
	// The generic message must not leak the underlying cause.
	if strings.Contains(got.Error, "corpus offline") {
		t.Errorf("error field leaked internals: %q", got.Error)
	}
}

func TestDebugSearchHandlerRejectsDisallowedOrigin(t *testing.T) {
	t.Parallel()
	wsURL := debugSearchTestServer(t, &fakeWikiSearcher{}, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://evil.example"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("expected cross-origin handshake to be rejected")
	}
}

func TestDebugSearchHandlerAcceptsAllowlistedCrossOrigin(t *testing.T) {
	t.Parallel()
	// Behind the dev frontend proxy the browser Origin differs from the backend
	// Host, so the upgrade is cross-origin; an allow-listed origin must connect,
	// mirroring the live socket's CORS_ALLOWED_ORIGIN handshake.
	wsURL := debugSearchTestServer(t, &fakeWikiSearcher{}, []string{"localhost:3000"})

	conn := dialDebug(t, wsURL, http.Header{"Origin": {"http://localhost:3000"}})
	_ = conn.CloseNow()
}

func TestDebugSearchRouteAbsentWhenDisabled(t *testing.T) {
	t.Parallel()
	// With no searcher the route is never registered, so an authenticated upgrade
	// attempt is a 404 rather than a WebSocket handshake.
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/debug/wiki-search", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET debug search when disabled = %d, want 404", rec.Code)
	}
}
