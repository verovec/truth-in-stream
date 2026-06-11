package handler

// Live fact-check streaming API.
//
//	GET /api/videos/{id}/live   (WebSocket) stream a video's audio, receive
//	                            incremental subtitles and fact-check results
//
// The client opens a WebSocket for a known video and streams its audio as raw
// 16 kHz mono PCM in binary frames, paced to playback. The server transcribes
// live, gates and matches each finalized statement, and pushes two JSON text
// frames per statement: a "subtitle" the moment the statement is transcribed,
// then a "result" once its verdict is ready. Both share an "id" so a verdict
// that lands after its subtitle reconciles to the right statement. A result
// frame mirrors the batch per-segment shape (start, end, text, matches,
// skip_reason) so a client written against batch results handles it unchanged.

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// LiveAnalyzer is the slice of the live analysis service the WebSocket handler
// drives: audio bytes in, fact-check events out. Satisfied by
// *service.LiveAnalyzer. The handler owns the socket; this port carries no
// transport types.
type LiveAnalyzer interface {
	Run(ctx context.Context, audio <-chan []byte) (<-chan service.LiveEvent, error)
}

// liveReadLimit bounds one inbound audio frame. At 16 kHz mono 16-bit PCM a
// second of audio is 32 KB, so 1 MiB leaves ample room for coarse client
// chunking while rejecting a runaway frame.
const liveReadLimit = 1 << 20

// liveWriteTimeout bounds a single outbound frame write so a stalled client
// cannot wedge the session indefinitely.
const liveWriteTimeout = 10 * time.Second

// subtitleFrame is the wire form of a subtitle event: a statement's text the
// moment it is transcribed, before any verdict.
type subtitleFrame struct {
	Type  string  `json:"type"`
	ID    string  `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// resultFrame is the wire form of a result event: the batch per-segment shape
// plus the correlation id and an optional non-fatal analysis error. Matches is
// always present (empty means "checked, no confident match"); skip_reason is set
// only when the gate declined to check the statement.
type resultFrame struct {
	Type       string                `json:"type"`
	ID         string                `json:"id"`
	Start      float64               `json:"start"`
	End        float64               `json:"end"`
	Text       string                `json:"text"`
	Matches    []domain.SegmentMatch `json:"matches"`
	SkipReason string                `json:"skip_reason,omitempty"`
	Error      string                `json:"error,omitempty"`
}

// liveHandler upgrades the request to a WebSocket and bridges it to the live
// analyzer: a reader pumps inbound audio frames to the analyzer while the main
// goroutine writes the analyzer's events back. allowedOrigins are the browser
// origins permitted to connect cross-origin; empty enforces same-origin.
func liveHandler(analyzer LiveAnalyzer, allowedOrigins []string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
		if err != nil {
			// Accept has already written the handshake failure response.
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(liveReadLimit)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		videoID := r.PathValue("id")
		audio := make(chan []byte)
		go readAudio(ctx, cancel, conn, audio)

		events, err := analyzer.Run(ctx, audio)
		if err != nil {
			logger.ErrorContext(ctx, "live analyze start failed", slog.String("video_id", videoID), slog.Any("err", err))
			_ = conn.Close(websocket.StatusInternalError, "analysis unavailable")
			return
		}

		writeEvents(ctx, cancel, conn, events, logger, videoID)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

// readAudio forwards inbound binary frames to the analyzer until the client
// closes, a read fails, or ctx is canceled. Non-binary frames are ignored. It
// closes audio on exit so the analyzer's stream ends, and cancels the session so
// the writer stops too.
func readAudio(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, audio chan<- []byte) {
	defer close(audio)
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case audio <- data:
		}
	}
}

// writeEvents serializes each live event to a JSON text frame until the event
// stream ends or a write fails. A write failure cancels the session so the
// reader unwinds and no goroutine leaks.
func writeEvents(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, events <-chan service.LiveEvent, logger *slog.Logger, videoID string) {
	for ev := range events {
		if err := writeEvent(ctx, conn, ev); err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "live event write failed", slog.String("video_id", videoID), slog.Any("err", err))
			}
			cancel()
			return
		}
	}
}

// writeEvent writes one event under a bounded deadline, shaping it by kind.
func writeEvent(ctx context.Context, conn *websocket.Conn, ev service.LiveEvent) error {
	ctx, cancel := context.WithTimeout(ctx, liveWriteTimeout)
	defer cancel()
	if ev.Kind == service.LiveEventSubtitle {
		return wsjson.Write(ctx, conn, subtitleFrame{
			Type:  string(ev.Kind),
			ID:    ev.ID,
			Start: ev.Segment.Start.Seconds(),
			End:   ev.Segment.End.Seconds(),
			Text:  ev.Segment.Text,
		})
	}
	matches := ev.Matches
	if matches == nil {
		matches = []domain.SegmentMatch{}
	}
	return wsjson.Write(ctx, conn, resultFrame{
		Type:       string(ev.Kind),
		ID:         ev.ID,
		Start:      ev.Segment.Start.Seconds(),
		End:        ev.Segment.End.Seconds(),
		Text:       ev.Segment.Text,
		Matches:    matches,
		SkipReason: string(ev.SkipReason),
		Error:      ev.Err,
	})
}
