package service

import (
	"context"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type stubClassifier struct{ checkable bool }

func (s stubClassifier) Classify(string) bool { return s.checkable }

type stubRetriever struct {
	score float64
	found bool
	err   error
}

func (s stubRetriever) TopSimilarity(context.Context, string) (float64, bool, error) {
	return s.score, s.found, s.err
}

// testCoverageThreshold is the coverage floor every gate test builds against;
// the cases vary the retriever score around it rather than the threshold.
const testCoverageThreshold = 0.4

func newTestGate(t *testing.T, c ClaimClassifier, r CorpusRetriever) *Gate {
	t.Helper()
	g, err := NewGate(c, r, GateConfig{CoverageThreshold: testCoverageThreshold})
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	return g
}

func TestGateSkipsNonClaim(t *testing.T) {
	t.Parallel()
	// A non-claim is skipped before coverage is ever consulted: the retriever
	// here would pass, but claim-worthiness fails first.
	g := newTestGate(t, stubClassifier{checkable: false}, stubRetriever{score: 1, found: true})
	got, err := g.Evaluate(t.Context(), "is this a question")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	want := domain.Skipped(domain.SkipReasonNotAClaim)
	if got != want {
		t.Errorf("Evaluate = %+v, want %+v", got, want)
	}
}

func TestGateSkipsUncovered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		score float64
		found bool
	}{
		{"below threshold", 0.30, true},
		{"nothing retrieved", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newTestGate(t, stubClassifier{checkable: true}, stubRetriever{score: tc.score, found: tc.found})
			got, err := g.Evaluate(t.Context(), "a claim the corpus does not cover")
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			want := domain.Skipped(domain.SkipReasonNotCovered)
			if got != want {
				t.Errorf("Evaluate = %+v, want %+v", got, want)
			}
		})
	}
}

func TestGatePassesCoveredClaim(t *testing.T) {
	t.Parallel()
	// At-threshold is covered: the boundary is inclusive.
	g := newTestGate(t, stubClassifier{checkable: true}, stubRetriever{score: testCoverageThreshold, found: true})
	got, err := g.Evaluate(t.Context(), "a well-covered factual claim")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got != domain.Checkable() {
		t.Errorf("Evaluate = %+v, want checkable", got)
	}
}

func TestGatePropagatesRetrieverError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("retrieval down")
	g := newTestGate(t, stubClassifier{checkable: true}, stubRetriever{err: sentinel})
	_, err := g.Evaluate(t.Context(), "a claim")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Evaluate err = %v, want wrapping %v", err, sentinel)
	}
}

func TestNewGateRejectsBadThreshold(t *testing.T) {
	t.Parallel()
	for _, threshold := range []float64{-1.5, 1.5} {
		if _, err := NewGate(stubClassifier{}, stubRetriever{}, GateConfig{CoverageThreshold: threshold}); err == nil {
			t.Errorf("NewGate(threshold=%v) err = nil, want error", threshold)
		}
	}
}

func TestAllowAllPrecheckerChecksEverything(t *testing.T) {
	t.Parallel()
	got, err := allowAllPrechecker{}.Evaluate(t.Context(), "anything at all")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got != domain.Checkable() {
		t.Errorf("allow-all Evaluate = %+v, want checkable", got)
	}
}
