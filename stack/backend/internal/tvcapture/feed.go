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
	client *backendClient
	tokens tokenProvider
}

func newWSFeedConnector(client *backendClient, tokens tokenProvider) *wsFeedConnector {
	return &wsFeedConnector{client: client, tokens: tokens}
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
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	}
	conn, resp, err := websocket.Dial(ctx, c.client.FeedURL(channelID), opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("tvcapture: dial feed for channel %s failed", channelID)
	}
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
