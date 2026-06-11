package transcribe

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/go-cmp/cmp"
)

// silentClient builds a Client whose realtime logging is discarded, keeping
// test output free of the expected error/warn lines from the classify cases.
func silentClient() *Client {
	return New(Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestRealtimeURL(t *testing.T) {
	t.Parallel()

	got, err := realtimeURL("wss://example.test/realtime", "scribe_v2_realtime", "en")
	if err != nil {
		t.Fatalf("realtimeURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if u.Scheme != "wss" || u.Host != "example.test" || u.Path != "/realtime" {
		t.Fatalf("unexpected base: %s", got)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"model_id":           "scribe_v2_realtime",
		"audio_format":       "pcm_16000",
		"commit_strategy":    "vad",
		"include_timestamps": "true",
		"language_code":      "en",
	} {
		if q.Get(key) != want {
			t.Errorf("query %q = %q, want %q", key, q.Get(key), want)
		}
	}
}

func TestRealtimeURLOmitsEmptyLanguage(t *testing.T) {
	t.Parallel()

	got, err := realtimeURL("wss://example.test/realtime", "scribe_v2_realtime", "")
	if err != nil {
		t.Fatalf("realtimeURL: %v", err)
	}
	u, _ := url.Parse(got)
	if _, ok := u.Query()["language_code"]; ok {
		t.Errorf("language_code present for empty language: %s", got)
	}
}

func TestCommittedSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  serverMessage
		want Segment
	}{
		{
			name: "spans first and last timed words",
			msg: serverMessage{
				Text: "the earth is round",
				Words: []serverWord{
					{Text: "the", Start: ptr(1.0), End: ptr(1.2)},
					{Text: "earth", Start: ptr(1.2), End: ptr(1.6)},
					{Text: "round", Start: ptr(1.6), End: ptr(2.0)},
				},
			},
			want: Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"},
		},
		{
			name: "tolerates untimed words",
			msg: serverMessage{
				Text:  "hello",
				Words: []serverWord{{Text: "hello", Start: nil, End: nil}},
			},
			want: Segment{Start: 0, End: 0, Text: "hello"},
		},
		{
			name: "no words leaves a text-only segment",
			msg:  serverMessage{Text: "no timestamps"},
			want: Segment{Text: "no timestamps"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := committedSegment(tc.msg); got != tc.want {
				t.Errorf("committedSegment() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	c := silentClient()
	ctx := t.Context()

	tests := []struct {
		name      string
		msg       serverMessage
		wantEvent TranscriptEvent
		wantOK    bool
		wantFatal bool
	}{
		{
			name:      "partial emits non-final",
			msg:       serverMessage{MessageType: msgPartialTranscript, Text: "the earth"},
			wantEvent: TranscriptEvent{Segment: Segment{Text: "the earth"}, Final: false},
			wantOK:    true,
		},
		{
			name:   "empty partial is absorbed",
			msg:    serverMessage{MessageType: msgPartialTranscript, Text: ""},
			wantOK: false,
		},
		{
			name: "committed emits final",
			msg: serverMessage{
				MessageType: msgCommittedWithTimestamps,
				Text:        "the earth is round",
				Words:       []serverWord{{Text: "the", Start: ptr(1.0), End: ptr(2.0)}},
			},
			wantEvent: TranscriptEvent{Segment: Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"}, Final: true},
			wantOK:    true,
		},
		{
			name:   "session_started is absorbed",
			msg:    serverMessage{MessageType: msgSessionStarted},
			wantOK: false,
		},
		{
			name:      "rate limited is fatal",
			msg:       serverMessage{MessageType: msgRateLimited, Error: "slow down"},
			wantFatal: true,
		},
		{
			name:      "auth error is fatal",
			msg:       serverMessage{MessageType: "auth_error", Error: "bad key"},
			wantFatal: true,
		},
		{
			name:   "transient input error continues",
			msg:    serverMessage{MessageType: "input_error", Error: "bad chunk"},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			event, ok, fatal := c.classify(ctx, tc.msg)
			if ok != tc.wantOK || fatal != tc.wantFatal {
				t.Fatalf("classify() ok=%v fatal=%v, want ok=%v fatal=%v", ok, fatal, tc.wantOK, tc.wantFatal)
			}
			if tc.wantOK && event != tc.wantEvent {
				t.Errorf("classify() event = %+v, want %+v", event, tc.wantEvent)
			}
		})
	}
}

func TestTranscribeStreamReadsOversizedCommittedTranscript(t *testing.T) {
	t.Parallel()
	// A committed transcript for a long utterance exceeds coder/websocket's 32 KiB
	// default read limit. Without raising the limit the read fails fatally
	// ("message too big") and the live session dies mid-stream; the client must
	// read the message and emit its event.
	bigText := strings.Repeat("alpha ", 10000) // ~60 KiB, over the 32 KiB default
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if err := wsjson.Write(r.Context(), conn, map[string]any{
			"message_type": msgCommittedTranscript,
			"text":         bigText,
		}); err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)

	client := New(Config{
		APIKey:      "k",
		RealtimeURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events, err := client.TranscribeStream(ctx, make(chan []byte), Options{})
	if err != nil {
		t.Fatalf("TranscribeStream: %v", err)
	}

	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("event channel closed with no event - the oversized message was rejected")
		}
		if !ev.Final || ev.Segment.Text != bigText {
			t.Errorf("got final=%v text-len=%d, want a final committed transcript carrying the full text", ev.Final, len(ev.Segment.Text))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the committed transcript event")
	}
}

// scriptedSocket is a faked realtimeSocket: it returns a fixed sequence of
// server messages, then either a final error or blocks until ctx is done, while
// capturing every audio frame written.
type scriptedSocket struct {
	msgs      []serverMessage
	finalErr  error
	blockRead bool

	mu     sync.Mutex
	idx    int
	writes []audioChunkMessage
	closed bool
}

func (s *scriptedSocket) writeJSON(_ context.Context, v any) error {
	s.mu.Lock()
	s.writes = append(s.writes, v.(audioChunkMessage))
	s.mu.Unlock()
	return nil
}

func (s *scriptedSocket) readJSON(ctx context.Context, v any) error {
	s.mu.Lock()
	if s.idx < len(s.msgs) {
		m := s.msgs[s.idx]
		s.idx++
		s.mu.Unlock()
		*(v.(*serverMessage)) = m
		return nil
	}
	s.mu.Unlock()
	if s.blockRead {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.finalErr
}

func (s *scriptedSocket) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *scriptedSocket) capturedWrites() []audioChunkMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audioChunkMessage(nil), s.writes...)
}

func collectEvents(t *testing.T, out <-chan TranscriptEvent) []TranscriptEvent {
	t.Helper()
	var events []TranscriptEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range out {
			events = append(events, ev)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event channel to close")
	}
	return events
}

func TestStreamSessionEmitsUntilServerClose(t *testing.T) {
	t.Parallel()

	sock := &scriptedSocket{
		msgs: []serverMessage{
			{MessageType: msgSessionStarted},
			{MessageType: msgPartialTranscript, Text: "the earth"},
			{
				MessageType: msgCommittedWithTimestamps,
				Text:        "the earth is round",
				Words:       []serverWord{{Text: "the", Start: ptr(1.0), End: ptr(2.0)}},
			},
		},
		finalErr: websocket.CloseError{Code: websocket.StatusNormalClosure},
	}
	chunks := make(chan []byte) // never fed, never closed; reader ends the session

	c := silentClient()
	out := make(chan TranscriptEvent)
	go c.streamSession(t.Context(), sock, chunks, out)

	events := collectEvents(t, out)
	want := []TranscriptEvent{
		{Segment: Segment{Text: "the earth"}, Final: false},
		{Segment: Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round"}, Final: true},
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
	if !sock.closed {
		t.Error("socket was not closed")
	}
}

func TestStreamSessionWritesAudioThenEndsOnChunkClose(t *testing.T) {
	t.Parallel()

	sock := &scriptedSocket{blockRead: true}
	chunks := make(chan []byte, 2)
	chunks <- []byte{0x01, 0x02}
	chunks <- []byte{0x03, 0x04}
	close(chunks)

	c := silentClient()
	out := make(chan TranscriptEvent)
	go c.streamSession(t.Context(), sock, chunks, out)

	// Once the audio source is drained the writer cancels the session, so the
	// blocked reader unblocks and the output channel closes without leaking.
	if events := collectEvents(t, out); len(events) != 0 {
		t.Errorf("expected no transcript events, got %d", len(events))
	}

	writes := sock.capturedWrites()
	if len(writes) != 2 {
		t.Fatalf("expected 2 audio frames written, got %d", len(writes))
	}
	for i, want := range [][]byte{{0x01, 0x02}, {0x03, 0x04}} {
		if writes[i].MessageType != msgInputAudioChunk {
			t.Errorf("frame %d type = %q, want %q", i, writes[i].MessageType, msgInputAudioChunk)
		}
		if got := writes[i].AudioBase64; got != base64.StdEncoding.EncodeToString(want) {
			t.Errorf("frame %d audio = %q, want base64 of %x", i, got, want)
		}
		if writes[i].Commit {
			t.Errorf("frame %d should not set commit (VAD owns segmentation)", i)
		}
	}
}

func TestStreamSessionStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	sock := &scriptedSocket{blockRead: true}
	chunks := make(chan []byte) // open and never fed

	ctx, cancel := context.WithCancel(t.Context())
	c := silentClient()
	out := make(chan TranscriptEvent)
	go c.streamSession(ctx, sock, chunks, out)

	cancel()

	if events := collectEvents(t, out); len(events) != 0 {
		t.Errorf("expected no events after cancel, got %d", len(events))
	}
	if !sock.closed {
		t.Error("socket was not closed after cancel")
	}
}
