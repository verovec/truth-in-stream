package handler

import (
	"context"
	"errors"
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
	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
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
		ClaimID string             `json:"claim_id"`
		Text    string             `json:"text"`
		Status  string             `json:"status"`
		Quote   string             `json:"quote"`
		Spans   []domain.ClaimSpan `json:"spans"`
	} `json:"claims"`
	ClaimID     string   `json:"claim_id"`
	Status      string   `json:"status"`
	Source      string   `json:"source"`
	SourceLabel string   `json:"source_label"`
	SourceURL   string   `json:"source_url"`
	Verdict     string   `json:"verdict"`
	Basis       string   `json:"basis"`
	Literal     string   `json:"literal"`
	Flags       []string `json:"flags"`
	Confidence  *float64 `json:"confidence"`
	// Speaker-tally frame fields.
	Credible          int `json:"credible"`
	Disputed          int `json:"disputed"`
	Unverifiable      int `json:"unverifiable"`
	MisleadingFraming int `json:"misleading_framing"`
}

func liveTestServer(t *testing.T, analyzer LiveAnalyzer, origins []string, debugFactCheck bool) string {
	t.Helper()
	return liveTestServerWithRecorder(t, analyzer, nil, origins, debugFactCheck)
}

func liveTestServerWithRecorder(t *testing.T, analyzer LiveAnalyzer, recorder AnalysisRecorder, origins []string, debugFactCheck bool) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	// This harness wraps the handler in the permissive Identity middleware to test
	// the handler's role-reading logic in isolation: a dial with no Bearer token is
	// a guest (debug detail stays off even when the server-side flag is on), and the
	// admin Bearer the stub verifier knows unlocks the evidence detail. The real
	// production gate - RequireIdentity, which rejects a no-token dial with 401 and
	// reads the access_token query parameter - is exercised separately through
	// NewMux by TestLiveWSAdminQueryParamUnlocksDebugDetail.
	h := middleware.Identity(stubVerifier{})(liveHandler(analyzer, recorder, nil, origins, debugFactCheck, logger))
	mux.Handle("GET /api/videos/{id}/live", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/videos/vid1/live"
}

// recordingPersister captures Persist calls so the handler's completion gate can
// be asserted: it records the video id and events on each call, and a buffered
// channel lets a test wait for the post-session write rather than poll-and-sleep.
type recordingPersister struct {
	persistErr error

	mu       sync.Mutex
	calls    int
	videoID  string
	events   []service.LiveEvent
	notified chan struct{}
}

func newRecordingPersister() *recordingPersister {
	return &recordingPersister{notified: make(chan struct{}, 1)}
}

func (p *recordingPersister) Persist(_ context.Context, videoID string, events []service.LiveEvent) error {
	p.mu.Lock()
	p.calls++
	p.videoID = videoID
	p.events = append([]service.LiveEvent(nil), events...)
	p.mu.Unlock()
	select {
	case p.notified <- struct{}{}:
	default:
	}
	return p.persistErr
}

func (p *recordingPersister) snapshot() (int, string, []service.LiveEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.videoID, append([]service.LiveEvent(nil), p.events...)
}

// awaitPersist waits for one Persist call or fails the test on timeout.
func (p *recordingPersister) awaitPersist(t *testing.T) {
	t.Helper()
	select {
	case <-p.notified:
	case <-time.After(3 * time.Second):
		t.Fatal("expected a snapshot persist call, none arrived")
	}
}

// adminDialOptions carries the admin Bearer header the stub verifier recognizes,
// letting a live-socket dial present as an admin so the debug evidence detail is
// emitted.
func adminDialOptions() *websocket.DialOptions {
	return &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + testAdminToken}}}
}

// countingLive records whether Run was invoked so a replay test can prove the
// live pipeline (transcriber + analyzer) was never constructed on a cache hit. Its
// event stream is empty and immediately closed, so on the miss/error paths the
// session completes cleanly.
type countingLive struct {
	mu   sync.Mutex
	runs int
}

