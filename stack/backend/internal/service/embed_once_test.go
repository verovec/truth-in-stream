package service

import (
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// embedOnceMatcherAdapter wires a Matcher over a shared embedder into the
// SegmentMatchAdapter, so a test can build the exact legacy production shape (a
// coverage gate and a matcher over one embedder) and count the embedding calls.
func embedOnceMatcherAdapter(t *testing.T, embedder QueryEmbedder, claims ClaimSearcher, evidence EvidenceSearcher) *SegmentMatchAdapter {
	t.Helper()
	m, err := NewMatcher(embedder, claims, evidence, testMatcherConfig())
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return NewSegmentMatchAdapter(m)
}

func TestGateAndMatchEmbedOnceEmbedsExactlyOnce(t *testing.T) {
	t.Parallel()
	// A checkable unit is embedded once and the vector is shared by the coverage
	// gate and the matcher, collapsing the former double embed (coverage then
	// match) that this card fixes. The counting embedder is shared by both stages,
	// so an embed in either shows up in the single call count.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	coverage := newCombined(t, embedder, &fakeSearcher{hits: claimHits(0.1)}, &fakeEvidence{},
		CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiEnabled: false})
	matcher := embedOnceMatcherAdapter(t, embedder, &fakeSearcher{hits: claimHits(0.1)}, &fakeEvidence{})

	_, decision, err := gateAndMatchEmbedOnce(t.Context(), stubClassifier{checkable: true}, coverage, matcher, "a factual statement")
	if err != nil {
		t.Fatalf("gateAndMatchEmbedOnce: %v", err)
	}
	if !decision.Checkable {
		t.Fatalf("decision = %+v, want checkable", decision)
	}
	if embedder.calls != 1 {
		t.Errorf("embedder called %d times, want exactly 1", embedder.calls)
	}
}

func TestGateAndMatchEmbedOnceSkipsWithoutEmbeddingNonClaims(t *testing.T) {
	t.Parallel()
	// A non-claim is declined by the classifier before any embedding: coverage and
	// match never run, so the embedder is never called - the cheap heuristic still
	// filters filler for free.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	coverage := newCombined(t, embedder, &fakeSearcher{hits: claimHits(0.1)}, &fakeEvidence{},
		CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiEnabled: false})
	matcher := embedOnceMatcherAdapter(t, embedder, &fakeSearcher{}, &fakeEvidence{})

	_, decision, err := gateAndMatchEmbedOnce(t.Context(), stubClassifier{checkable: false}, coverage, matcher, "small talk")
	if err != nil {
		t.Fatalf("gateAndMatchEmbedOnce: %v", err)
	}
	if decision.Reason != domain.SkipReasonNotAClaim {
		t.Errorf("skip reason = %v, want not_a_claim", decision.Reason)
	}
	if embedder.calls != 0 {
		t.Errorf("embedder called %d times, want 0 (declined before embed)", embedder.calls)
	}
}

func TestGateAndMatchEmbedOnceUncoveredSkipsMatch(t *testing.T) {
	t.Parallel()
	// A claim no corpus grounds is skipped as not_covered after the single embed;
	// the match never runs, mirroring the two-embed gate's precision-over-recall
	// short-circuit.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	coverage := newCombined(t, embedder, &fakeSearcher{hits: claimHits(0.9)}, &fakeEvidence{},
		CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiEnabled: false})
	matcher := embedOnceMatcherAdapter(t, embedder, &fakeSearcher{}, &fakeEvidence{})

	_, decision, err := gateAndMatchEmbedOnce(t.Context(), stubClassifier{checkable: true}, coverage, matcher, "a novel uncovered claim")
	if err != nil {
		t.Fatalf("gateAndMatchEmbedOnce: %v", err)
	}
	if decision.Reason != domain.SkipReasonNotCovered {
		t.Errorf("skip reason = %v, want not_covered", decision.Reason)
	}
	if embedder.calls != 1 {
		t.Errorf("embedder called %d times, want 1 (embedded for coverage, no match)", embedder.calls)
	}
}

func TestLiveAnalyzerLegacyPathEmbedsUnitOnce(t *testing.T) {
	t.Parallel()
	// End-to-end through the analyzer wiring: a live legacy unit (verify path off)
	// with the production shape - a *Gate whose coverage embeds and the segment
	// match adapter - embeds the checkable unit exactly once. This asserts the
	// analyzer actually selects the single-embed path, not just that the
	// orchestration exists.
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	coverage := newCombined(t, embedder, &fakeSearcher{hits: claimHits(0.1)}, &fakeEvidence{},
		CoverageConfig{ClaimsThreshold: testClaimsThreshold, WikiEnabled: false})
	gate := NewGate(stubClassifier{checkable: true}, coverage)
	matcher := embedOnceMatcherAdapter(t, embedder, &fakeSearcher{hits: claimHits(0.1)}, &fakeEvidence{})

	stream := &fakeSegmentStream{transcripts: finalize(
		domain.Segment{Start: time.Second, End: 2 * time.Second, Text: "unemployment fell to four percent.", Speaker: "A"},
	)}
	a, err := NewLiveAnalyzer(LiveAnalyzerConfig{
		Stream: stream, Matcher: matcher, Prechecker: gate, Logger: discardLogger(), Concurrency: 1,
	})
	if err != nil {
		t.Fatalf("NewLiveAnalyzer: %v", err)
	}
	out, err := a.Run(t.Context(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = drainLiveEvents(t, out)
	if embedder.calls != 1 {
		t.Errorf("embedder called %d times for one checkable unit, want 1", embedder.calls)
	}
}
