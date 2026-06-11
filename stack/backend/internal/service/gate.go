package service

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// SegmentMatcher is the slice of the embed-and-match service the live pipeline
// consumes: ranked claim matches for one segment's text, most similar first.
type SegmentMatcher interface {
	Match(ctx context.Context, text string) ([]domain.SegmentMatch, error)
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
// Checkable flag is the authoritative skip-vs-check signal) alongside the
// matches, which are nil for a skipped segment.
func gateAndMatch(ctx context.Context, prechecker SegmentPrechecker, matcher SegmentMatcher, text string) ([]domain.SegmentMatch, domain.PrecheckDecision, error) {
	decision, err := prechecker.Evaluate(ctx, text)
	if err != nil {
		return nil, domain.PrecheckDecision{}, fmt.Errorf("precheck: %w", err)
	}
	if !decision.Checkable {
		return nil, decision, nil
	}
	matches, err := matcher.Match(ctx, text)
	if err != nil {
		return nil, domain.PrecheckDecision{}, fmt.Errorf("match: %w", err)
	}
	return matches, decision, nil
}