func (c *countingLive) Run(ctx context.Context, audio <-chan []byte) (<-chan service.LiveEvent, error) {
	c.mu.Lock()
	c.runs++
	c.mu.Unlock()
	// Emit one subtitle as soon as the first audio frame lands, then drain the rest.
	// The emitted frame lets a test read-synchronize on the live pipeline actually
	// running (proving the replay path was skipped) before asserting the run count.
	out := make(chan service.LiveEvent)
	go func() {
		defer close(out)
		sent := false
		for range audio {
			if sent {
				continue
			}
			sent = true
			select {
			case <-ctx.Done():
				return
			case out <- service.LiveEvent{Kind: service.LiveEventSubtitle, ID: "live", Segment: domain.Segment{Text: "live"}}:
			}
		}
	}()
	return out, nil
}

func (c *countingLive) runCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs
}

// stubReplayer serves a canned snapshot lookup so the handler's cache-hit replay
// path can be driven without a real cache. It records each lookup so a test can
// confirm the cache was consulted at session open.
type stubReplayer struct {
	events []service.LiveEvent
	found  bool
	err    error

	mu     sync.Mutex
	calls  int
	lookup string
}

func (r *stubReplayer) Snapshot(_ context.Context, videoID string) ([]service.LiveEvent, bool, error) {
	r.mu.Lock()
	r.calls++
	r.lookup = videoID
	r.mu.Unlock()
	if r.err != nil {
		return nil, false, r.err
	}
	return r.events, r.found, nil
}

func (r *stubReplayer) snapshot() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.lookup
}

func liveTestServerWithReplayer(t *testing.T, analyzer LiveAnalyzer, replayer AnalysisReplayer) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mux := http.NewServeMux()
	h := middleware.Identity(stubVerifier{})(liveHandler(analyzer, nil, replayer, nil, false, logger))
	mux.Handle("GET /api/videos/{id}/live", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/videos/vid1/live"
}

func TestLiveHandlerReplaysCachedSnapshotWithoutPipeline(t *testing.T) {
	t.Parallel()
	// A finite video with a complete cached analysis: opening it replays every
	// stored event to the client in order, through the same serializer the live
	// path uses, and never constructs the live pipeline (the analyzer's Run is not
	// called).
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	claim := []domain.SegmentMatch{{Kind: domain.MatchKindClaim, Claim: "earth shape", Verdict: domain.Verdict("corroborates"), Sources: []domain.Source{}, Similarity: 0.9}}
	events := []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg},
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, Matches: claim},
	}
	live := &countingLive{}
	replayer := &stubReplayer{events: events, found: true}
	wsURL := liveTestServerWithReplayer(t, live, replayer)

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

	subtitle := readFrame(ctx, t, conn)
	if subtitle.Type != "subtitle" || subtitle.ID != "0" || subtitle.Text != seg.Text {
		t.Errorf("subtitle frame = %+v", subtitle)
	}
	if subtitle.Start != 1 || subtitle.End != 2 || subtitle.Speaker != "A" {
		t.Errorf("subtitle span/speaker = [%v,%v]/%q", subtitle.Start, subtitle.End, subtitle.Speaker)
	}
	result := readFrame(ctx, t, conn)
	if result.Type != "result" || result.ID != "0" {
		t.Errorf("result frame = %+v", result)
	}
	if len(result.Matches) != 1 || result.Matches[0].Claim != claim[0].Claim {
		t.Errorf("result matches = %+v, want the cached claim hit", result.Matches)
	}

	// The server closes the socket cleanly once the snapshot is drained; the next
	// read returns a normal-closure error.
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("after replay the socket should close normally, got %v", err)
	}

	if live.runCount() != 0 {
		t.Errorf("analyzer Run was called %d times on a cache hit, want 0", live.runCount())
	}
	if calls, lookup := replayer.snapshot(); calls != 1 || lookup != "vid1" {
		t.Errorf("replayer lookups = %d for %q, want 1 for vid1", calls, lookup)
	}
}

