package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type captureWriter struct {
	mu     sync.Mutex
	rows   []domain.ClaimCheck
	err    error
	writes int
}

func (w *captureWriter) InsertClaimChecks(_ context.Context, checks []domain.ClaimCheck) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.err != nil {
		return w.err
	}
	w.rows = append(w.rows, checks...)
	return nil
}

func (w *captureWriter) all() []domain.ClaimCheck {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]domain.ClaimCheck(nil), w.rows...)
}

func testTelemetryConfig() TelemetryConfig {
	return TelemetryConfig{QueueDepth: 4, BatchSize: 2, FlushEvery: 10 * time.Millisecond, SampleRate: 1, Locale: "fr"}
}

func TestTelemetryRecordNeverBlocksAndCountsDrops(t *testing.T) {
	t.Parallel()
	r, err := NewTelemetryRecorder(&captureWriter{}, testTelemetryConfig())
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}
	// The recorder is not running, so the buffer (depth 4) fills and every
	// further Record must return immediately as a counted drop.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			r.Record(domain.ClaimCheck{DecisionPath: domain.DecisionVerified})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked under saturation")
	}
	if got := r.Drops(); got != 16 {
		t.Errorf("Drops = %d, want 16 (20 records into a depth-4 buffer)", got)
	}
}

func TestTelemetryRunFlushesBatchesAndDrain(t *testing.T) {
	t.Parallel()
	w := &captureWriter{}
	r, err := NewTelemetryRecorder(w, testTelemetryConfig())
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		r.Run(ctx)
	}()

	r.Record(domain.ClaimCheck{ClaimText: "a", DecisionPath: domain.DecisionCache})
	r.Record(domain.ClaimCheck{ClaimText: "b", DecisionPath: domain.DecisionVerified})
	deadline := time.Now().Add(2 * time.Second)
	for len(w.all()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	rows := w.all()
	if len(rows) != 2 {
		t.Fatalf("flushed rows = %d, want 2", len(rows))
	}
	if rows[0].Locale != "fr" || rows[0].OccurredAt.IsZero() {
		t.Errorf("row not stamped with locale and time: %+v", rows[0])
	}

	// Rows enqueued at shutdown are drained before Run returns.
	r.Record(domain.ClaimCheck{ClaimText: "c", DecisionPath: domain.DecisionCurated})
	cancel()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if got := len(w.all()); got != 3 {
		t.Errorf("rows after shutdown drain = %d, want 3", got)
	}
}

func TestTelemetryWriteFailureDropsBatch(t *testing.T) {
	t.Parallel()
	w := &captureWriter{err: errors.New("db down")}
	r, err := NewTelemetryRecorder(w, testTelemetryConfig())
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		r.Run(ctx)
	}()
	r.Record(domain.ClaimCheck{DecisionPath: domain.DecisionVerified})
	deadline := time.Now().Add(2 * time.Second)
	for r.Drops() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-ran
	if got := r.Drops(); got < 1 {
		t.Errorf("Drops = %d, want at least 1 after a failed write", got)
	}
}

func TestNewTelemetryRecorderValidatesConfig(t *testing.T) {
	t.Parallel()
	base := testTelemetryConfig()
	bad := []TelemetryConfig{
		{QueueDepth: 0, BatchSize: base.BatchSize, FlushEvery: base.FlushEvery, SampleRate: 1},
		{QueueDepth: base.QueueDepth, BatchSize: 0, FlushEvery: base.FlushEvery, SampleRate: 1},
		{QueueDepth: base.QueueDepth, BatchSize: base.BatchSize, FlushEvery: 0, SampleRate: 1},
		{QueueDepth: base.QueueDepth, BatchSize: base.BatchSize, FlushEvery: base.FlushEvery, SampleRate: 0},
		{QueueDepth: base.QueueDepth, BatchSize: base.BatchSize, FlushEvery: base.FlushEvery, SampleRate: 1.5},
	}
	for i, cfg := range bad {
		if _, err := NewTelemetryRecorder(&captureWriter{}, cfg); err == nil {
			t.Errorf("config %d accepted, want error", i)
		}
	}
	if _, err := NewTelemetryRecorder(nil, base); err == nil {
		t.Error("nil writer accepted, want error")
	}
}

