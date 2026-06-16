package service

import (
	"context"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type stubClassifier struct {
	checkable bool
	err       error
}

func (s stubClassifier) Classify(context.Context, string) (bool, error) {
	return s.checkable, s.err
}

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

func TestGatePropagatesClassifierError(t *testing.T) {
	t.Parallel()
	// A classifier that reports failure (a model-backed one carrying a deadline
	// or transport error) surfaces from the gate rather than being swallowed into
	// a skip; coverage is never consulted.
	sentinel := errors.New("classifier down")
	g := NewGate(stubClassifier{checkable: true, err: sentinel}, stubCoverage{covered: true})
	_, err := g.Evaluate(t.Context(), "a claim")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Evaluate err = %v, want wrapping %v", err, sentinel)
	}
}

func TestClaimGateDecidesCheckWorthinessAlone(t *testing.T) {
	t.Parallel()
	// The retrieve-then-verify gate consults no corpus: a check-worthy claim is
	// checkable regardless of coverage (coverage is discovered by retrieval), and
	// a non-claim is declined as not_a_claim.
	tests := []struct {
		name       string
		classifier stubClassifier
		want       domain.PrecheckDecision
	}{
		{"a claim is checkable with no coverage lookup", stubClassifier{checkable: true}, domain.Checkable()},
		{"a non-claim is declined", stubClassifier{checkable: false}, domain.Skipped(domain.SkipReasonNotAClaim)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewClaimGate(tc.classifier).Evaluate(t.Context(), "some text")
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Evaluate = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClaimGateSurfacesClassifierError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("classify boom")
	_, err := NewClaimGate(stubClassifier{err: sentinel}).Evaluate(t.Context(), "a claim")
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
