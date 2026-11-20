package service

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// MatchResult is one statement's fact-check outcome from the matcher: its
// ranked matches (most similar first) and the confidence aggregated over them.
// The confidence is meaningful only alongside its matches, so the two travel
// together rather than as separate return values that a caller could pair up
// wrongly.
//
// QueryEmbedding is the retrieval vector the matcher already embedded for this
// statement, surfaced so a consumer (live intra-speaker consistency detection)
// can reuse it for similarity comparison instead of paying a second embedding
// call. It is nil when the matcher embedded nothing (an empty segment); the
// fact-check fields above are unaffected by its presence.
type MatchResult struct {
	Matches        []domain.SegmentMatch
	Confidence     domain.Confidence
	QueryEmbedding []float32
}

// SegmentMatcher is the slice of the embed-and-match service the live pipeline
// consumes: a statement's ranked matches and their aggregated confidence.
type SegmentMatcher interface {
	Match(ctx context.Context, text string) (MatchResult, error)
}

// SegmentPrechecker is the check-worthiness gate the pipeline consults before
// matching: it decides whether a segment is a checkable, corpus-covered claim
// worth a verdict, or one to skip with a reason. Skipped segments never reach
// the matcher, so no verdict is ever emitted on un-checkable speech.
type SegmentPrechecker interface {
	Evaluate(ctx context.Context, text string) (domain.PrecheckDecision, error)
}

// embeddingCoverage is the coverage stage's embed-once capability: it embeds a
// query and decides coverage from a precomputed vector, so the classify -> embed
// -> cover -> match sequence can share one embedding. CombinedCoverage satisfies
// it; a coverage decider that cannot expose its embedding simply keeps the
// two-embed gateAndMatch path.
type embeddingCoverage interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	CoveredVec(ctx context.Context, vec []float32) (bool, error)
}

// embedOnceMatcher is a matcher that can search from a precomputed query vector,
// so the legacy path reuses the coverage stage's embedding instead of embedding
// the same unit a second time. SegmentMatchAdapter satisfies it.
type embedOnceMatcher interface {
	MatchVec(ctx context.Context, text string, vec []float32) (MatchResult, error)
}

// gateAndMatchEmbedOnce is the single-embed variant of gateAndMatch for the
// legacy path: it classifies the unit (no embedding), and only for a claim
// embeds the text once, reusing that one vector for both the coverage decision
// and the match. It returns the same (MatchResult, decision) shape as
// gateAndMatch - a not_a_claim or not_covered skip carries the zero MatchResult -
// so a caller cannot tell the two apart except by the embedding count. The
// single embed is the behavior this card collapses: the former path embedded
// once in coverage and again in the matcher.
func gateAndMatchEmbedOnce(ctx context.Context, classifier ClaimClassifier, coverage embeddingCoverage, matcher embedOnceMatcher, text string) (MatchResult, domain.PrecheckDecision, error) {
	claim, err := classifier.Classify(ctx, text)
	if err != nil {
		return MatchResult{}, domain.PrecheckDecision{}, fmt.Errorf("precheck: %w", err)
	}
	if !claim {
		return MatchResult{}, domain.Skipped(domain.SkipReasonNotAClaim), nil
	}
	vec, err := coverage.EmbedQuery(ctx, text)
	if err != nil {
		return MatchResult{}, domain.PrecheckDecision{}, fmt.Errorf("precheck: %w", err)
	}
	covered, err := coverage.CoveredVec(ctx, vec)
	if err != nil {
		return MatchResult{}, domain.PrecheckDecision{}, fmt.Errorf("precheck: %w", err)
	}
	if !covered {
		return MatchResult{}, domain.Skipped(domain.SkipReasonNotCovered), nil
	}
	result, err := matcher.MatchVec(ctx, text, vec)
	if err != nil {
		return MatchResult{}, domain.PrecheckDecision{}, fmt.Errorf("match: %w", err)
	}
	return result, domain.Checkable(), nil
}

// gateAndMatch is the check-worthiness core of the live pipeline: it runs the
// precheck gate, then matches only checkable segments, so a single skip-vs-check
// policy governs every verdict. It returns the precheck decision (whose
// Checkable flag is the authoritative skip-vs-check signal) alongside the match
// result, which is the zero MatchResult (no matches, zero confidence) for a
// skipped segment.
func gateAndMatch(ctx context.Context, prechecker SegmentPrechecker, matcher SegmentMatcher, text string) (MatchResult, domain.PrecheckDecision, error) {
	decision, err := prechecker.Evaluate(ctx, text)
	if err != nil {
		return MatchResult{}, domain.PrecheckDecision{}, fmt.Errorf("precheck: %w", err)
	}
	if !decision.Checkable {
		return MatchResult{}, decision, nil
	}
	result, err := matcher.Match(ctx, text)
	if err != nil {
		return MatchResult{}, domain.PrecheckDecision{}, fmt.Errorf("match: %w", err)
	}
	return result, decision, nil
}
