package service

import (
	"context"
	"errors"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// segmentMatcher is the slice of *Matcher the adapter consumes: ranked matches
// for one segment's text, the confidence aggregated over them, and each match's
// contribution to that confidence. All three run on the rich Match values (which
// carry the chunk kind the wire-shaped domain.SegmentMatch drops), so they are
// computed here, before conversion. Contributions returns one weight per match,
// in match order, so the adapter can attach each to its converted match.
type segmentMatcher interface {
	MatchSegment(ctx context.Context, segment string) ([]Match, []float32, error)
	MatchSegmentVec(ctx context.Context, segment string, vec []float32) ([]Match, []float32, error)
	Confidence(matches []Match) domain.Confidence
	Contributions(matches []Match) []float64
}

// SegmentMatchAdapter adapts a *Matcher to the processing pipeline's
// SegmentMatcher port: it converts the rich Match values the match service
// returns into the persisted, wire-shaped domain.SegmentMatch the pipeline
// stores and serves verbatim.
type SegmentMatchAdapter struct {
	matcher segmentMatcher
}

// NewSegmentMatchAdapter wraps matcher so the processing pipeline can consume
// it through the domain.SegmentMatch contract.
func NewSegmentMatchAdapter(matcher segmentMatcher) *SegmentMatchAdapter {
	return &SegmentMatchAdapter{matcher: matcher}
}

// Match returns the ranked matches for text as a MatchResult: domain.SegmentMatch
// values nearest first, plus the confidence aggregated over the cluster. Curated
// claims keep their verdict and sources; Wikipedia evidence carries its article
// attribution and no verdict. A segment with no matchable text yields no matches
// and a zero confidence rather than failing the run; the matches slice is empty,
// never nil.
func (a *SegmentMatchAdapter) Match(ctx context.Context, text string) (MatchResult, error) {
	hits, query, err := a.matcher.MatchSegment(ctx, text)
	return a.convert(hits, query, err)
}

// MatchVec is Match with the query embedding supplied by the caller: it searches
// against the precomputed vector instead of embedding text again, so the legacy
// path's coverage gate and matcher share one embedding. The converted result is
// identical to Match for the same query.
func (a *SegmentMatchAdapter) MatchVec(ctx context.Context, text string, vec []float32) (MatchResult, error) {
	hits, query, err := a.matcher.MatchSegmentVec(ctx, text, vec)
	return a.convert(hits, query, err)
}

// convert turns the matcher's rich hits into the wire-shaped MatchResult, shared
// by Match and MatchVec so both surface identical matches, confidence, and the
// reusable query embedding. An empty segment (or empty vector) is a no-match, not
// a failure, matching the pre-existing Match contract.
func (a *SegmentMatchAdapter) convert(hits []Match, query []float32, err error) (MatchResult, error) {
	if err != nil {
		if errors.Is(err, ErrEmptySegment) || errors.Is(err, ErrEmptyEmbedding) {
			return MatchResult{Matches: []domain.SegmentMatch{}}, nil
		}
		return MatchResult{}, err
	}
	// Contributions is parallel to hits (one weight per match, in order), so the
	// weight that fed the score travels with its converted match. Guarded by index
	// against a contract violation so a short slice can never panic the live path.
	contributions := a.matcher.Contributions(hits)
	matches := make([]domain.SegmentMatch, 0, len(hits))
	for i, h := range hits {
		var contribution float64
		if i < len(contributions) {
			contribution = contributions[i]
		}
		sm := domain.SegmentMatch{
			Kind:         h.Kind,
			Claim:        h.Text,
			Sources:      []domain.Source{},
			Similarity:   h.Score,
			Contribution: contribution,
			EvidenceID:   h.EvidenceID,
		}
		if h.Kind == domain.MatchKindEvidence {
			article := h.Article
			sm.Article = &article
		} else {
			sm.Verdict = h.Verdict
			if h.Sources != nil {
				sm.Sources = h.Sources
			}
		}
		matches = append(matches, sm)
	}
	return MatchResult{Matches: matches, Confidence: a.matcher.Confidence(hits), QueryEmbedding: query}, nil
}
