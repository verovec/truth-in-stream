package service

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestCosineSimilarity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"scaled is still parallel", []float32{2, 0}, []float32{5, 0}, 1},
		{"length mismatch", []float32{1, 0}, []float32{1}, 0},
		{"empty", []float32{}, []float32{}, 0},
		{"zero magnitude", []float32{0, 0}, []float32{1, 1}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cosineSimilarity(tc.a, tc.b)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("cosineSimilarity = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpeakerMemoryIsolatesSpeakers(t *testing.T) {
	t.Parallel()
	mem := newSpeakerMemory()
	mem.remember("A", priorStatement{id: "0", text: "a1", embedding: []float32{1, 0}})
	mem.remember("A", priorStatement{id: "1", text: "a2", embedding: []float32{0, 1}})
	mem.remember("B", priorStatement{id: "2", text: "b1", embedding: []float32{1, 1}})

	if got := len(mem.prior("A")); got != 2 {
		t.Errorf("speaker A history = %d, want 2", got)
	}
	if got := len(mem.prior("B")); got != 1 {
		t.Errorf("speaker B history = %d, want 1", got)
	}
	if got := mem.prior("C"); got != nil {
		t.Errorf("unknown speaker history = %v, want nil", got)
	}
}

func TestRankBySimilarityFiltersAndCaps(t *testing.T) {
	t.Parallel()
	priors := []priorStatement{
		{id: "near", embedding: []float32{1, 0}},       // sim 1.0
		{id: "mid", embedding: []float32{1, 0.2}},      // sim ~0.98
		{id: "far", embedding: []float32{0, 1}},        // sim 0, below floor
		{id: "second", embedding: []float32{0.9, 0.1}}, // high sim
	}
	emb := []float32{1, 0}

	ranked := rankBySimilarity(emb, priors, 0.6, 2)
	if len(ranked) != 2 {
		t.Fatalf("ranked length = %d, want 2 (top-k cap)", len(ranked))
	}
	if ranked[0].id != "near" {
		t.Errorf("closest prior = %q, want near", ranked[0].id)
	}
	for _, p := range ranked {
		if p.id == "far" {
			t.Error("below-floor prior should be excluded")
		}
	}
}

// stubStance records every pairwise call and returns a programmed verdict, so a
// test can assert exactly which comparisons ran and drive the flag outcome.
type stubStance struct {
	verdict func(earlier, later string) (bool, string, error)

	mu    sync.Mutex
	calls [][2]string
}

func (s *stubStance) Contradicts(_ context.Context, earlier, later string) (bool, string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, [2]string{earlier, later})
	s.mu.Unlock()
	return s.verdict(earlier, later)
}

func (s *stubStance) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// alwaysContradicts is the verdict for tests that want every stance call (when
// one happens) to report a contradiction, so a missing flag means detection
// never reached the stance check.
func alwaysContradicts(_, _ string) (bool, string, error) {
	return true, "they conflict", nil
}

// consistencyEvents extracts only the consistency events from a drained stream.
func consistencyEvents(events []LiveEvent) []LiveEvent {
	var out []LiveEvent
	for _, ev := range events {
		if ev.Kind == LiveEventConsistency {
			out = append(out, ev)
		}
	}
	return out
}

// twoStatementConfig wires an analyzer that scores each segment as its own unit
// (MaxSentences 1) on a single worker (Concurrency 1), so the first statement is
// always remembered before the second is checked - the deterministic ordering
// the consistency assertions need. Each statement's text maps to a query
// embedding so the cosine stage is controllable.
func twoStatementConfig(stream SegmentStream, stance StanceClassifier, embeddings map[string][]float32, skip map[string]domain.SkipReason) LiveAnalyzerConfig {
	return LiveAnalyzerConfig{
		Stream:           stream,
		Matcher:          liveMatcher{embedding: embeddings},
		Prechecker:       livePrechecker{skip: skip},
		Logger:           discardLogger(),
		Concurrency:      1,
		MaxSentences:     1,
		Stance:           stance,
		ConsistencyFloor: 0.6,
		ConsistencyTopK:  3,
	}
}

func runToCompletion(t *testing.T, cfg LiveAnalyzerConfig) []LiveEvent {
	t.Helper()
	analyzer, err := NewLiveAnalyzer(cfg)
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}
	out, err := analyzer.Run(t.Context(), make(chan []byte))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return drainLiveEvents(t, out)
}

