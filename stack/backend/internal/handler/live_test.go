package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// stubLiveAnalyzer satisfies the live port for tests that wire NewMux but never
// open the live socket; its event stream is empty and immediately closed.
type stubLiveAnalyzer struct{}

func (stubLiveAnalyzer) Run(_ context.Context, _ <-chan []byte) (<-chan service.LiveEvent, error) {
	out := make(chan service.LiveEvent)
	close(out)
	return out, nil
}

// recordingLive records every audio frame it receives and, once the first frame
// arrives, emits a fixed sequence of events. Emitting only after a frame lands
// makes the audio assertion deterministic.
type recordingLive struct {
	events []service.LiveEvent

	mu       sync.Mutex
	received [][]byte
}

func (r *recordingLive) Run(ctx context.Context, audio <-chan []byte) (<-chan service.LiveEvent, error) {
	out := make(chan service.LiveEvent)
	go func() {
		defer close(out)
		sent := false
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-audio:
				if !ok {
					return
				}
				r.mu.Lock()
				r.received = append(r.received, frame)
				r.mu.Unlock()
				if sent {
					continue
				}
				sent = true
				for _, ev := range r.events {
					select {
					case <-ctx.Done():
						return
					case out <- ev:
					}
				}
			}
		}
	}()
	return out, nil
}

func (r *recordingLive) frames() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.received...)
}

// wireFrame decodes either event shape; absent fields stay zero.
type wireFrame struct {
	Type        string                `json:"type"`
	ID          string                `json:"id"`
	Start       float64               `json:"start"`
	End         float64               `json:"end"`
	Text        string                `json:"text"`
	Speaker     string                `json:"speaker"`
	Matches     []domain.SegmentMatch `json:"matches"`
	SkipReason  string                `json:"skip_reason"`
	Error       string                `json:"error"`
	EarlierID   string                `json:"earlier_id"`
	EarlierText string                `json:"earlier_text"`
	Rationale   string                `json:"rationale"`
	Claims      []struct {
		ClaimID string `json:"claim_id"`
		Text    string `json:"text"`
		Status  string `json:"status"`
	} `json:"claims"`
	ClaimID    string   `json:"claim_id"`
	Status     string   `json:"status"`
	Source     string   `json:"source"`
	Verdict    string   `json:"verdict"`
	Confidence *float64 `json:"confidence"`
}

func liveTestServer(t *testing.T, analyzer LiveAnalyzer, origins []string) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/videos/{id}/live", liveHandler(analyzer, origins, logger))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/videos/vid1/live"
}

func TestLiveHandlerStreamsAudioAndReturnsEvents(t *testing.T) {
	t.Parallel()

	claim := []domain.SegmentMatch{{
		Kind:       domain.MatchKindClaim,
		Claim:      "the earth is an oblate spheroid",
		Verdict:    domain.Verdict("corroborates"),
		Sources:    []domain.Source{},
		Similarity: 0.9,
	}}
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg},
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, Matches: claim},
	}}
	wsURL := liveTestServer(t, fake, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	subtitle := readFrame(ctx, t, conn)
	if subtitle.Type != "subtitle" || subtitle.ID != "0" || subtitle.Text != seg.Text {
		t.Errorf("subtitle frame = %+v", subtitle)
	}
	if subtitle.Start != 1 || subtitle.End != 2 {
		t.Errorf("subtitle span = [%v,%v], want [1,2]", subtitle.Start, subtitle.End)
	}
	if subtitle.Speaker != "A" {
		t.Errorf("subtitle speaker = %q, want A", subtitle.Speaker)
	}

	result := readFrame(ctx, t, conn)
	if result.Type != "result" || result.ID != "0" {
		t.Errorf("result frame = %+v", result)
	}
	if len(result.Matches) != 1 || result.Matches[0].Claim != claim[0].Claim {
		t.Errorf("result matches = %+v, want the claim hit", result.Matches)
	}

	if frames := fake.frames(); len(frames) != 1 || string(frames[0]) != string([]byte{0x01, 0x02, 0x03}) {
		t.Errorf("server received audio = %v, want one 3-byte frame", frames)
	}
}

