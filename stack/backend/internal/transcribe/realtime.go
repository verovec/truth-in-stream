package transcribe

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// realtimeAudioFormat pins the PCM encoding the client streams to Scribe v2
// Realtime: signed 16-bit little-endian PCM, mono, 16 kHz. The browser
// resamples playback audio to this format before sending.
const realtimeAudioFormat = "pcm_16000"

// Client message types sent to Scribe v2 Realtime.
const msgInputAudioChunk = "input_audio_chunk"

// Server message types received from Scribe v2 Realtime. The message_type
// field is the sole partial-vs-final discriminator.
const (
	msgSessionStarted          = "session_started"
	msgPartialTranscript       = "partial_transcript"
	msgCommittedTranscript     = "committed_transcript"
	msgCommittedWithTimestamps = "committed_transcript_with_timestamps"
	msgRateLimited             = "rate_limited"
)

// audioChunkMessage is one outbound audio frame: raw PCM base64-encoded into a
// JSON text frame. Commit is left false so server-side VAD owns segmentation.
type audioChunkMessage struct {
	MessageType string `json:"message_type"`
	AudioBase64 string `json:"audio_base_64"`
	Commit      bool   `json:"commit"`
}

// serverWord is one timestamped word in a committed transcript. Start and End
// are pointers because Scribe omits them on entries without timing.
type serverWord struct {
	Text  string   `json:"text"`
	Start *float64 `json:"start"`
	End   *float64 `json:"end"`
}

// serverMessage is the decoded shape of every inbound message. Only the fields
// this client acts on are modeled; unknown fields are ignored so a provider-side
// addition never breaks decoding.
type serverMessage struct {
	MessageType string       `json:"message_type"`
	Text        string       `json:"text"`
	Words       []serverWord `json:"words"`
	Error       string       `json:"error"`
}

// realtimeSocket is the slice of a WebSocket connection the streaming session
// drives: JSON messages in and out, plus teardown. It is an interface so the
// session loop is tested against a faked socket without a live provider.
type realtimeSocket interface {
	writeJSON(ctx context.Context, v any) error
	readJSON(ctx context.Context, v any) error
	close()
}

// wsSocket adapts a *websocket.Conn to realtimeSocket over JSON frames.
type wsSocket struct {
	conn *websocket.Conn
}

func (s *wsSocket) writeJSON(ctx context.Context, v any) error {
	return wsjson.Write(ctx, s.conn, v)
}

func (s *wsSocket) readJSON(ctx context.Context, v any) error {
	return wsjson.Read(ctx, s.conn, v)
}

func (s *wsSocket) close() {
	_ = s.conn.CloseNow()
}

// TranscribeStream streams audio chunks to Scribe v2 Realtime and emits a
// TranscriptEvent for every transcript the provider returns: partials as they
// are revised and committed segments as VAD finalizes them on detected silence.
// It dials the upstream socket eagerly so a connection failure surfaces from
// this call; per-segment results then flow over the returned channel, which
// closes when chunks closes, ctx is canceled, or the provider ends the session.
func (c *Client) TranscribeStream(ctx context.Context, chunks <-chan []byte, opts Options) (<-chan TranscriptEvent, error) {
	sock, err := c.dialRealtime(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make(chan TranscriptEvent)
	go c.streamSession(ctx, sock, chunks, out)
	return out, nil
}

// dialRealtime opens the upstream WebSocket to Scribe v2 Realtime, passing the
// API key as the xi-api-key header.
func (c *Client) dialRealtime(ctx context.Context, opts Options) (realtimeSocket, error) {
	endpoint, err := realtimeURL(c.realtimeURL, c.realtimeModel, opts.Language)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: http.Header{"xi-api-key": {c.apiKey}},
	})
	// On a successful handshake the library leaves resp.Body closed, but a failed
	// handshake hands back the error response for inspection; close it either way.
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("scribe: dial realtime: %w", err)
	}
	return &wsSocket{conn: conn}, nil
}

// realtimeURL builds the connection URL with the fixed audio format and
// server-side VAD segmentation, requesting word timestamps so committed
// segments carry their span. An empty language lets the provider auto-detect.
func realtimeURL(base, model, language string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("scribe: parse realtime url: %w", err)
	}
	q := url.Values{
		"model_id":           {model},
		"audio_format":       {realtimeAudioFormat},
		"commit_strategy":    {"vad"},
		"include_timestamps": {"true"},
	}
	if language != "" {
		q.Set("language_code", language)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// streamSession runs one live transcription: a writer pumps audio chunks to the
// provider while the reader emits transcript events, until the audio source is
// exhausted, the provider ends the session, or ctx is canceled. The deferred
// close and the shared cancel guarantee both goroutines stop and the socket is
// torn down, so no goroutine outlives the call.
//
// On normal end-of-audio the reader is canceled at once, so a committed segment
// the provider has not yet flushed for the trailing sub-silence-threshold audio
// is not emitted. That tail loss is the accepted cost of VAD segmentation
// without a manual end-of-stream commit.
func (c *Client) streamSession(ctx context.Context, sock realtimeSocket, chunks <-chan []byte, out chan<- TranscriptEvent) {
	defer close(out)
	defer sock.close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		c.writeAudio(ctx, sock, chunks)
	}()

	c.readEvents(ctx, sock, out)
	cancel()
	wg.Wait()
}

