package transcribe

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// AssemblyAI Universal-3 Pro streaming is the single diarizing speech-to-text
// provider, for live streams and imported videos alike. Verified 2026-06
// against assemblyai.com/docs streaming v3: connect to
// wss://streaming.assemblyai.com/v3/ws with the API key in the Authorization
// header (NOT Bearer); audio is sent as raw binary PCM s16le frames; the server
// returns JSON Turn messages whose end_of_turn flag commits a turn and whose
// speaker_label diarizes it. It diarizes inline, which the fact-checker needs so
// a verdict never blends two speakers.
const (
	defaultAssemblyAIURL   = "wss://streaming.assemblyai.com/v3/ws"
	defaultAssemblyAIModel = "u3-rt-pro"
	// assemblyAIEncoding and assemblyAISampleRateHz pin the PCM format the live
	// pipeline already produces: signed 16-bit little-endian, mono, 16 kHz.
	// assemblyAISampleRateHz is the single source of truth: it sizes the audio
	// coalescing buffer and is sent verbatim as the sample_rate query parameter.
	assemblyAIEncoding     = "pcm_s16le"
	assemblyAISampleRateHz = 16000
	// assemblyAIReadLimit raises the inbound cap above coder/websocket's 32 KiB
	// default: a formatted Turn for a long utterance carries a per-word array
	// that can exceed it, and the default turns that into a fatal read error that
	// kills the live session. 4 MiB covers far longer turns while still bounding a
	// misbehaving provider.
	assemblyAIReadLimit int64 = 4 << 20
	// AssemblyAI streaming v3 accepts only audio frames carrying 50-1000 ms of
	// audio; a frame outside that range is rejected with WebSocket close code
	// 3007, which kills the session. The live pipeline emits ~3 ms frames (one per
	// audio worklet block), so the writer coalesces them into assemblyAIChunkBytes
	// frames before sending and drops a trailing buffer below assemblyAIMinChunkBytes
	// on teardown. Coalescing in fixed assemblyAIChunkBytes units also caps the
	// upper bound: a single oversized inbound chunk is split, so an emitted frame
	// never exceeds 100 ms. Sizes are in bytes of the pinned PCM format: 2 bytes
	// per sample, one channel, so assemblyAISampleRateHz*2/1000 bytes per ms.
	assemblyAIBytesPerMilli = assemblyAISampleRateHz * 2 / 1000
	assemblyAIChunkBytes    = 100 * assemblyAIBytesPerMilli
	assemblyAIMinChunkBytes = 50 * assemblyAIBytesPerMilli
)

// Server message types received from AssemblyAI streaming v3. The type field is
// the discriminator; end_of_turn separates partial from committed turns.
const (
	aaiMsgBegin       = "Begin"
	aaiMsgTurn        = "Turn"
	aaiMsgTermination = "Termination"
	// aaiMsgError is a fatal session error the provider sends as a JSON frame
	// just before it closes the socket; its close reason is only "See Error
	// message for details", so this message carries the actual cause.
	aaiMsgError = "Error"
)

