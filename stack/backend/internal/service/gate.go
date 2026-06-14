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