// writeAudio forwards each audio chunk as a base64 JSON frame until chunks
// closes, a write fails, or ctx is canceled. A write failure after cancellation
// is the expected teardown race and is not logged.
func (c *Client) writeAudio(ctx context.Context, sock realtimeSocket, chunks <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			msg := audioChunkMessage{
				MessageType: msgInputAudioChunk,
				AudioBase64: base64.StdEncoding.EncodeToString(chunk),
			}
			if err := sock.writeJSON(ctx, msg); err != nil {
				if ctx.Err() == nil {
					c.logger.ErrorContext(ctx, "scribe realtime: write audio", slog.Any("err", err))
				}
				return
			}
		}
	}
}

// readEvents decodes inbound messages and emits one TranscriptEvent per
// transcript, returning when the socket closes, a fatal provider message
// arrives, or ctx is canceled.
func (c *Client) readEvents(ctx context.Context, sock realtimeSocket, out chan<- TranscriptEvent) {
	for {
		var msg serverMessage
		if err := sock.readJSON(ctx, &msg); err != nil {
			if ctx.Err() == nil && !isNormalClose(err) {
				c.logger.ErrorContext(ctx, "scribe realtime: read", slog.Any("err", err))
			}
			return
		}
		event, ok, fatal := c.classify(ctx, msg)
		if fatal {
			return
		}
		if !ok {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case out <- event:
		}
	}
}

// classify maps one server message to a transcript event. ok reports whether an
// event should be emitted; fatal reports whether the session must end. Partial
// transcripts emit as non-final, committed transcripts as final; session_started
// and non-fatal errors are absorbed, and fatal errors end the stream.
func (c *Client) classify(ctx context.Context, msg serverMessage) (event TranscriptEvent, ok, fatal bool) {
	switch msg.MessageType {
	case msgPartialTranscript:
		if msg.Text == "" {
			return TranscriptEvent{}, false, false
		}
		return TranscriptEvent{Segment: Segment{Text: msg.Text}, Final: false}, true, false
	case msgCommittedTranscript, msgCommittedWithTimestamps:
		return TranscriptEvent{Segment: committedSegment(msg), Final: true}, true, false
	case msgSessionStarted:
		return TranscriptEvent{}, false, false
	case msgRateLimited:
		c.logger.ErrorContext(ctx, "scribe realtime: rate limited", slog.String("detail", msg.Error))
		return TranscriptEvent{}, false, true
	default:
		return TranscriptEvent{}, false, c.handleOther(ctx, msg)
	}
}

// handleOther logs error messages and reports whether the message is fatal.
// Fatal provider errors end the session; transient ones (throttling, malformed
// input, queue overflow, an empty commit) are logged and the stream continues.
func (c *Client) handleOther(ctx context.Context, msg serverMessage) bool {
	if isFatalMessage(msg.MessageType) {
		c.logger.ErrorContext(ctx, "scribe realtime: fatal provider error",
			slog.String("type", msg.MessageType), slog.String("detail", msg.Error))
		return true
	}
	if msg.Error != "" {
		c.logger.WarnContext(ctx, "scribe realtime: transient provider error",
			slog.String("type", msg.MessageType), slog.String("detail", msg.Error))
	}
	return false
}

// committedSegment builds a finalized Segment from a committed message, taking
// the span from the first and last timestamped words. Without timestamps the
// span is zero and only the text carries information.
func committedSegment(msg serverMessage) Segment {
	seg := Segment{Text: msg.Text}
	startSet := false
	for _, w := range msg.Words {
		if w.Start != nil && !startSet {
			seg.Start = seconds(*w.Start)
			startSet = true
		}
		if w.End != nil {
			seg.End = seconds(*w.End)
		}
	}
	return seg
}

// isFatalMessage reports whether a provider message type ends the session: the
// connection cannot recover from an auth failure, exhausted quota, session
// time limit, internal transcriber failure, or unaccepted terms.
func isFatalMessage(messageType string) bool {
	switch messageType {
	case "auth_error", "quota_exceeded", "session_time_limit_exceeded",
		"transcriber_error", "unaccepted_terms":
		return true
	default:
		return false
	}
}

// isNormalClose reports whether err is a clean WebSocket close, which is an
// expected end of stream rather than a fault worth logging.
func isNormalClose(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway
}