func TestLiveHandlerForwardsInterimCaption(t *testing.T) {
	t.Parallel()
	// An interim event is the live caption: it serializes to a text-only frame
	// with no id and no timestamps, distinct from a committed subtitle.
	seg := domain.Segment{Text: "the earth is"}
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventInterim, Segment: seg},
	}}
	wsURL := liveTestServer(t, fake, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	frame := readFrame(ctx, t, conn)
	if frame.Type != "interim" || frame.Text != seg.Text {
		t.Errorf("interim frame = %+v, want type interim with text %q", frame, seg.Text)
	}
	if frame.ID != "" {
		t.Errorf("interim frame should carry no id, got %q", frame.ID)
	}
}

func TestLiveHandlerForwardsConsistencyFlag(t *testing.T) {
	t.Parallel()
	// A consistency event serializes to a frame that links the offending
	// statement to the earlier one it contradicts, so the client can render the
	// inline inconsistency flag.
	seg := domain.Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: "the bridge opened in 1940", Speaker: "A"}
	fake := &recordingLive{events: []service.LiveEvent{
		{
			Kind:    service.LiveEventConsistency,
			ID:      "1",
			Segment: seg,
			Consistency: &service.ConsistencyFlag{
				EarlierID:   "0",
				EarlierText: "the bridge opened in 1937",
				Speaker:     "A",
				Rationale:   "1937 versus 1940 for the same event",
			},
		},
	}}
	wsURL := liveTestServer(t, fake, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	frame := readFrame(ctx, t, conn)
	if frame.Type != "consistency" || frame.ID != "1" {
		t.Errorf("consistency frame = %+v, want type consistency on statement 1", frame)
	}
	if frame.EarlierID != "0" || frame.EarlierText != "the bridge opened in 1937" {
		t.Errorf("frame should link to the earlier statement, got earlier_id=%q earlier_text=%q", frame.EarlierID, frame.EarlierText)
	}
	if frame.Speaker != "A" || frame.Rationale == "" {
		t.Errorf("frame speaker=%q rationale=%q", frame.Speaker, frame.Rationale)
	}
}

func TestLiveHandlerForwardsClaimsAndPerClaimResults(t *testing.T) {
	t.Parallel()
	// The retrieve-then-verify events serialize to a claims frame (atomic claims,
	// each pending) followed by per-claim result frames keyed on claim_id so the
	// client replaces a claim's row in place as it goes checking -> verified.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the moon is made of cheese", Speaker: "A"}
	cite := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", Similarity: 0.7, EvidenceID: "evidence:42:0"}
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventClaims, ID: "0", Segment: seg, Claims: []service.AtomicClaim{{ClaimID: "0-0", Text: "the moon is made of cheese."}}},
		{Kind: service.LiveEventResult, ID: "0-0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusChecking},
		{Kind: service.LiveEventResult, ID: "0-0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{Verdict: "refutes", Confidence: 0.9, Citations: []domain.SegmentMatch{cite}, Rationale: "rock, not cheese"}},
	}}
	wsURL := liveTestServer(t, fake, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	claims := readFrame(ctx, t, conn)
	if claims.Type != "claims" || claims.ID != "0" || len(claims.Claims) != 1 {
		t.Fatalf("claims frame = %+v, want one pending claim", claims)
	}
	if claims.Claims[0].ClaimID != "0-0" || claims.Claims[0].Status != "pending" {
		t.Errorf("claim = %+v, want claim_id 0-0 pending", claims.Claims[0])
	}

	checking := readFrame(ctx, t, conn)
	if checking.Type != "claim_result" || checking.ClaimID != "0-0" || checking.Status != "checking" {
		t.Fatalf("checking frame = %+v, want claim_result claim_id 0-0 checking", checking)
	}

	verified := readFrame(ctx, t, conn)
	if verified.Type != "claim_result" || verified.ClaimID != "0-0" || verified.Status != "verified" || verified.Source != "verified" {
		t.Fatalf("verified frame = %+v, want claim_result 0-0 verified/verified", verified)
	}
	if verified.Verdict != "refutes" || verified.Confidence == nil || *verified.Confidence != 0.9 {
		t.Errorf("verdict=%q confidence=%v, want refutes/0.9", verified.Verdict, verified.Confidence)
	}
	if len(verified.Matches) != 1 || verified.Matches[0].EvidenceID != "evidence:42:0" {
		t.Errorf("citations did not round-trip: %+v", verified.Matches)
	}
}