func TestLiveHandlerReplaysFullEventSequence(t *testing.T) {
	t.Parallel()
	// The replay emits the exact stored sequence, in order, across the full event
	// vocabulary - subtitle, interim, claims, per-claim result, consistency, and
	// speaker score - so a cache-served session reproduces the live one frame for
	// frame.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the moon is made of cheese", Speaker: "A"}
	events := []service.LiveEvent{
		{Kind: service.LiveEventInterim, Segment: domain.Segment{Text: "the moon"}},
		{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg},
		{Kind: service.LiveEventClaims, ID: "0", Segment: seg, Claims: []service.AtomicClaim{{ClaimID: "0-0", Text: "the moon is made of cheese."}}},
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusChecking},
		{Kind: service.LiveEventConsistency, ID: "1", Segment: seg, Consistency: &service.ConsistencyFlag{EarlierID: "0", EarlierText: "the moon is rock", Speaker: "A", Rationale: "contradiction"}},
		{Kind: service.LiveEventSpeakerTally, SpeakerTally: &service.SpeakerTally{Speaker: "A", Credible: 1, Unverifiable: 2}},
	}
	live := &countingLive{}
	replayer := &stubReplayer{events: events, found: true}
	wsURL := liveTestServerWithReplayer(t, live, replayer)

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

	wantTypes := []string{"interim", "subtitle", "claims", "claim_result", "consistency", "speaker_tally"}
	for i, want := range wantTypes {
		frame := readFrame(ctx, t, conn)
		if frame.Type != want {
			t.Fatalf("frame %d type = %q, want %q", i, frame.Type, want)
		}
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("after replay the socket should close normally, got %v", err)
	}
	if live.runCount() != 0 {
		t.Errorf("analyzer Run was called %d times on a cache hit, want 0", live.runCount())
	}
}

func TestLiveHandlerCacheMissRunsLivePipeline(t *testing.T) {
	t.Parallel()
	// On a cache miss the handler falls through to the live pipeline: the analyzer
	// is constructed and driven exactly as before, replaying nothing.
	live := &countingLive{}
	replayer := &stubReplayer{found: false}
	wsURL := liveTestServerWithReplayer(t, live, replayer)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	// On a miss the live pipeline runs as usual: it emits its live subtitle, which
	// the client reads - proving the analyzer was driven and the replay path skipped.
	frame := readFrame(ctx, t, conn)
	if frame.Type != "subtitle" || frame.ID != "live" {
		t.Fatalf("frame = %+v, want the live pipeline's subtitle", frame)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("clean close: %v", err)
	}
	if live.runCount() != 1 {
		t.Errorf("analyzer Run was called %d times on a miss, want 1", live.runCount())
	}
	if calls, _ := replayer.snapshot(); calls != 1 {
		t.Errorf("replayer lookups = %d, want 1", calls)
	}
}

func TestLiveHandlerNilReplayerRunsLivePipeline(t *testing.T) {
	t.Parallel()
	// With no replayer wired, the cache-hit path is disabled and the live route
	// behaves exactly as before: the analyzer always runs.
	live := &countingLive{}
	wsURL := liveTestServerWithReplayer(t, live, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	frame := readFrame(ctx, t, conn)
	if frame.Type != "subtitle" || frame.ID != "live" {
		t.Fatalf("frame = %+v, want the live pipeline's subtitle", frame)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("clean close: %v", err)
	}
	if live.runCount() != 1 {
		t.Errorf("analyzer Run was called %d times with a nil replayer, want 1", live.runCount())
	}
}

func TestLiveHandlerPersistsSnapshotOnCleanCompletion(t *testing.T) {
	t.Parallel()
	// A finite video analyzed to completion: the client streams audio, reads its
	// events, then closes the socket cleanly (StatusNormalClosure - its audio
	// reached EOF). The handler tees the events and persists exactly one snapshot
	// under the video id with the same ordered events it sent the client.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	events := []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg},
		{Kind: service.LiveEventResult, ID: "0", Segment: seg},
	}
	fake := &recordingLive{events: events}
	rec := newRecordingPersister()
	wsURL := liveTestServerWithRecorder(t, fake, rec, nil, false)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	// Drain both events, then close cleanly to signal the audio reached its end.
	_ = readFrame(ctx, t, conn)
	_ = readFrame(ctx, t, conn)
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("clean close: %v", err)
	}

	rec.awaitPersist(t)
	calls, videoID, got := rec.snapshot()
	if calls != 1 {
		t.Fatalf("persist calls = %d, want exactly 1", calls)
	}
	if videoID != "vid1" {
		t.Errorf("video id = %q, want vid1", videoID)
	}
	if diff := cmp.Diff(events, got); diff != "" {
		t.Errorf("persisted events (-want +got):\n%s", diff)
	}
}