// drainRecorder reads whatever rows sit in the recorder's buffer without
// running its write loop, so instrumentation tests assert rows deterministically.
func drainRecorder(r *TelemetryRecorder) []domain.ClaimCheck {
	var rows []domain.ClaimCheck
	for {
		select {
		case c := <-r.ch:
			rows = append(rows, c)
		default:
			return rows
		}
	}
}

func TestVerifyPathRecordsClaimChecks(t *testing.T) {
	t.Parallel()
	unit := "the earth is round and the moon is made of cheese."
	fastClaim := "the earth is round."
	verifyClaim := "the moon is made of cheese."

	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	matcher := liveMatcher{
		matches: map[string][]domain.SegmentMatch{
			fastClaim:   {{Kind: domain.MatchKindClaim, Claim: "earth is an oblate spheroid", Verdict: domain.Verdict("corroborates"), Similarity: 0.95, EvidenceID: "claim:c1:0"}},
			verifyClaim: {{Kind: domain.MatchKindEvidence, Claim: "the moon is composed of rock", Similarity: 0.7, EvidenceID: "evidence:42:0"}},
		},
	}
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		verifyClaim: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.9, Citations: []EvidenceCitation{{EvidenceID: "evidence:42:0", QuotedSpan: "rock"}}, Rationale: "the moon is rock, not cheese"},
	}}
	recorder, err := NewTelemetryRecorder(&captureWriter{}, TelemetryConfig{QueueDepth: 32, BatchSize: 8, FlushEvery: time.Second, SampleRate: 1, Locale: "fr"})
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {fastClaim, verifyClaim}}},
		Verifier:   verifier,
		Telemetry:  recorder,
	})
	runVerifyPath(t, a)

	rows := drainRecorder(recorder)
	byPath := map[string]domain.ClaimCheck{}
	for _, r := range rows {
		byPath[r.DecisionPath] = r
	}
	if len(rows) != 2 {
		t.Fatalf("recorded rows = %d (%v), want 2 (one per claim)", len(rows), byPath)
	}

	curated, ok := byPath[domain.DecisionCurated]
	if !ok {
		t.Fatal("no curated-path row recorded")
	}
	if curated.ClaimText != fastClaim || curated.LLMCalls != 0 || curated.Source != string(SourceCurated) {
		t.Errorf("curated row = %+v, want claim text, zero llm calls, curated source", curated)
	}
	if curated.RetrievalTop != 0.95 || curated.RetrievalClaimHits != 1 {
		t.Errorf("curated retrieval snapshot = %+v, want top 0.95 with one claim hit", curated)
	}

	verified, ok := byPath[domain.DecisionVerified]
	if !ok {
		t.Fatal("no verified-path row recorded")
	}
	if verified.ClaimText != verifyClaim || verified.LLMCalls != 1 || verified.Verdict != VerdictDisputed {
		t.Errorf("verified row = %+v, want one llm call and the disputed verdict", verified)
	}
	if verified.UnitText != unit || verified.Speaker != "A" {
		t.Errorf("verified row context = %+v, want unit text and speaker", verified)
	}
}

func TestVerifyPathRecordsGateSkip(t *testing.T) {
	t.Parallel()
	unit := "hello everyone welcome to the show tonight."
	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "H",
	})}
	recorder, err := NewTelemetryRecorder(&captureWriter{}, TelemetryConfig{QueueDepth: 8, BatchSize: 8, FlushEvery: time.Second, SampleRate: 1})
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}
	a := verifyPathFixture(t, stream, liveMatcher{}, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {}}},
		Verifier:   &fakeVerifier{},
		Telemetry:  recorder,
	})
	// The analyzer fixture wires an allow-all prechecker, so the empty
	// decomposition is the skip exercised here: the unit reaches the extractor
	// and yields no claim.
	runVerifyPath(t, a)

	rows := drainRecorder(recorder)
	if len(rows) != 1 {
		t.Fatalf("recorded rows = %d, want 1 unit-level skip row", len(rows))
	}
	if rows[0].DecisionPath != domain.DecisionNoClaim || rows[0].ClaimText != "" || rows[0].UnitText != unit {
		t.Errorf("skip row = %+v, want no-claim path with unit text only", rows[0])
	}
}

