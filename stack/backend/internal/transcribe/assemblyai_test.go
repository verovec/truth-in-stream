package transcribe

import (
	"context"
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

func silentAssemblyAI() *AssemblyAIClient {
	return NewAssemblyAI(AssemblyAIConfig{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestAssemblyAIURL(t *testing.T) {
	t.Parallel()

	got, err := assemblyAIURL("wss://streaming.example.test/v3/ws", "u3-rt-pro", 3, "fr")
	if err != nil {
		t.Fatalf("assemblyAIURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if u.Scheme != "wss" || u.Host != "streaming.example.test" || u.Path != "/v3/ws" {
		t.Fatalf("unexpected base: %s", got)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"speech_model":     "u3-rt-pro",
		"sample_rate":      "16000",
		"encoding":         "pcm_s16le",
		"speaker_labels":   "true",
		"max_speakers":     "3",
		"language_code":    "fr",
		"max_turn_silence": "500",
	} {
		if q.Get(key) != want {
			t.Errorf("query %q = %q, want %q", key, q.Get(key), want)
		}
	}
	// model_region is not part of the v3 streaming contract; region is pinned by
	// the host, so the global edge-routed default must not gain a fabricated param.
	if _, ok := q["model_region"]; ok {
		t.Errorf("model_region must not be sent: %s", got)
	}
	// The deprecated "language" key must not be used in place of language_code.
	if _, ok := q["language"]; ok {
		t.Errorf("deprecated language key present, want language_code: %s", got)
	}
}

func TestAssemblyAIURLOmitsUnsetOptions(t *testing.T) {
	t.Parallel()

	got, err := assemblyAIURL("wss://streaming.example.test/v3/ws", "u3-rt-pro", 0, "")
	if err != nil {
		t.Fatalf("assemblyAIURL: %v", err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if _, ok := q["max_speakers"]; ok {
		t.Errorf("max_speakers present for zero value: %s", got)
	}
	if _, ok := q["language_code"]; ok {
		t.Errorf("language_code present for empty value: %s", got)
	}
	// speaker_labels is always on: diarization is the reason this provider exists.
	if q.Get("speaker_labels") != "true" {
		t.Errorf("speaker_labels = %q, want true", q.Get("speaker_labels"))
	}
}

func TestAAISegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  aaiMessage
		want []Segment
	}{
		{
			name: "uniform speaker keeps the formatted transcript as one segment",
			msg: aaiMessage{
				Transcript:   "the earth is round",
				SpeakerLabel: "A",
				Words: []aaiWord{
					{Text: "the", Start: 1000, End: 1200, Speaker: "A"},
					{Text: "earth", Start: 1200, End: 1600, Speaker: "A"},
					{Text: "round", Start: 1600, End: 2000, Speaker: "A"},
				},
			},
			want: []Segment{{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}},
		},
		{
			name: "speaker change splits the turn into one segment per run",
			msg: aaiMessage{
				Transcript:   "i agree no you are wrong",
				SpeakerLabel: "A",
				Words: []aaiWord{
					{Text: "i", Start: 1000, End: 1200, Speaker: "A"},
					{Text: "agree", Start: 1200, End: 1600, Speaker: "A"},
					{Text: "no", Start: 2000, End: 2200, Speaker: "B"},
					{Text: "you", Start: 2200, End: 2500, Speaker: "B"},
					{Text: "are", Start: 2500, End: 2800, Speaker: "B"},
					{Text: "wrong", Start: 2800, End: 3000, Speaker: "B"},
				},
			},
			want: []Segment{
				{Start: time.Second, End: 1600 * time.Millisecond, Text: "i agree", Speaker: "A"},
				{Start: 2 * time.Second, End: 3 * time.Second, Text: "no you are wrong", Speaker: "B"},
			},
		},
		{
			name: "an empty mid-run speaker continues the current run, no split",
			msg: aaiMessage{
				Transcript:   "the earth is round",
				SpeakerLabel: "A",
				Words: []aaiWord{
					{Text: "the", Start: 1000, End: 1200, Speaker: "A"},
					{Text: "earth", Start: 1200, End: 1600, Speaker: ""},
					{Text: "round", Start: 1600, End: 2000, Speaker: "A"},
				},
			},
			want: []Segment{{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}},
		},
		{
			name: "no words leaves a text-only segment with the turn speaker",
			msg:  aaiMessage{Transcript: "no timestamps", SpeakerLabel: "B"},
			want: []Segment{{Text: "no timestamps", Speaker: "B"}},
		},
		{
			name: "trailing zero end is clamped to start, never inverted",
			msg: aaiMessage{
				Transcript:   "uh",
				SpeakerLabel: "A",
				Words:        []aaiWord{{Text: "uh", Start: 1500, End: 0}},
			},
			want: []Segment{{Start: 1500 * time.Millisecond, End: 1500 * time.Millisecond, Text: "uh", Speaker: "A"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, aaiSegments(tc.msg)); diff != "" {
				t.Errorf("aaiSegments() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAAIClassify(t *testing.T) {
	t.Parallel()

	c := silentAssemblyAI()
	ctx := t.Context()
	tests := []struct {
		name       string
		msg        aaiMessage
		wantEvents []TranscriptEvent
		wantEnd    bool
	}{
		{
			name:       "in-progress turn emits one non-final event with the turn speaker",
			msg:        aaiMessage{Type: aaiMsgTurn, Transcript: "the earth", SpeakerLabel: "A", EndOfTurn: false},
			wantEvents: []TranscriptEvent{{Segment: Segment{Text: "the earth", Speaker: "A"}, Final: false}},
		},
		{
			name: "empty turn is absorbed",
			msg:  aaiMessage{Type: aaiMsgTurn, Transcript: "", EndOfTurn: true},
		},
		{
			name: "uniform committed turn emits one final event",
			msg: aaiMessage{
				Type:         aaiMsgTurn,
				Transcript:   "the earth is round",
				SpeakerLabel: "A",
				EndOfTurn:    true,
				Words:        []aaiWord{{Text: "the", Start: 1000, End: 2000, Speaker: "A"}},
			},
			wantEvents: []TranscriptEvent{{Segment: Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}, Final: true}},
		},
		{
			name: "mixed committed turn emits one final event per speaker run",
			msg: aaiMessage{
				Type:         aaiMsgTurn,
				Transcript:   "i agree no wrong",
				SpeakerLabel: "A",
				EndOfTurn:    true,
				Words: []aaiWord{
					{Text: "i", Start: 1000, End: 1200, Speaker: "A"},
					{Text: "agree", Start: 1200, End: 1600, Speaker: "A"},
					{Text: "no", Start: 2000, End: 2200, Speaker: "B"},
					{Text: "wrong", Start: 2200, End: 2600, Speaker: "B"},
				},
			},
			wantEvents: []TranscriptEvent{
				{Segment: Segment{Start: time.Second, End: 1600 * time.Millisecond, Text: "i agree", Speaker: "A"}, Final: true},
				{Segment: Segment{Start: 2 * time.Second, End: 2600 * time.Millisecond, Text: "no wrong", Speaker: "B"}, Final: true},
			},
		},
		{
			name: "begin is absorbed",
			msg:  aaiMessage{Type: aaiMsgBegin},
		},
		{
			name:    "termination ends the stream",
			msg:     aaiMessage{Type: aaiMsgTermination},
			wantEnd: true,
		},
		{
			name:    "error ends the stream and is surfaced",
			msg:     aaiMessage{Type: aaiMsgError, ErrorCode: 3007, Error: "Audio chunk duration violation"},
			wantEnd: true,
		},
		{
			name: "unknown message is absorbed",
			msg:  aaiMessage{Type: "SomethingNew", Transcript: "ignored"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events, end := c.classify(ctx, tc.msg)
			if end != tc.wantEnd {
				t.Fatalf("classify() end=%v, want %v", end, tc.wantEnd)
			}
			if diff := cmp.Diff(tc.wantEvents, events); diff != "" {
				t.Errorf("classify() events mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// scriptedAAISocket is a faked aaiSocket: it returns a fixed sequence of server
// messages, then either a final error or blocks until ctx is done, capturing
// every binary audio frame written.
type scriptedAAISocket struct {
	msgs      []aaiMessage
	finalErr  error
	blockRead bool

	mu     sync.Mutex
	idx    int
	binary [][]byte
	closed bool
}

func (s *scriptedAAISocket) writeBinary(_ context.Context, data []byte) error {
	s.mu.Lock()
	s.binary = append(s.binary, append([]byte(nil), data...))
	s.mu.Unlock()
	return nil
}

func (s *scriptedAAISocket) readJSON(ctx context.Context, v any) error {
	s.mu.Lock()
	if s.idx < len(s.msgs) {
		m := s.msgs[s.idx]
		s.idx++
		s.mu.Unlock()
		*v.(*aaiMessage) = m
		return nil
	}
	s.mu.Unlock()
	if s.blockRead {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.finalErr
}

func (s *scriptedAAISocket) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *scriptedAAISocket) capturedBinary() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.binary...)
}

func TestAAIStreamSessionEmitsUntilServerClose(t *testing.T) {
	t.Parallel()

	sock := &scriptedAAISocket{
		msgs: []aaiMessage{
			{Type: aaiMsgBegin},
			{Type: aaiMsgTurn, Transcript: "the earth", SpeakerLabel: "A", EndOfTurn: false},
			{
				Type:         aaiMsgTurn,
				Transcript:   "the earth is round",
				SpeakerLabel: "A",
				EndOfTurn:    true,
				Words:        []aaiWord{{Text: "the", Start: 1000, End: 2000, Speaker: "A"}},
			},
		},
		finalErr: websocket.CloseError{Code: websocket.StatusNormalClosure},
	}
	chunks := make(chan []byte) // never fed, never closed; reader ends the session

	c := silentAssemblyAI()
	out := make(chan TranscriptEvent)
	go c.streamSession(t.Context(), sock, chunks, out)

	events := collectEvents(t, out)
	want := []TranscriptEvent{
		{Segment: Segment{Text: "the earth", Speaker: "A"}, Final: false},
		{Segment: Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}, Final: true},
	}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
	if !sock.closed {
		t.Error("socket was not closed")
	}
}

// maxAAIChunkBytes is AssemblyAI streaming v3's per-frame upper bound (1000 ms);
// a frame above it is rejected with WebSocket close code 3007. It is derived
// from the production bytes-per-ms so it cannot drift if the sample rate changes.
// The lower bound is the production constant assemblyAIMinChunkBytes. The client
// must keep every coalesced frame inside [assemblyAIMinChunkBytes, maxAAIChunkBytes].
const maxAAIChunkBytes = 1000 * assemblyAIBytesPerMilli

func TestAAIStreamSessionCoalescesAudioToProviderChunkSize(t *testing.T) {
	t.Parallel()

	// The live pipeline emits tiny frames (~3 ms each), each below the 50 ms
	// minimum on its own. Feed 8000 bytes (250 ms) as 100 80-byte frames so the
	// writer must emit multiple full frames and flush a >= 50 ms tail.
	const frameBytes, frameCount = 80, 100
	sock := &scriptedAAISocket{blockRead: true}
	chunks := make(chan []byte, frameCount)
	sent := make([]byte, 0, frameBytes*frameCount)
	for i := range frameCount {
		frame := make([]byte, frameBytes)
		for j := range frame {
			frame[j] = byte(i)
		}
		sent = append(sent, frame...)
		chunks <- frame
	}
	close(chunks)

	c := silentAssemblyAI()
	out := make(chan TranscriptEvent)
	go c.streamSession(t.Context(), sock, chunks, out)

	if events := collectEvents(t, out); len(events) != 0 {
		t.Errorf("expected no transcript events, got %d", len(events))
	}

	binary := sock.capturedBinary()
	if len(binary) < 2 {
		t.Fatalf("expected multiple coalesced frames, got %d", len(binary))
	}
	var got []byte
	for i, frame := range binary {
		if len(frame) < assemblyAIMinChunkBytes || len(frame) > maxAAIChunkBytes {
			t.Errorf("frame %d is %d bytes, want within [%d, %d] (50-1000 ms)", i, len(frame), assemblyAIMinChunkBytes, maxAAIChunkBytes)
		}
		got = append(got, frame...)
	}
	// Coalescing must preserve the audio bytes and their order exactly.
	if diff := cmp.Diff(sent, got); diff != "" {
		t.Errorf("coalesced audio mismatch (-want +got):\n%s", diff)
	}
}

func TestAAIStreamSessionSplitsOversizedChunk(t *testing.T) {
	t.Parallel()

	// A single inbound chunk larger than the 1000 ms ceiling must be split so no
	// emitted frame re-trips 3007. Feed 6400 bytes (200 ms) as one chunk.
	sock := &scriptedAAISocket{blockRead: true}
	chunks := make(chan []byte, 1)
	sent := make([]byte, 2*assemblyAIChunkBytes)
	for i := range sent {
		sent[i] = byte(i)
	}
	chunks <- sent
	close(chunks)

	c := silentAssemblyAI()
	out := make(chan TranscriptEvent)
	go c.streamSession(t.Context(), sock, chunks, out)

	if events := collectEvents(t, out); len(events) != 0 {
		t.Errorf("expected no transcript events, got %d", len(events))
	}

	binary := sock.capturedBinary()
	if len(binary) < 2 {
		t.Fatalf("expected the oversized chunk split into multiple frames, got %d", len(binary))
	}
	var got []byte
	for i, frame := range binary {
		if len(frame) < assemblyAIMinChunkBytes || len(frame) > maxAAIChunkBytes {
			t.Errorf("frame %d is %d bytes, want within [%d, %d]", i, len(frame), assemblyAIMinChunkBytes, maxAAIChunkBytes)
		}
		got = append(got, frame...)
	}
	if diff := cmp.Diff(sent, got); diff != "" {
		t.Errorf("split audio mismatch (-want +got):\n%s", diff)
	}
}

func TestAAIStreamSessionDropsSubMinimumTailOnClose(t *testing.T) {
	t.Parallel()

	// A trailing buffer below the 50 ms minimum cannot be sent without tripping
	// 3007, so it is dropped when the audio source closes.
	sock := &scriptedAAISocket{blockRead: true}
	chunks := make(chan []byte, 1)
	chunks <- make([]byte, assemblyAIMinChunkBytes-32) // just under 50 ms
	close(chunks)

	c := silentAssemblyAI()
	out := make(chan TranscriptEvent)
	go c.streamSession(t.Context(), sock, chunks, out)

	if events := collectEvents(t, out); len(events) != 0 {
		t.Errorf("expected no transcript events, got %d", len(events))
	}
	if binary := sock.capturedBinary(); len(binary) != 0 {
		t.Errorf("expected the sub-minimum tail to be dropped, got %d frame(s)", len(binary))
	}
}

func TestAAIStreamSessionStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	sock := &scriptedAAISocket{blockRead: true}
	chunks := make(chan []byte) // open and never fed

	ctx, cancel := context.WithCancel(t.Context())
	c := silentAssemblyAI()
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

func TestAssemblyAITranscribeStreamReadsOversizedTurn(t *testing.T) {
	t.Parallel()
	// A committed Turn for a long utterance exceeds coder/websocket's 32 KiB
	// default read limit. Without raising the limit the read fails fatally and
	// the live session dies mid-stream; the client must read it and emit it.
	bigText := strings.Repeat("alpha ", 10000) // ~60 KiB, over the 32 KiB default
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if err := wsjson.Write(r.Context(), conn, map[string]any{
			"type":        aaiMsgTurn,
			"transcript":  bigText,
			"end_of_turn": true,
		}); err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)

	client := NewAssemblyAI(AssemblyAIConfig{
		APIKey: "k",
		URL:    "ws" + strings.TrimPrefix(srv.URL, "http"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
			t.Errorf("got final=%v text-len=%d, want a final committed turn carrying the full text", ev.Final, len(ev.Segment.Text))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the committed turn event")
	}
}

func TestAssemblyAITranscribeStreamFrenchDiarized(t *testing.T) {
	t.Parallel()
	// End-to-end over a real WebSocket: a French source dials with the French
	// language bias and diarization on, and a French committed Turn flows back as
	// a French diarized segment - the offline stand-in for "a French audio source
	// produces French diarized subtitles".
	const frenchTurn = "le chomage a baisse de deux points"
	gotQuery := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery <- r.URL.Query()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if err := wsjson.Write(r.Context(), conn, map[string]any{
			"type":          aaiMsgTurn,
			"transcript":    frenchTurn,
			"speaker_label": "A",
			"end_of_turn":   true,
			"words": []map[string]any{
				{"text": "le", "start": 1000, "end": 2000, "speaker": "A"},
			},
		}); err != nil {
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)

	client := NewAssemblyAI(AssemblyAIConfig{
		APIKey: "k",
		URL:    "ws" + strings.TrimPrefix(srv.URL, "http"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events, err := client.TranscribeStream(ctx, make(chan []byte), Options{Language: "fr"})
	if err != nil {
		t.Fatalf("TranscribeStream: %v", err)
	}

	select {
	case q := <-gotQuery:
		if q.Get("language_code") != "fr" {
			t.Errorf("language_code = %q, want fr", q.Get("language_code"))
		}
		if q.Get("speaker_labels") != "true" {
			t.Errorf("speaker_labels = %q, want true", q.Get("speaker_labels"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the dial query")
	}

	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("event channel closed with no event")
		}
		if !ev.Final || ev.Segment.Text != frenchTurn || ev.Segment.Speaker != "A" {
			t.Errorf("got final=%v text=%q speaker=%q, want a final French diarized turn", ev.Final, ev.Segment.Text, ev.Segment.Speaker)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the French committed turn event")
	}
}

// collectEvents drains the event channel until it closes, failing if it does
// not within a bounded window so a stuck stream surfaces as a test failure
// rather than a hang.
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
