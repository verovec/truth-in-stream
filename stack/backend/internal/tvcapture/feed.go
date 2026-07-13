package tvcapture

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coder/websocket"
)

// wsFeedConnector dials the backend publisher WebSocket for a channel, carrying
// the client-credentials token in the Authorization header on the upgrade
// request (never in the URL, so it cannot leak into logs or intermediaries).
type wsFeedConnector struct {
	client     *backendClient
	tokens     tokenProvider
	httpClient *http.Client
}

func newWSFeedConnector(client *backendClient, tokens tokenProvider, httpClient *http.Client) *wsFeedConnector {
	return &wsFeedConnector{client: client, tokens: tokens, httpClient: httpClient}
}

// Connect opens the publisher socket for channelID and returns a frameSink that
// writes each PCM frame as a binary message. The server's identity gate reads
// the bearer token from the Authorization header first, so no access_token query
// parameter is needed for a non-browser client like the worker.
func (c *wsFeedConnector) Connect(ctx context.Context, channelID string) (frameSink, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	opts := &websocket.DialOptions{
		HTTPClient: c.httpClient,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	}
	conn, resp, err := websocket.Dial(ctx, c.client.FeedURL(channelID), opts)
	if resp != nil {
		// A rejected token (401/403) on the upgrade means the cached token is
		// stale; drop it so the next attempt refetches.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			c.tokens.Invalidate()
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("tvcapture: dial feed for channel %s failed", channelID)
	}
	// The worker only writes PCM; it never reads application messages. But the
	// server's publisher handler runs a ping loop and closes the socket if no pong
	// returns, and coder/websocket only answers pings while something is reading.
	// CloseRead runs that reader (discarding any inbound frame), so pings are
	// ponged and the session is not force-closed on the ping interval.
	conn.CloseRead(ctx)
	return &wsFrameSink{conn: conn}, nil
}

// wsFrameSink writes PCM frames to the publisher socket as binary messages.
type wsFrameSink struct {
	conn *websocket.Conn
}

func (s *wsFrameSink) Send(ctx context.Context, frame []byte) error {
	if err := s.conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		return fmt.Errorf("tvcapture: write feed frame: %w", err)
	}
	return nil
}

func (s *wsFrameSink) Close() error {
	return s.conn.Close(websocket.StatusNormalClosure, "capture ended")
}