func TestLiveHandlerPersistsNothingOnEarlyDisconnect(t *testing.T) {
	t.Parallel()
	// A viewer who leaves before completion: the client aborts the socket
	// (CloseNow - no close frame) instead of closing cleanly. The audio reader
	// never sees a normal closure, so the session is not a completion and nothing
	// is persisted, even though the pipeline drained.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	fake := &recordingLive{events: []service.LiveEvent{{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg}}}
	rec := newRecordingPersister()
	wsURL := liveTestServerWithRecorder(t, fake, rec, nil, false)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	_ = readFrame(ctx, t, conn)
	// Abort without a close handshake - the dropped-viewer / live-stream shape.
	_ = conn.CloseNow()

	select {
	case <-rec.notified:
		t.Fatal("a snapshot was persisted on an early disconnect")
	case <-time.After(500 * time.Millisecond):
	}
	if calls, _, _ := rec.snapshot(); calls != 0 {
		t.Fatalf("persist calls = %d, want 0 on disconnect", calls)
	}
}

func TestLiveHandlerCacheWriteFailureDoesNotAffectSession(t *testing.T) {
	t.Parallel()
	// A cache write failure is logged, never surfaced: the client's session
	// completes normally (it received all its events and a clean close) regardless
	// of the persist error, which the recorder returns.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the earth is round", Speaker: "A"}
	events := []service.LiveEvent{{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg}}
	fake := &recordingLive{events: events}
	rec := newRecordingPersister()
	rec.persistErr = errors.New("redis down")
	wsURL := liveTestServerWithRecorder(t, fake, rec, nil, false)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	frame := readFrame(ctx, t, conn)
	if frame.Type != "subtitle" || frame.ID != "0" {
		t.Fatalf("client did not receive its event before the failing persist: %+v", frame)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("clean close: %v", err)
	}

	// The persist was attempted (and failed) but the session was unaffected.
	rec.awaitPersist(t)
	if calls, _, _ := rec.snapshot(); calls != 1 {
		t.Fatalf("persist calls = %d, want 1 (attempted despite the error)", calls)
	}
}

func TestLiveHandlerNilRecorderIsSafe(t *testing.T) {
	t.Parallel()
	// With no recorder wired, capture is disabled and the live route works
	// unchanged: a clean completion simply persists nothing and does not panic.
	seg := domain.Segment{Text: "the earth is round"}
	fake := &recordingLive{events: []service.LiveEvent{{Kind: service.LiveEventSubtitle, ID: "0", Segment: seg}}}
	wsURL := liveTestServerWithRecorder(t, fake, nil, nil, false)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	_ = readFrame(ctx, t, conn)
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("clean close: %v", err)
	}
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
	wsURL := liveTestServer(t, fake, nil, false)

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
	wsURL := liveTestServer(t, fake, nil, false)

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
	wsURL := liveTestServer(t, fake, nil, false)

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
		{Kind: service.LiveEventClaims, ID: "0", Segment: seg, Claims: []service.AtomicClaim{{
			ClaimID: "0-0",
			Text:    "the moon is made of cheese.",
			Quote:   "moon is made of cheese",
			Spans:   []domain.ClaimSpan{{SegmentID: "0", Start: 4, End: 26}},
		}}},
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusChecking},
		{
			Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{Verdict: service.VerdictDisputed, Basis: service.BasisEvidence, Confidence: 0.9, Citations: []domain.SegmentMatch{cite}, Rationale: "rock, not cheese"},
		},
	}}
	// Debug on AND an admin caller, so the per-passage evidence detail (the cited
	// matches) rides the frame and its round-trip can be asserted.
	wsURL := liveTestServer(t, fake, nil, true)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, adminDialOptions())
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
	if claims.Claims[0].Quote != "moon is made of cheese" {
		t.Errorf("claim quote = %q, want the verbatim quote round-tripped", claims.Claims[0].Quote)
	}
	if len(claims.Claims[0].Spans) != 1 || claims.Claims[0].Spans[0] != (domain.ClaimSpan{SegmentID: "0", Start: 4, End: 26}) {
		t.Errorf("claim spans = %+v, want the highlight span round-tripped", claims.Claims[0].Spans)
	}

	checking := readFrame(ctx, t, conn)
	if checking.Type != "claim_result" || checking.ID != "0" || checking.ClaimID != "0-0" || checking.Status != "checking" {
		t.Fatalf("checking frame = %+v, want claim_result id 0 claim_id 0-0 checking", checking)
	}

	verified := readFrame(ctx, t, conn)
	if verified.Type != "claim_result" || verified.ID != "0" || verified.ClaimID != "0-0" || verified.Status != "verified" || verified.Source != "verified" {
		t.Fatalf("verified frame = %+v, want claim_result id 0 claim_id 0-0 verified/verified", verified)
	}
	if verified.Verdict != "disputed" || verified.Basis != "evidence" || verified.Confidence == nil || *verified.Confidence != 0.9 {
		t.Errorf("verdict=%q basis=%q confidence=%v, want disputed/evidence/0.9", verified.Verdict, verified.Basis, verified.Confidence)
	}
	if len(verified.Matches) != 1 || verified.Matches[0].EvidenceID != "evidence:42:0" {
		t.Errorf("citations did not round-trip: %+v", verified.Matches)
	}
	if verified.SourceLabel != domain.SourceLabelWikipedia {
		t.Errorf("source_label = %q, want %q", verified.SourceLabel, domain.SourceLabelWikipedia)
	}
}

