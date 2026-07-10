package handler

// TV live hub WebSocket API.
//
//	GET /api/tv/channels/{id}/feed   (WebSocket, admin/service) publisher: a
//	                                 capture process streams 16 kHz mono PCM in;
//	                                 one publisher per channel.
//	GET /api/tv/channels/{id}/live   (WebSocket, any authed) viewer: subtitle and
//	                                 verdict events out, with a recent backlog on
//	                                 join and an off_air frame when the feed ends.
//
// The feed and viewer sockets reuse the browser live path's audio contract and
// event wire frames unchanged (see live.go): a channel session is byte-for-byte
// the video session at the wire, so the frontend live components work as-is. The
// per-channel session lifecycle lives in service.TVHub; the handlers own only the
// sockets.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// TVFeedHub starts a publisher session for a channel. *service.TVHub satisfies it.
type TVFeedHub interface {
	Publish(ctx context.Context, channelID string) (*service.TVPublisher, error)
}

// TVViewerHub subscribes a viewer to a channel. *service.TVHub satisfies it.
type TVViewerHub interface {
	Subscribe(channelID string) *service.TVSubscriber
}

// TVHub is the whole hub surface NewMux wires: live-status enrichment for the
// channel list plus the publisher and viewer sockets. *service.TVHub satisfies
// it.
type TVHub interface {
	TVLiveStatus
	TVFeedHub
	TVViewerHub
}

// offAirType is the wire discriminator for the off-air control frame the viewer
// socket sends when a channel's capture feed disconnects. It is additive: a
// client that does not understand it drops the frame.
const offAirType = "off_air"

// offAirFrame tells a viewer the channel's live session ended; the socket closes
// right after. It is not a service.LiveEvent (the analyzer never emits it) but a
// transport control frame the hub raises on publisher disconnect.
type offAirFrame struct {
	Type string `json:"type"`
}

// tvFeedHandler is the publisher socket: a capture process (the tvcapture
// worker, carrying an admin service token) streams the channel's PCM audio in.
// It is registered admin-gated, so only a verified admin/service caller reaches
// it. A second publisher for a live channel is rejected with a policy-violation
// close. Inbound binary frames are forwarded to the channel's analysis session;
// the socket closing ends the session and takes viewers off air.
func tvFeedHandler(hub TVFeedHub, allowedOrigins []string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(liveReadLimit)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		channelID := r.PathValue("id")

		pub, err := hub.Publish(ctx, channelID)
		if err != nil {
			if errors.Is(err, service.ErrTVChannelBusy) {
				_ = conn.Close(websocket.StatusPolicyViolation, "channel already has a publisher")
				return
			}
			logger.ErrorContext(ctx, "tv feed: start session failed", slog.String("channel_id", channelID), slog.Any("err", err))
			_ = conn.Close(websocket.StatusInternalError, "capture unavailable")
			return
		}
		defer pub.Close()

		go pingLoop(ctx, cancel, conn, livePingInterval, livePingTimeout)

		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary || len(data) == 0 {
				continue
			}
			pub.Feed(data)
		}
	}
}

// tvViewerHandler is the read-only viewer socket: any authenticated user follows
// a channel's live fact-check stream. It sends the recent-event backlog first,
// then live events as they arrive, then an off_air frame when the session ends.
// A drain reader services control frames and detects the client leaving. A viewer
// write failure means the client left, which needs no logging.
func tvViewerHandler(hub TVViewerHub, allowedOrigins []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		channelID := r.PathValue("id")

		sub := hub.Subscribe(channelID)
		defer sub.Close()

		go drainReader(ctx, cancel, conn)
		go pingLoop(ctx, cancel, conn, livePingInterval, livePingTimeout)

		for _, ev := range sub.Backlog() {
			if err := writeEvent(ctx, conn, ev, false); err != nil {
				return
			}
		}
		for msg := range sub.Messages() {
			if msg.OffAir {
				if err := writeOffAir(ctx, conn); err != nil {
					return
				}
				_ = conn.Close(websocket.StatusNormalClosure, "off air")
				return
			}
			if err := writeEvent(ctx, conn, *msg.Event, false); err != nil {
				return
			}
		}
	}
}

// drainReader reads and discards inbound frames so the WebSocket library can
// service control frames (pongs, close) and the handler learns when the viewer
// leaves. A viewer sends no application data; any read error cancels the session
// so the writer stops.
func drainReader(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}

// writeOffAir sends the off-air control frame under the shared write deadline.
func writeOffAir(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, liveWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, offAirFrame{Type: offAirType})
}