func TestVerifyPathCountsAttemptedReverify(t *testing.T) {
	t.Parallel()
	unit := "the deficit doubled last year."
	claim := unit

	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "A",
	})}
	matcher := liveMatcher{matches: map[string][]domain.SegmentMatch{
		claim: {{Kind: domain.MatchKindEvidence, Claim: "the deficit rose slightly", Similarity: 0.7, EvidenceID: "evidence:9:0"}},
	}}
	// The fast verdict is weak (below the trigger floor), so the terminal gate
	// runs the reasoning call; the re-judgment stays low-confidence, so it is
	// rejected and the verdict is NOT replaced. The call was still spent.
	verifier := &fakeVerifier{byClaim: map[string]ClaimVerdict{
		claim: {Verdict: VerdictUnverifiable, Basis: BasisEvidence, Confidence: 0.3, Citations: []EvidenceCitation{{EvidenceID: "evidence:9:0", QuotedSpan: "rose"}}},
	}}
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		claim: {Verdict: VerdictUnverifiable, Basis: BasisKnowledge, Confidence: 0.2},
	}}
	recorder, err := NewTelemetryRecorder(&captureWriter{}, TelemetryConfig{QueueDepth: 8, BatchSize: 8, FlushEvery: time.Second, SampleRate: 1})
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {claim}}},
		Verifier:   verifier,
		Telemetry:  recorder,
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})
	runVerifyPath(t, a)

	rows := drainRecorder(recorder)
	if len(rows) != 1 {
		t.Fatalf("recorded rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Escalated {
		t.Error("row marked escalated though the re-judgment was rejected")
	}
	if row.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, want 2 (verifier plus the attempted reasoning call)", row.LLMCalls)
	}
	if len(reverifier.calls) != 1 {
		t.Fatalf("reverifier calls = %d, want 1 (the premise of this test)", len(reverifier.calls))
	}
}

func TestVerifyPathRecordsLocalNLIDecision(t *testing.T) {
	t.Parallel()
	unit := "the debt exceeds three trillion euros."
	claim := unit

	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "B",
	})}
	matcher := liveMatcher{
		matches: map[string][]domain.SegmentMatch{
			claim: {{Kind: domain.MatchKindEvidence, Claim: "public debt reached 3.1 trillion euros", Similarity: 0.7, EvidenceID: "evidence:7:0"}},
		},
	}
	verifier := &fakeVerifier{}
	recorder, err := NewTelemetryRecorder(&captureWriter{}, TelemetryConfig{QueueDepth: 32, BatchSize: 8, FlushEvery: time.Second, SampleRate: 1, Locale: "fr"})
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {claim}}},
		Verifier:   verifier,
		Telemetry:  recorder,
		NLIStance: &StanceConfig{
			Scorer:              &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: {entail(0.95)}}},
			EntailThreshold:     0.7,
			ContradictThreshold: 0.9,
			MinAgree:            1,
			MaxPassages:         6,
		},
	})
	runVerifyPath(t, a)

	rows := drainRecorder(recorder)
	if len(rows) != 1 {
		t.Fatalf("recorded rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.DecisionPath != domain.DecisionLocalNLI {
		t.Fatalf("decision path = %q, want %q", row.DecisionPath, domain.DecisionLocalNLI)
	}
	if row.LLMCalls != 0 {
		t.Errorf("llm calls = %d, want 0 for a locally-decided verdict", row.LLMCalls)
	}
	if row.Verdict != VerdictCredible || row.Basis != BasisEvidence {
		t.Errorf("row verdict = %s/%s, want credible/evidence", row.Verdict, row.Basis)
	}
	if row.Source != string(SourceVerified) {
		t.Errorf("row source = %q, want %q (indistinguishable on the wire)", row.Source, SourceVerified)
	}
	if len(verifier.calls) != 0 {
		t.Errorf("verifier called %d times, want 0", len(verifier.calls))
	}
}