func TestLiveHandlerOmitsEvidenceDetailWhenDebugOff(t *testing.T) {
	t.Parallel()
	// With DEBUG_FACT_CHECK off, a verified per-claim result surfaces the source
	// label and link but never the per-passage evidence detail, so the detailed
	// payload is not emitted in production.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "le chômage a baissé", Speaker: "A"}
	cite := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "taux", Similarity: 1, EvidenceID: "insee:CHOM:0", Sources: []domain.Source{{Title: "INSEE", URL: "https://insee.fr/x"}}}
	fake := &recordingLive{events: []service.LiveEvent{
		{
			Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{Verdict: service.VerdictCredible, Basis: service.BasisEvidence, Confidence: 0.9, Citations: []domain.SegmentMatch{cite}, Rationale: "ok"},
		},
	}}
	wsURL := liveTestServer(t, fake, nil, false)

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

	verified := readFrame(ctx, t, conn)
	if verified.Type != "claim_result" || verified.Status != "verified" {
		t.Fatalf("frame = %+v, want verified claim_result", verified)
	}
	if verified.SourceLabel != domain.SourceLabelINSEE || verified.SourceURL != "https://insee.fr/x" {
		t.Errorf("source label/url = %q/%q, want INSEE link", verified.SourceLabel, verified.SourceURL)
	}
	if len(verified.Matches) != 0 {
		t.Errorf("matches = %+v, want none emitted with debug off", verified.Matches)
	}
}

func TestLiveHandlerOmitsEvidenceDetailForNonAdminEvenWhenDebugOn(t *testing.T) {
	t.Parallel()
	// The server-side debug flag is ON, but the caller is a guest (no Bearer
	// token, so the Identity middleware attaches the guest role). The evidence
	// detail must still be withheld: debug behavior rides a verified admin claim,
	// never a client's ability to reach the endpoint.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the moon is made of cheese", Speaker: "A"}
	cite := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", Similarity: 0.7, EvidenceID: "evidence:42:0"}
	fake := &recordingLive{events: []service.LiveEvent{
		{
			Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{Verdict: service.VerdictDisputed, Basis: service.BasisEvidence, Confidence: 0.9, Citations: []domain.SegmentMatch{cite}, Rationale: "rock, not cheese"},
		},
	}}
	wsURL := liveTestServer(t, fake, nil, true)

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

	verified := readFrame(ctx, t, conn)
	if verified.Type != "claim_result" || verified.Status != "verified" {
		t.Fatalf("frame = %+v, want verified claim_result", verified)
	}
	if len(verified.Matches) != 0 {
		t.Errorf("matches = %+v, want none emitted for a non-admin caller", verified.Matches)
	}
}

