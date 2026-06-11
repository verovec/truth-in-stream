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

// livePingInterval and livePingTimeout drive a keepalive ping that detects a
// half-open connection (a peer that vanished without a close frame: laptop
// sleep, network drop, crashed tab). Without it, conn.Read on a dead peer
// blocks forever, pinning the reader, the writer, and the upstream provider
// session. The ping tolerates legitimate playback pauses: the browser answers
// pings even when it is sending no audio, so only a genuinely dead peer trips it.
const (
	livePingInterval = 30 * time.Second
	livePingTimeout  = 10 * time.Second
)

// liveAudioBuffer bounds how many inbound frames may queue between the socket
// reader and the analyzer. A small buffer absorbs transient analysis stalls so
// readAudio keeps returning to conn.Read (where the library services pong
// frames), which keeps the keepalive ping from mistaking backpressure for a
// dead peer. It also bounds memory; sustained overload still applies
// backpressure once the buffer fills. At ~100 ms/frame this is a few seconds.
const liveAudioBuffer = 32

// interimFrame is the wire form of an interim event: the live, still-revised
// caption for the current utterance. It carries only text - no id, no
// timestamps, no verdict - and the next interim or subtitle supersedes it.
type interimFrame struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

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
// (embedded segmentJSON, so the live and batch result shapes are the same type
// by construction and cannot drift) plus the correlation id and an optional
// non-fatal analysis error.
type resultFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	segmentJSON
	Error string `json:"error,omitempty"`
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
		audio := make(chan []byte, liveAudioBuffer)
		go readAudio(ctx, cancel, conn, audio)
		go pingLoop(ctx, cancel, conn, livePingInterval, livePingTimeout)

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
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case audio <- data:
		}
	}
}

// pingLoop pings the peer on a fixed interval and cancels the session when a
// ping is not answered within timeout, so a half-open connection is reclaimed
// instead of blocking the reader forever. It exits when ctx is canceled.
func pingLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// writeEvents serializes each live event to a JSON text frame until the event
// stream ends or a committed-event write fails. A failed write of a committed
// event (subtitle or result) cancels the session so the reader unwinds and no
// goroutine leaks. An interim caption is disposable, so a failed interim write
// is logged and dropped rather than tearing the session down over a throwaway
// partial; a genuinely dead client surfaces on the next committed write.
func writeEvents(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, events <-chan service.LiveEvent, logger *slog.Logger, videoID string) {
	for ev := range events {
		if err := writeEvent(ctx, conn, ev); err != nil {
			// A disposable interim is dropped silently rather than logged per
			// frame (they arrive several times a second); a truly dead client
			// surfaces on the next committed event's write below.
			if ev.Kind == service.LiveEventInterim {
				continue
			}
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
	if ev.Kind == service.LiveEventInterim {
		return wsjson.Write(ctx, conn, interimFrame{
			Type: string(ev.Kind),
			Text: ev.Segment.Text,
		})
	}
	if ev.Kind == service.LiveEventSubtitle {
		return wsjson.Write(ctx, conn, subtitleFrame{
			Type:  string(ev.Kind),
			ID:    ev.ID,
			Start: ev.Segment.Start.Seconds(),
			End:   ev.Segment.End.Seconds(),
			Text:  ev.Segment.Text,
		})
	}
	return wsjson.Write(ctx, conn, resultFrame{
		Type:        string(ev.Kind),
		ID:          ev.ID,
		segmentJSON: toSegmentJSON(domain.SegmentResult{Segment: ev.Segment, Matches: ev.Matches, SkipReason: ev.SkipReason}),
		Error:       ev.Err,
	})
}