func TestVerifyPathEscalatedNLIVerdictRecordsVerifiedPath(t *testing.T) {
	t.Parallel()
	unit := "public debt exceeds three trillion euros again."
	claim := unit

	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "C",
	})}
	matcher := liveMatcher{
		matches: map[string][]domain.SegmentMatch{
			claim: {{Kind: domain.MatchKindEvidence, Claim: "debt data passage", Similarity: 0.7, EvidenceID: "evidence:9:0"}},
		},
	}
	// The stance stage decides locally at 0.75 confidence - inside the second
	// pass's trigger band - and the reasoner then replaces the verdict.
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		claim: {Verdict: VerdictDisputed, Basis: BasisEvidence, Confidence: 0.95, Citations: []EvidenceCitation{{EvidenceID: "evidence:9:0", QuotedSpan: "debt data passage"}}, Rationale: "re-judged"},
	}}
	recorder, err := NewTelemetryRecorder(&captureWriter{}, TelemetryConfig{QueueDepth: 32, BatchSize: 8, FlushEvery: time.Second, SampleRate: 1, Locale: "fr"})
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {claim}}},
		Verifier:   &fakeVerifier{},
		Telemetry:  recorder,
		NLIStance: &StanceConfig{
			Scorer:              &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: {entail(0.75)}}},
			EntailThreshold:     0.7,
			ContradictThreshold: 0.9,
			MinAgree:            1,
			MaxPassages:         6,
		},
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})
	runVerifyPath(t, a)

	rows := drainRecorder(recorder)
	if len(rows) != 1 {
		t.Fatalf("recorded rows = %d, want 1", len(rows))
	}
	row := rows[0]
	// The recorded verdict is the reasoner's, so the path must be the
	// generative one: local-nli is reserved for rows whose verdict really was
	// decided locally.
	if row.DecisionPath != domain.DecisionVerified {
		t.Errorf("decision path = %q, want %q after the second pass replaced the local verdict", row.DecisionPath, domain.DecisionVerified)
	}
	if !row.Escalated {
		t.Error("escalated = false, want true")
	}
	if row.LLMCalls != 1 {
		t.Errorf("llm calls = %d, want 1 (the reverify call only)", row.LLMCalls)
	}
	if row.Verdict != VerdictDisputed || row.Confidence != 0.95 {
		t.Errorf("row verdict = %s/%v, want the reasoner's disputed at 0.95", row.Verdict, row.Confidence)
	}
}

func TestVerifyPathAttemptedButKeptNLIVerdictStaysLocal(t *testing.T) {
	t.Parallel()
	unit := "the minimum wage was raised twice this year."
	claim := unit

	stream := &fakeSegmentStream{transcripts: finalize(domain.Segment{
		Start: time.Second, End: 2 * time.Second, Text: unit, Speaker: "D",
	})}
	matcher := liveMatcher{
		matches: map[string][]domain.SegmentMatch{
			claim: {{Kind: domain.MatchKindEvidence, Claim: "wage data passage", Similarity: 0.7, EvidenceID: "evidence:5:0"}},
		},
	}
	// The reasoner's answer is too weak to adopt (below MinConfidence), so the
	// local verdict stands; the attempt still cost one generative call.
	reverifier := &fakeReverifier{byClaim: map[string]ClaimVerdict{
		claim: {Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.5, Citations: []EvidenceCitation{{EvidenceID: "evidence:5:0", QuotedSpan: "wage data passage"}}},
	}}
	recorder, err := NewTelemetryRecorder(&captureWriter{}, TelemetryConfig{QueueDepth: 32, BatchSize: 8, FlushEvery: time.Second, SampleRate: 1, Locale: "fr"})
	if err != nil {
		t.Fatalf("NewTelemetryRecorder: %v", err)
	}

	a := verifyPathFixture(t, stream, matcher, VerifyPathConfig{
		Decomposer: fakeDecomposer{byText: map[string][]string{unit: {claim}}},
		Verifier:   &fakeVerifier{},
		Telemetry:  recorder,
		NLIStance: &StanceConfig{
			Scorer:              &fakeStanceScorer{byClaim: map[string][]StanceResult{claim: {entail(0.75)}}},
			EntailThreshold:     0.7,
			ContradictThreshold: 0.9,
			MinAgree:            1,
			MaxPassages:         6,
		},
		SecondPass: &SecondPassConfig{Reverifier: reverifier, TriggerBelow: 0.8, MinConfidence: 0.9, Deadline: time.Second},
	})
	runVerifyPath(t, a)

	rows := drainRecorder(recorder)
	if len(rows) != 1 {
		t.Fatalf("recorded rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.DecisionPath != domain.DecisionLocalNLI {
		t.Errorf("decision path = %q, want %q when the local verdict stands", row.DecisionPath, domain.DecisionLocalNLI)
	}
	if row.Escalated {
		t.Error("escalated = true, want false when the reverify was rejected")
	}
	if row.LLMCalls != 1 {
		t.Errorf("llm calls = %d, want 1 (the attempted reverify)", row.LLMCalls)
	}
	if row.Verdict != VerdictCredible || row.Confidence != 0.75 {
		t.Errorf("row verdict = %s/%v, want the local credible at 0.75", row.Verdict, row.Confidence)
	}
}
