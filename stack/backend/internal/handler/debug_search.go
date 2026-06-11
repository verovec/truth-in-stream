package handler

// Developer wiki-search probe (dev only).
//
//	GET /api/debug/wiki-search   (WebSocket) search the embedded Wikipedia corpus
//
// A developer types a query in the web UI; the client sends it as a JSON text
// frame and the server embeds it, runs the same nearest-neighbor search the
// fact-check matcher uses over the evidence corpus, and pushes back the raw hits.
// It mirrors the live socket's accept, read-limit, write-timeout, and keepalive
// conventions (see live.go) so the two transports stay consistent. The route is
// registered only when the debug flag is on, so it does not exist in production.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// WikiSearcher is the slice of the developer search probe the handler drives:
// a query string in, ranked corpus hits out. Satisfied by *service.WikiSearch.
// The handler owns the socket; this port carries no transport types.
type WikiSearcher interface {
	Search(ctx context.Context, query string) ([]service.WikiHit, error)
}

// debugReadLimit bounds one inbound query frame. Queries are short text, so
// 64 KiB is generous while rejecting a runaway frame.
const debugReadLimit = 1 << 16

// debugQueryBuffer bounds how many query frames may queue between the socket
// reader and the search loop. The reader stays in conn.Read (where the library
// services pong frames) while a search runs, so the keepalive ping is not
// starved by a slow embed call.
const debugQueryBuffer = 8

// debugMaxSnippetRunes bounds the article excerpt carried in a hit so a long
// chunk does not bloat the result frame. It is a presentation cap on a debug
// probe, not corpus truncation.
const debugMaxSnippetRunes = 320

// debugQueryFrame is the wire form of a search request: the query text and a
// client-assigned sequence number echoed back on the matching results frame so
// the client can discard a response superseded by a later keystroke.
type debugQueryFrame struct {
	Q   string `json:"q"`
	Seq int    `json:"seq"`
}

// debugHit is the wire form of one corpus neighbor: the article attribution, a
// bounded excerpt, and the cosine similarity in [-1, 1].
type debugHit struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Snippet    string  `json:"snippet"`
	Similarity float64 `json:"similarity"`
}

// debugResultsFrame is the wire form of a search response. Seq echoes the
// request's sequence number; Hits is empty (never null) when nothing matched or
// the search failed; Error is a generic, non-leaking message set only on
// failure so the bar can show that a query errored without exposing internals.
type debugResultsFrame struct {
	Type  string     `json:"type"`
	Seq   int        `json:"seq"`
	Hits  []debugHit `json:"hits"`
	Error string     `json:"error,omitempty"`
}

// debugSearchHandler upgrades the request to a WebSocket and bridges it to the
// wiki-search probe: a reader pumps inbound query frames to the search loop,
// which embeds each query, searches the corpus, and writes the hits back.
// allowedOrigins are the browser origins permitted to connect cross-origin;
// empty enforces same-origin.
func debugSearchHandler(searcher WikiSearcher, allowedOrigins []string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
		if err != nil {
			// Accept has already written the handshake failure response.
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(debugReadLimit)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		queries := make(chan debugQueryFrame, debugQueryBuffer)
		go readQueries(ctx, cancel, conn, queries)
		go pingLoop(ctx, cancel, conn, livePingInterval, livePingTimeout)

		runSearchLoop(ctx, cancel, conn, searcher, queries, logger)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

// readQueries forwards inbound text frames to the search loop until the client
// closes, a read fails, or ctx is canceled. Non-text and malformed frames are
// skipped rather than tearing the session down. It closes queries on exit so the
// search loop ends, and cancels the session so the writer stops too.
func readQueries(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, queries chan<- debugQueryFrame) {
	defer close(queries)
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var q debugQueryFrame
		if err := json.Unmarshal(data, &q); err != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case queries <- q:
		}
	}
}

// runSearchLoop searches the corpus for each query and writes the hits back,
// echoing the request's sequence number. A search failure is reported in-band as
// an empty-hit error frame (logged server-side, generic to the client) rather
// than ending the session, so a single bad query does not drop the socket. A
// write failure cancels the session so the reader unwinds and no goroutine leaks.
func runSearchLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, searcher WikiSearcher, queries <-chan debugQueryFrame, logger *slog.Logger) {
	for q := range queries {
		frame := debugResultsFrame{Type: "results", Seq: q.Seq, Hits: []debugHit{}}
		hits, err := searcher.Search(ctx, q.Q)
		if err != nil {
			if ctx.Err() != nil {
				// The session is already torn down; stop quietly.
				return
			}
			logger.ErrorContext(ctx, "debug wiki search failed", slog.Any("err", err))
			frame.Error = "search failed"
		} else {
			frame.Hits = toDebugHits(hits)
		}
		if err := writeDebugResults(ctx, conn, frame); err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "debug results write failed", slog.Any("err", err))
			}
			cancel()
			return
		}
	}
}

// writeDebugResults writes one results frame under the shared bounded write
// deadline so a stalled client cannot wedge the session.
func writeDebugResults(ctx context.Context, conn *websocket.Conn, frame debugResultsFrame) error {
	ctx, cancel := context.WithTimeout(ctx, liveWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, frame)
}

// toDebugHits maps service hits to their wire form, bounding each excerpt. The
// result is empty, never nil, so it serializes to [] rather than null.
func toDebugHits(hits []service.WikiHit) []debugHit {
	out := make([]debugHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, debugHit{
			Title:      h.Title,
			URL:        h.URL,
			Snippet:    snippet(h.Content, debugMaxSnippetRunes),
			Similarity: h.Similarity,
		})
	}
	return out
}

// snippet truncates content to at most maxRunes runes, appending an ellipsis
// when it cut the text. Truncation is on rune boundaries so a multi-byte
// character is never split.
func snippet(content string, maxRunes int) string {
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes]) + "…"
}