func TestLiveHandlerSkipsMalformedConsistencyEvent(t *testing.T) {
	t.Parallel()
	// A consistency event with no flag payload must not panic or emit a bogus
	// frame: it is skipped, and the following event still reaches the client.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventConsistency, ID: "1"}, // Consistency is nil
		{Kind: service.LiveEventSubtitle, ID: "2", Segment: seg},
	}}
	wsURL := liveTestServer(t, fake, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	// The malformed consistency event is dropped, so the very next frame is the
	// subtitle - the session survived the bad event.
	frame := readFrame(ctx, t, conn)
	if frame.Type != "subtitle" || frame.ID != "2" {
		t.Errorf("expected the subtitle after a skipped consistency event, got %+v", frame)
	}
}

func TestLiveHandlerAcceptsAllowlistedCrossOrigin(t *testing.T) {
	t.Parallel()
	// Behind the dev frontend proxy the browser Origin (localhost:3000) differs
	// from the backend Host, so the upgrade is cross-origin to the server. An
	// allow-listed origin must still be accepted - this is the handshake dev's
	// CORS_ALLOWED_ORIGIN enables, distinct from the same-Host happy path.
	wsURL := liveTestServer(t, stubLiveAnalyzer{}, []string{"localhost:3000"})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://localhost:3000"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("allow-listed cross-origin handshake rejected: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
}

func TestLiveHandlerRejectsDisallowedOrigin(t *testing.T) {
	t.Parallel()

	wsURL := liveTestServer(t, stubLiveAnalyzer{}, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://evil.example"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("expected cross-origin handshake to be rejected")
	}
}

func TestLiveRouteRequiresAuth(t *testing.T) {
	t.Parallel()
	// The live route sits under the /api guard, so an unauthenticated upgrade is
	// rejected with 401 before any WebSocket handshake.
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid1/live", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET live without session = %d, want 401", rec.Code)
	}
}

func acceptAndServe(t *testing.T, serve func(ctx context.Context, c *websocket.Conn)) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		serve(r.Context(), c)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func dialWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func TestPingLoopCancelsOnDeadPeer(t *testing.T) {
	t.Parallel()
	// The peer vanishes the moment it accepts, so the keepalive ping goes
	// unanswered and the loop must cancel the session rather than block forever.
	wsURL := acceptAndServe(t, func(_ context.Context, c *websocket.Conn) {
		_ = c.CloseNow()
	})
	conn := dialWS(t, wsURL)
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go pingLoop(ctx, cancel, conn, 10*time.Millisecond, time.Second)

	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("pingLoop did not cancel on a dead peer")
	}
}

func TestPingLoopExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	// A live peer answers pings (its Read loop auto-pongs), so the loop survives
	// until the session context is canceled, then it must return.
	wsURL := acceptAndServe(t, func(ctx context.Context, c *websocket.Conn) {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	})
	conn := dialWS(t, wsURL)
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		pingLoop(ctx, cancel, conn, 10*time.Millisecond, time.Second)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pingLoop did not exit on context cancel")
	}
}

func readFrame(ctx context.Context, t *testing.T, conn *websocket.Conn) wireFrame {
	t.Helper()
	var frame wireFrame
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return frame
}
