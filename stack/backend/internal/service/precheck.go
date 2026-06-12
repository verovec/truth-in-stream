package service

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ClaimClassifier decides whether a transcript segment is a checkable,
// declarative factual assertion. It is the cheap first stage of the gate;
// HeuristicClassifier is the default implementation and a model-based one can
// take its place without touching the gate.
type ClaimClassifier interface {
	Classify(text string) bool
}

// CoverageDecider answers the gate's coverage question - "is this segment
// grounded by any reference corpus?" - returning covered=true when at least one
// corpus clears its own threshold. The per-corpus thresholds live with the
// decider, so the gate stays corpus-agnostic: growing, swapping, or adding a
// corpus changes coverage decisions with no change to gate logic.
type CoverageDecider interface {
	Covered(ctx context.Context, text string) (bool, error)
}

// Gate is the check-worthiness precheck: a two-stage filter, cheap to
// expensive, in front of the matcher. Stage one rejects non-claims; stage two
// rejects claims no reference corpus can ground. Only segments that clear both
// are checkable. The bias is precision over recall throughout: when in doubt,
// skip.
type Gate struct {
	classifier ClaimClassifier
	coverage   CoverageDecider
}

// NewGate builds a Gate from a claim classifier and a coverage decider. Both
// stages' validation lives with their own constructors, so the gate itself
// cannot be misconfigured.
func NewGate(classifier ClaimClassifier, coverage CoverageDecider) *Gate {
	return &Gate{classifier: classifier, coverage: coverage}
}

// Evaluate runs the two stages and returns the decision. A non-claim is
// declined before any corpus is consulted; a claim no corpus covers is declined
// as not covered; everything else is checkable.
func (g *Gate) Evaluate(ctx context.Context, text string) (domain.PrecheckDecision, error) {
	if !g.classifier.Classify(text) {
		return domain.Skipped(domain.SkipReasonNotAClaim), nil
	}

	covered, err := g.coverage.Covered(ctx, text)
	if err != nil {
		return domain.PrecheckDecision{}, fmt.Errorf("service: precheck coverage: %w", err)
	}
	if !covered {
		return domain.Skipped(domain.SkipReasonNotCovered), nil
	}
	return domain.Checkable(), nil
}

// allowAllPrechecker is the no-op gate: every segment is checkable. It is wired
// when the precheck is disabled by configuration, so the pipeline keeps its
// pre-gate behavior with no special-casing in the processor.
type allowAllPrechecker struct{}

func (allowAllPrechecker) Evaluate(context.Context, string) (domain.PrecheckDecision, error) {
	return domain.Checkable(), nil
}
