package service

import (
	"context"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type stubClassifier struct{ checkable bool }

func (s stubClassifier) Classify(string) bool { return s.checkable }

// stubCoverage is a CoverageDecider whose verdict and error the gate tests set
// directly, so they exercise the gate's stage-two policy without a corpus.
type stubCoverage struct {
	covered bool
	err     error
}

func (s stubCoverage) Covered(context.Context, string) (bool, error) {
	return s.covered, s.err
}

func TestGateSkipsNonClaim(t *testing.T) {
	t.Parallel()
	// A non-claim is skipped before coverage is ever consulted: the decider
	// here would pass, but claim-worthiness fails first.
	g := NewGate(stubClassifier{checkable: false}, stubCoverage{covered: true})
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
	g := NewGate(stubClassifier{checkable: true}, stubCoverage{covered: false})
	got, err := g.Evaluate(t.Context(), "a claim no corpus covers")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	want := domain.Skipped(domain.SkipReasonNotCovered)
	if got != want {
		t.Errorf("Evaluate = %+v, want %+v", got, want)
	}
}

func TestGatePassesCoveredClaim(t *testing.T) {
	t.Parallel()
	g := NewGate(stubClassifier{checkable: true}, stubCoverage{covered: true})
	got, err := g.Evaluate(t.Context(), "a well-covered factual claim")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got != domain.Checkable() {
		t.Errorf("Evaluate = %+v, want checkable", got)
	}
}

func TestGatePropagatesCoverageError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("coverage down")
	g := NewGate(stubClassifier{checkable: true}, stubCoverage{err: sentinel})
	_, err := g.Evaluate(t.Context(), "a claim")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Evaluate err = %v, want wrapping %v", err, sentinel)
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