// aaiWord is one word in a Turn. Timestamps are integer milliseconds; speaker
// is the per-word diarization label, populated on committed words when
// speaker_labels is enabled.
type aaiWord struct {
	Text        string `json:"text"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	WordIsFinal bool   `json:"word_is_final"`
	Speaker     string `json:"speaker"`
}

// aaiMessage is the decoded shape of every inbound message. Only the fields this
// client acts on are modeled; unknown fields are ignored so a provider-side
// addition never breaks decoding.
type aaiMessage struct {
	Type         string    `json:"type"`
	Transcript   string    `json:"transcript"`
	EndOfTurn    bool      `json:"end_of_turn"`
	SpeakerLabel string    `json:"speaker_label"`
	Words        []aaiWord `json:"words"`
	// ErrorCode and Error carry the cause of an aaiMsgError frame, e.g. code 3007
	// "Audio chunk duration violation".
	ErrorCode int    `json:"error_code"`
	Error     string `json:"error"`
}

// aaiSocket is the slice of a WebSocket connection the AssemblyAI session
// drives: binary audio out, JSON messages in, plus teardown. It is an interface
// so the session loop is tested against a faked socket without a live provider.
type aaiSocket interface {
	// writeBinary sends data synchronously and must not retain the slice past
	// return, so the caller may reuse the backing array for the next frame.
	writeBinary(ctx context.Context, data []byte) error
	readJSON(ctx context.Context, v any) error
	close()
}

// aaiWSSocket adapts a *websocket.Conn to aaiSocket: audio as binary frames,
// inbound messages as JSON frames.
type aaiWSSocket struct {
	conn *websocket.Conn
}

func (s *aaiWSSocket) writeBinary(ctx context.Context, data []byte) error {
	return s.conn.Write(ctx, websocket.MessageBinary, data)
}

func (s *aaiWSSocket) readJSON(ctx context.Context, v any) error {
	return wsjson.Read(ctx, s.conn, v)
}

func (s *aaiWSSocket) close() {
	_ = s.conn.CloseNow()
}

// AssemblyAIConfig configures an AssemblyAIClient. APIKey is required; Model,
// URL, and Logger default to Universal-3 Pro streaming, its v3 endpoint, and
// slog.Default. MaxSpeakers, when positive, hints the diarizer at the expected
// speaker count and improves label stability.
type AssemblyAIConfig struct {
	APIKey      string
	Model       string
	URL         string
	MaxSpeakers int
	Logger      *slog.Logger
}

// AssemblyAIClient streams audio to AssemblyAI Universal-3 Pro and emits
// diarized transcript events. It satisfies the streaming contract the live
// pipeline consumes (TranscribeStream); there is no batch transcriber.
type AssemblyAIClient struct {
	apiKey      string
	model       string
	url         string
	maxSpeakers int
	logger      *slog.Logger
}

// NewAssemblyAI builds an AssemblyAIClient from cfg, applying defaults for the
// optional fields.
func NewAssemblyAI(cfg AssemblyAIConfig) *AssemblyAIClient {
	c := &AssemblyAIClient{
		apiKey:      cfg.APIKey,
		model:       cfg.Model,
		url:         cfg.URL,
		maxSpeakers: cfg.MaxSpeakers,
		logger:      cfg.Logger,
	}
	if c.model == "" {
		c.model = defaultAssemblyAIModel
	}
	if c.url == "" {
		c.url = defaultAssemblyAIURL
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c
}

// TranscribeStream streams audio chunks to AssemblyAI and emits a
// TranscriptEvent for every Turn: partials as they revise and committed turns as
// the provider finalizes them on a detected pause, each carrying its diarized
// speaker label. It dials the upstream socket eagerly so a connection failure
// surfaces from this call; per-turn results then flow over the returned channel,
// which closes when chunks closes, ctx is canceled, or the provider ends the
// session.
func (c *AssemblyAIClient) TranscribeStream(ctx context.Context, chunks <-chan []byte, opts Options) (<-chan TranscriptEvent, error) {
	sock, err := c.dial(ctx, opts)
	if err != nil {
		return nil, err
	}
	out := make(chan TranscriptEvent)
	go c.streamSession(ctx, sock, chunks, out)
	return out, nil
}

// dial opens the upstream WebSocket to AssemblyAI streaming v3, passing the API
// key as the Authorization header.
func (c *AssemblyAIClient) dial(ctx context.Context, opts Options) (aaiSocket, error) {
	endpoint, err := assemblyAIURL(c.url, c.model, c.maxSpeakers, opts.Language)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {c.apiKey}},
	})
	// A successful handshake leaves resp.Body closed; a failed one hands back the
	// error response for inspection. Close it either way.
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("assemblyai: dial streaming: %w", err)
	}
	conn.SetReadLimit(assemblyAIReadLimit)
	return &aaiWSSocket{conn: conn}, nil
}

// assemblyAIURL builds the connection URL with the model, fixed PCM format, and
// streaming diarization enabled. A positive maxSpeakers and a non-empty language
// are added only when set, so the provider auto-detects otherwise.
func assemblyAIURL(base, model string, maxSpeakers int, language string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("assemblyai: parse streaming url: %w", err)
	}
	q := url.Values{
		"speech_model":   {model},
		"sample_rate":    {strconv.Itoa(assemblyAISampleRateHz)},
		"encoding":       {assemblyAIEncoding},
		"speaker_labels": {"true"},
	}
	if maxSpeakers > 0 {
		q.Set("max_speakers", strconv.Itoa(maxSpeakers))
	}
	if language != "" {
		q.Set("language", language)
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
// On end-of-audio the reader is canceled at once (the audio source closing is
// itself driven by the session ending), so a turn the provider has not yet
// committed for trailing sub-pause audio is not emitted. That tail loss is the
// accepted cost of turn-based segmentation; the live aggregator's idle flush
// still scores a buffered short turn.
func (c *AssemblyAIClient) streamSession(ctx context.Context, sock aaiSocket, chunks <-chan []byte, out chan<- TranscriptEvent) {
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

// writeAudio coalesces inbound audio into provider-sized frames and forwards
// each as a binary frame until chunks closes, a write fails, or ctx is canceled.
// The live pipeline emits ~3 ms frames, far below AssemblyAI's 50 ms minimum, so
// sending them through unbatched trips close code 3007; this accumulates and
// emits in fixed assemblyAIChunkBytes (100 ms) frames. Emitting fixed-size frames
// also splits an oversized inbound chunk, so an emitted frame never exceeds the
// 1000 ms upper bound either. Because the source is paced to playback, a frame
// fills at roughly real time, keeping within the transmission-rate limit too. On
// a clean end-of-stream a trailing buffer is flushed if it meets the 50 ms
// minimum, else dropped; on a canceled teardown the flush write no-ops and the
// sub-frame tail is dropped, the accepted tail loss. A write failure is logged
// unless ctx is already canceled, the expected teardown race.
func (c *AssemblyAIClient) writeAudio(ctx context.Context, sock aaiSocket, chunks <-chan []byte) {
	// Capacity holds a full frame plus the carry-over tail, so the steady state
	// never reallocates.
	buf := make([]byte, 0, 2*assemblyAIChunkBytes)
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-chunks:
			if !ok {
				if len(buf) >= assemblyAIMinChunkBytes {
					if err := sock.writeBinary(ctx, buf); err != nil && ctx.Err() == nil {
						c.logger.ErrorContext(ctx, "assemblyai: write audio", slog.Any("err", err))
					}
				}
				return
			}
			buf = append(buf, chunk...)
			// Emit every full frame at an advancing offset, then compact the tail
			// once. Reading frames in place keeps the loop O(n) for an oversized
			// chunk, and the single trailing copy is non-overlapping (sent is a
			// multiple of the frame size, so it is at least the tail length).
			sent := 0
			for len(buf)-sent >= assemblyAIChunkBytes {
				if err := sock.writeBinary(ctx, buf[sent:sent+assemblyAIChunkBytes]); err != nil {
					if ctx.Err() == nil {
						c.logger.ErrorContext(ctx, "assemblyai: write audio", slog.Any("err", err))
					}
					return
				}
				sent += assemblyAIChunkBytes
			}
			if sent > 0 {
				buf = buf[:copy(buf, buf[sent:])]
			}
		}
	}
}

// readEvents decodes inbound messages and emits one TranscriptEvent per Turn,
// returning when the socket closes, the provider terminates the session, or ctx
// is canceled. AssemblyAI signals fatal errors with a non-normal WebSocket
// close, not a data-channel message, so a read error that is not a clean close
// is logged and ends the stream.
func (c *AssemblyAIClient) readEvents(ctx context.Context, sock aaiSocket, out chan<- TranscriptEvent) {
	for {
		var msg aaiMessage
		if err := sock.readJSON(ctx, &msg); err != nil {
			if ctx.Err() == nil && !isNormalClose(err) {
				c.logger.ErrorContext(ctx, "assemblyai: read", slog.Any("err", err))
			}
			return
		}
		event, ok, end := c.classify(ctx, msg)
		if end {
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
// event should be emitted; end reports whether the session has ended. A Turn
// with end_of_turn emits as final, an in-progress Turn as a partial; Begin is
// absorbed and a Termination ends the stream. An unrecognized type is absorbed
// and logged at debug, since fatal provider errors arrive as a WebSocket close
// rather than a data-channel message.
func (c *AssemblyAIClient) classify(ctx context.Context, msg aaiMessage) (event TranscriptEvent, ok, end bool) {
	switch msg.Type {
	case aaiMsgTurn:
		if msg.Transcript == "" {
			return TranscriptEvent{}, false, false
		}
		if msg.EndOfTurn {
			return TranscriptEvent{Segment: aaiSegment(msg), Final: true}, true, false
		}
		return TranscriptEvent{Segment: Segment{Text: msg.Transcript, Speaker: msg.SpeakerLabel}, Final: false}, true, false
	case aaiMsgTermination:
		return TranscriptEvent{}, false, true
	case aaiMsgError:
		// Surface the provider's stated cause at error level: the WebSocket close
		// reason only points here ("See Error message for details"), so without
		// this the real failure (e.g. code 3007 audio chunk duration violation) is
		// invisible. The error ends the session.
		c.logger.ErrorContext(ctx, "assemblyai: session error",
			slog.Int("error_code", msg.ErrorCode),
			slog.String("error", msg.Error))
		return TranscriptEvent{}, false, true
	case aaiMsgBegin:
		return TranscriptEvent{}, false, false
	default:
		c.logger.DebugContext(ctx, "assemblyai: ignoring unrecognized message", slog.String("type", msg.Type))
		return TranscriptEvent{}, false, false
	}
}

// aaiSegment builds a finalized Segment from a committed Turn. Start is the first
// word's onset and End is the latest word end, so a trailing in-progress word
// with a zero end (a committed Turn can still carry one) cannot truncate the
// span. The speaker is the turn-level diarization label. With no words the span
// is zero and only the text and speaker carry information. End is clamped to at
// least Start so a turn whose only word has a zero end never yields an inverted
// span.
func aaiSegment(msg aaiMessage) Segment {
	seg := Segment{Text: msg.Transcript, Speaker: msg.SpeakerLabel}
	for i, w := range msg.Words {
		if i == 0 {
			seg.Start = millis(w.Start)
		}
		if end := millis(w.End); end > seg.End {
			seg.End = end
		}
	}
	if seg.End < seg.Start {
		seg.End = seg.Start
	}
	return seg
}

// millis converts an integer-millisecond provider timestamp to a Duration.
func millis(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// isNormalClose reports whether err is a clean WebSocket close (the provider
// ended the session), so an expected end of stream is not logged as a read
// error.
func isNormalClose(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway
}