func TestLiveAnalyzerFlagsSelfContradiction(t *testing.T) {
	t.Parallel()
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "the bridge opened in 1940.", Speaker: "A"},
	}
	stance := &stubStance{verdict: alwaysContradicts}
	embeddings := map[string][]float32{
		"the bridge opened in 1937.": {1, 0},
		"the bridge opened in 1940.": {1, 0}, // same topic -> clears the floor
	}
	events := runToCompletion(t, twoStatementConfig(&fakeSegmentStream{transcripts: finalize(segs...)}, stance, embeddings, nil))

	flags := consistencyEvents(events)
	if len(flags) != 1 {
		t.Fatalf("expected exactly one consistency event, got %d", len(flags))
	}
	flag := flags[0]
	if flag.ID != "1" {
		t.Errorf("flag is on statement %q, want the later statement id 1", flag.ID)
	}
	if flag.Consistency == nil || flag.Consistency.EarlierID != "0" {
		t.Errorf("flag should link back to the earlier statement id 0, got %+v", flag.Consistency)
	}
	if flag.Consistency.EarlierText != "the bridge opened in 1937." {
		t.Errorf("earlier text = %q", flag.Consistency.EarlierText)
	}
	if flag.Consistency.Speaker != "A" {
		t.Errorf("flag speaker = %q, want A", flag.Consistency.Speaker)
	}
}

func TestLiveAnalyzerFlagsSelfContradictionUnderConcurrency(t *testing.T) {
	t.Parallel()
	// With more than one worker, two of the same speaker's units can be scored
	// at once. Per-speaker serialization of detection must still surface the
	// contradiction exactly once: the later-detected statement flags against the
	// earlier-detected one, whichever order the workers happen to run in.
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "the bridge opened in 1940.", Speaker: "A"},
	}
	embeddings := map[string][]float32{
		"the bridge opened in 1937.": {1, 0},
		"the bridge opened in 1940.": {1, 0},
	}
	cfg := twoStatementConfig(&fakeSegmentStream{transcripts: finalize(segs...)}, &stubStance{verdict: alwaysContradicts}, embeddings, nil)
	cfg.Concurrency = 4 // exercise the same-speaker race the per-speaker lock closes

	flags := consistencyEvents(runToCompletion(t, cfg))
	if len(flags) != 1 {
		t.Fatalf("expected exactly one consistency event under concurrency, got %d", len(flags))
	}
	flag := flags[0]
	if flag.Consistency == nil {
		t.Fatal("consistency event missing its flag payload")
	}
	if flag.ID == flag.Consistency.EarlierID {
		t.Errorf("a statement must not contradict itself: id=%q earlier=%q", flag.ID, flag.Consistency.EarlierID)
	}
	ids := map[string]bool{"0": true, "1": true}
	if !ids[flag.ID] || !ids[flag.Consistency.EarlierID] {
		t.Errorf("flag should link the two statements 0 and 1, got id=%q earlier=%q", flag.ID, flag.Consistency.EarlierID)
	}
}

func TestLiveAnalyzerNoFlagAcrossSpeakers(t *testing.T) {
	t.Parallel()
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "the bridge opened in 1940.", Speaker: "B"},
	}
	stance := &stubStance{verdict: alwaysContradicts}
	embeddings := map[string][]float32{
		"the bridge opened in 1937.": {1, 0},
		"the bridge opened in 1940.": {1, 0},
	}
	events := runToCompletion(t, twoStatementConfig(&fakeSegmentStream{transcripts: finalize(segs...)}, stance, embeddings, nil))

	if got := consistencyEvents(events); len(got) != 0 {
		t.Fatalf("expected no consistency events across speakers, got %d", len(got))
	}
	if stance.callCount() != 0 {
		t.Errorf("stance should never be called across speakers, got %d calls", stance.callCount())
	}
}

func TestLiveAnalyzerNoFlagBelowSimilarityFloor(t *testing.T) {
	t.Parallel()
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "i had eggs for breakfast.", Speaker: "A"},
	}
	stance := &stubStance{verdict: alwaysContradicts}
	embeddings := map[string][]float32{
		"the bridge opened in 1937.": {1, 0}, // orthogonal topics -> below the floor
		"i had eggs for breakfast.":  {0, 1},
	}
	events := runToCompletion(t, twoStatementConfig(&fakeSegmentStream{transcripts: finalize(segs...)}, stance, embeddings, nil))

	if got := consistencyEvents(events); len(got) != 0 {
		t.Fatalf("unrelated topics must not flag, got %d events", len(got))
	}
	if stance.callCount() != 0 {
		t.Errorf("the floor should gate the stance check off, got %d calls", stance.callCount())
	}
}