func TestLiveHandlerForwardsSpeakerTally(t *testing.T) {
	t.Parallel()
	// A speaker-tally event serializes to a speaker_tally frame carrying the running
	// verdict counts, keyed on speaker.
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventSpeakerTally, SpeakerTally: &service.SpeakerTally{Speaker: "A", Credible: 1, Disputed: 0, Unverifiable: 2}},
	}}
	wsURL := liveTestServer(t, fake, nil, false)

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
	if frame.Type != "speaker_tally" || frame.Speaker != "A" {
		t.Fatalf("frame = %+v, want speaker_tally for A", frame)
	}
	if frame.Credible != 1 || frame.Disputed != 0 || frame.Unverifiable != 2 {
		t.Errorf("tally frame = %+v, want credible 1 disputed 0 unverifiable 2", frame)
	}
	if frame.MisleadingFraming != 0 {
		t.Errorf("misleading_framing = %d, want 0 on the credibility-only path", frame.MisleadingFraming)
	}
}

func TestLiveHandlerForwardsPoliticalTwoAxisVerdict(t *testing.T) {
	t.Parallel()
	// A political per-claim verdict serializes its two orthogonal axes onto the wire:
	// the literal verdict and the manipulation flags, alongside the credibility verdict
	// and the cited source, so the frontend (VER-104) can render the two-axis display.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "le chômage a baissé", Speaker: "A"}
	cite := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "taux", Similarity: 1, EvidenceID: "insee:CHOM:0", Sources: []domain.Source{{Title: "INSEE", URL: "https://insee.fr/x"}}}
	fake := &recordingLive{events: []service.LiveEvent{
		{
			Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{
				Verdict: service.VerdictCredible, Basis: service.BasisEvidence, Confidence: 0.9,
				Literal: service.LiteralAccurate, Flags: []string{service.FlagCherryPicked},
				Citations: []domain.SegmentMatch{cite}, Rationale: "exact mais cherry-picked",
			},
		},
	}}
	wsURL := liveTestServer(t, fake, nil, false)

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
	if frame.Type != "claim_result" || frame.Status != "verified" {
		t.Fatalf("frame = %+v, want verified claim_result", frame)
	}
	if frame.Literal != "accurate" {
		t.Errorf("literal = %q, want accurate", frame.Literal)
	}
	if len(frame.Flags) != 1 || frame.Flags[0] != "cherry-picked" {
		t.Errorf("flags = %v, want [cherry-picked]", frame.Flags)
	}
	if frame.Verdict != "credible" {
		t.Errorf("verdict = %q, want credible (credibility axis)", frame.Verdict)
	}
	// The source label and link are surfaced even with debug off, so a normal
	// viewer sees the provenance; the detailed matches payload stays absent.
	if frame.SourceLabel != domain.SourceLabelINSEE {
		t.Errorf("source_label = %q, want %q", frame.SourceLabel, domain.SourceLabelINSEE)
	}
	if frame.SourceURL != "https://insee.fr/x" {
		t.Errorf("source_url = %q, want the INSEE citation url", frame.SourceURL)
	}
	if len(frame.Matches) != 0 {
		t.Errorf("matches = %+v, want none emitted with debug off", frame.Matches)
	}
}

func TestLiveHandlerForwardsMisleadingFramingTally(t *testing.T) {
	t.Parallel()
	// A speaker-tally event with a misleading-framing tally serializes it onto the
	// wire, so the frontend can distinguish outright falsehood from misleading framing.
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventSpeakerTally, SpeakerTally: &service.SpeakerTally{Speaker: "A", Credible: 2, MisleadingFraming: 1}},
	}}
	wsURL := liveTestServer(t, fake, nil, false)

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
	if frame.Type != "speaker_tally" || frame.MisleadingFraming != 1 {
		t.Fatalf("frame = %+v, want speaker_tally with misleading_framing 1", frame)
	}
}

func TestLiveHandlerSerializesClaimErrorTerminal(t *testing.T) {
	t.Parallel()
	// A claim that fails verification serializes to a claim_result with status
	// "error" and the failure reason, distinct on the wire from a verified verdict
	// so the client does not render it as a reached verdict.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the moon is made of cheese", Speaker: "A"}
	fake := &recordingLive{events: []service.LiveEvent{
		{Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusError, Err: "verification failed"},
	}}
	wsURL := liveTestServer(t, fake, nil, false)

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
	if frame.Type != "claim_result" || frame.ID != "0" || frame.ClaimID != "0-0" || frame.Status != "error" {
		t.Fatalf("error frame = %+v, want claim_result id 0 claim_id 0-0 error", frame)
	}
	if frame.Error != "verification failed" {
		t.Errorf("error frame Error = %q, want verification failed", frame.Error)
	}
	if frame.Verdict != "" {
		t.Errorf("error frame must not carry a verdict, got %q", frame.Verdict)
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
	wsURL := liveTestServer(t, fake, nil, false)

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
	wsURL := liveTestServer(t, stubLiveAnalyzer{}, []string{"localhost:3000"}, false)

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

	wsURL := liveTestServer(t, stubLiveAnalyzer{}, nil, false)

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
	// The live route sits under the /api Keycloak identity gate, so an upgrade with
	// no verified token is rejected with 401 before any WebSocket handshake.
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid1/live", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET live without a token = %d, want 401", rec.Code)
	}
}

