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

// CorpusRetriever reports the best similarity of a segment to the reference
// corpus. It is deliberately corpus-agnostic - any store that can rank text by
// similarity satisfies it - so growing or swapping the corpus changes coverage
// decisions with no change to gate logic. found is false when the corpus
// returns nothing to compare against.
type CorpusRetriever interface {
	TopSimilarity(ctx context.Context, text string) (score float64, found bool, err error)
}

// GateConfig bounds a Gate. CoverageThreshold is the minimum corpus similarity
// (cosine, [-1, 1]) a claim must reach to be worth checking; below it the
// segment is skipped as not covered rather than matched and forced into a
// verdict.
type GateConfig struct {
	CoverageThreshold float64
}

// Gate is the check-worthiness precheck: a two-stage filter, cheap to
// expensive, in front of the matcher. Stage one rejects non-claims; stage two
// rejects claims the corpus cannot ground. Only segments that clear both are
// checkable. The bias is precision over recall throughout: when in doubt, skip.
type Gate struct {
	classifier ClaimClassifier
	retriever  CorpusRetriever
	threshold  float64
}

// NewGate builds a Gate, rejecting a coverage threshold outside cosine
// similarity's [-1, 1] range, which would make the coverage stage meaningless.
func NewGate(classifier ClaimClassifier, retriever CorpusRetriever, cfg GateConfig) (*Gate, error) {
	if !domain.ValidCosineThreshold(cfg.CoverageThreshold) {
		return nil, fmt.Errorf("service: gate coverage threshold %v outside cosine similarity range [-1, 1]", cfg.CoverageThreshold)
	}
	return &Gate{
		classifier: classifier,
		retriever:  retriever,
		threshold:  cfg.CoverageThreshold,
	}, nil
}

// Evaluate runs the two stages and returns the decision. A non-claim is
// declined before the corpus is consulted; a claim the corpus does not cover
// at or above the threshold is declined as not covered; everything else is
// checkable.
func (g *Gate) Evaluate(ctx context.Context, text string) (domain.PrecheckDecision, error) {
	if !g.classifier.Classify(text) {
		return domain.Skipped(domain.SkipReasonNotAClaim), nil
	}

	score, found, err := g.retriever.TopSimilarity(ctx, text)
	if err != nil {
		return domain.PrecheckDecision{}, fmt.Errorf("service: precheck coverage: %w", err)
	}
	if !found || score < g.threshold {
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