func TestLiveAnalyzerNoFlagForNonCheckableUnit(t *testing.T) {
	t.Parallel()
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "how about the bridge?", Speaker: "A"},
	}
	stance := &stubStance{verdict: alwaysContradicts}
	embeddings := map[string][]float32{
		"the bridge opened in 1937.": {1, 0},
		"how about the bridge?":      {1, 0},
	}
	// The second statement is gated out as a non-claim, so it is never compared.
	skip := map[string]domain.SkipReason{"how about the bridge?": domain.SkipReasonNotAClaim}
	events := runToCompletion(t, twoStatementConfig(&fakeSegmentStream{transcripts: finalize(segs...)}, stance, embeddings, skip))

	if got := consistencyEvents(events); len(got) != 0 {
		t.Fatalf("a non-checkable unit must not flag, got %d events", len(got))
	}
	if stance.callCount() != 0 {
		t.Errorf("a skipped unit must not run a stance check, got %d calls", stance.callCount())
	}
}

func TestLiveAnalyzerStanceErrorDegradesToNoFlag(t *testing.T) {
	t.Parallel()
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "the bridge opened in 1940.", Speaker: "A"},
	}
	stance := &stubStance{verdict: func(_, _ string) (bool, string, error) {
		return false, "", errors.New("stance api down")
	}}
	embeddings := map[string][]float32{
		"the bridge opened in 1937.": {1, 0},
		"the bridge opened in 1940.": {1, 0},
	}
	events := runToCompletion(t, twoStatementConfig(&fakeSegmentStream{transcripts: finalize(segs...)}, stance, embeddings, nil))

	if got := consistencyEvents(events); len(got) != 0 {
		t.Fatalf("a stance error must degrade to no flag, got %d events", len(got))
	}
	// The session still completed: both subtitles and both results were emitted.
	subtitles, results := collectByKind(events)
	if len(subtitles) != 2 || len(results) != 2 {
		t.Fatalf("a stance error must not end the session: got %d subtitles, %d results", len(subtitles), len(results))
	}
	if stance.callCount() == 0 {
		t.Error("expected the stance check to have been attempted")
	}
}

func TestLiveAnalyzerFeatureOffEmitsNoConsistency(t *testing.T) {
	t.Parallel()
	segs := []domain.Segment{
		{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"},
		{Start: 3 * time.Second, End: 4 * time.Second, Text: "the bridge opened in 1940.", Speaker: "A"},
	}
	// No Stance configured: live analysis behaves exactly as before.
	cfg := LiveAnalyzerConfig{
		Stream:       &fakeSegmentStream{transcripts: finalize(segs...)},
		Matcher:      liveMatcher{embedding: map[string][]float32{"the bridge opened in 1937.": {1, 0}, "the bridge opened in 1940.": {1, 0}}},
		Logger:       discardLogger(),
		Concurrency:  1,
		MaxSentences: 1,
	}
	events := runToCompletion(t, cfg)

	if got := consistencyEvents(events); len(got) != 0 {
		t.Fatalf("the feature-off path must emit no consistency events, got %d", len(got))
	}
	subtitles, results := collectByKind(events)
	if len(subtitles) != 2 || len(results) != 2 {
		t.Fatalf("subtitles and results must stream normally: got %d subtitles, %d results", len(subtitles), len(results))
	}
}

func TestLiveAnalyzerMemoryIsPerSession(t *testing.T) {
	t.Parallel()
	stance := &stubStance{verdict: alwaysContradicts}
	embeddings := map[string][]float32{"the bridge opened in 1937.": {1, 0}}
	analyzer, err := NewLiveAnalyzer(twoStatementConfig(blockingSegmentStream{}, stance, embeddings, nil))
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}

	// One statement per session, identical across sessions. Within a session a
	// lone statement has no prior to compare against; if memory leaked across
	// sessions the second run would compare against the first run's statement.
	seg := domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "the bridge opened in 1937.", Speaker: "A"}
	for run := 0; run < 2; run++ {
		stream := &fakeSegmentStream{transcripts: finalize(seg)}
		out, err := analyzerRun(t, analyzer, stream)
		if err != nil {
			t.Fatalf("Run %d: %v", run, err)
		}
		if got := consistencyEvents(drainLiveEvents(t, out)); len(got) != 0 {
			t.Fatalf("run %d: a session-fresh memory must not flag a lone statement, got %d", run, len(got))
		}
	}
	if stance.callCount() != 0 {
		t.Errorf("no cross-session comparison should occur, got %d stance calls", stance.callCount())
	}
}

// analyzerRun swaps the analyzer's stream for a fresh one and runs it, so a test
// can drive the same analyzer across two independent sessions.
func analyzerRun(t *testing.T, a *LiveAnalyzer, stream SegmentStream) (<-chan LiveEvent, error) {
	t.Helper()
	a.stream = stream
	return a.Run(t.Context(), make(chan []byte))
}