// liveMuxServer wires the live route through NewMux exactly as production does:
// behind RequireIdentity, which reads the access_token query parameter the browser
// WebSocket carries (it cannot set the Authorization header). It returns the base
// ws URL; tests append the query string.
func liveMuxServer(t *testing.T, analyzer LiveAnalyzer, debugFactCheck bool) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hc := service.NewHealthChecker(fakePinger{})
	mux := NewMux(hc, &fakeVideoService{}, &fakeDocumentService{}, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, &fakeTVChannelService{}, &fakeTVRecordingService{}, testTVHub(), analyzer, nil, nil, nil, debugFactCheck, nil, "", globalTestAuth, logger)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/videos/vid1/live"
}

// TestLiveWSAdminQueryParamUnlocksDebugDetail proves the WS admin-debug gating
// end to end through NewMux: the browser carries the Keycloak token as the
// access_token query parameter (it cannot set the Authorization header), and only
// a verified admin token unlocks the per-passage evidence detail. A guest token
// connects but never receives the detail, and no token is rejected before the
// handshake. Server-side enforcement stays authoritative: nothing else a client
// sends can flip the detail on.
func TestLiveWSAdminQueryParamUnlocksDebugDetail(t *testing.T) {
	t.Parallel()
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the moon is made of cheese", Speaker: "A"}
	cite := domain.SegmentMatch{Kind: domain.MatchKindEvidence, Claim: "the moon is rock", Similarity: 0.7, EvidenceID: "evidence:42:0"}
	events := []service.LiveEvent{
		{
			Kind: service.LiveEventResult, ID: "0", Segment: seg, ClaimID: "0-0", ClaimStatus: service.ClaimStatusVerified, Source: service.SourceVerified,
			Verdict: &service.VerifiedVerdict{Verdict: service.VerdictDisputed, Basis: service.BasisEvidence, Confidence: 0.9, Citations: []domain.SegmentMatch{cite}, Rationale: "rock, not cheese"},
		},
	}

	tests := []struct {
		name           string
		token          string
		wantDialErr    bool
		wantDetail     bool
		wantDetailNote string
	}{
		{name: "admin token unlocks the evidence detail", token: testAdminToken, wantDetail: true},
		{name: "guest token connects without the detail", token: testGuestToken, wantDetail: false},
		{name: "no token is rejected before the handshake", token: "", wantDialErr: true},
		{name: "invalid token is rejected before the handshake", token: "bogus", wantDialErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := liveMuxServer(t, &recordingLive{events: events}, true)
			wsURL := base
			if tc.token != "" {
				wsURL = base + "?access_token=" + tc.token
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, resp, err := websocket.Dial(ctx, wsURL, nil)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if tc.wantDialErr {
				if err == nil {
					_ = conn.CloseNow()
					t.Fatal("dial without a verified token must be rejected at the identity gate")
				}
				if resp == nil || resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("dial rejection status = %v, want 401", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = conn.CloseNow() }()
			if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01}); err != nil {
				t.Fatalf("write audio: %v", err)
			}
			frame := readFrame(ctx, t, conn)
			if frame.Type != "claim_result" || frame.Status != "verified" {
				t.Fatalf("frame = %+v, want verified claim_result", frame)
			}
			if tc.wantDetail && len(frame.Matches) != 1 {
				t.Errorf("admin caller must receive the evidence detail, got matches %+v", frame.Matches)
			}
			if !tc.wantDetail && len(frame.Matches) != 0 {
				t.Errorf("non-admin caller must not receive the evidence detail, got matches %+v", frame.Matches)
			}
		})
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
