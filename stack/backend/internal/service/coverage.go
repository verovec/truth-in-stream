package service

import (
	"context"
	"fmt"
	"strings"
)

// CoverageRetriever answers the gate's coverage question - "is this claim in
// the corpus at all?" - by embedding the segment as a retrieval query and
// taking the single nearest claim's similarity. It asks a lower-bar question
// than the matcher: coverage decides whether to check, the matcher decides the
// verdict, so it ignores the match score threshold and never drops a hit.
//
// It reuses the same embedder and searcher as matching, so coverage stays
// corpus-agnostic: any ClaimSearcher (curated claims today, a larger corpus
// later) satisfies it without changing gate logic.
type CoverageRetriever struct {
	embedder QueryEmbedder
	store    ClaimSearcher
}

// NewCoverageRetriever wraps the query embedder and claim searcher the coverage
// stage shares with the matcher.
func NewCoverageRetriever(embedder QueryEmbedder, store ClaimSearcher) *CoverageRetriever {
	return &CoverageRetriever{embedder: embedder, store: store}
}

// coverageTopK fetches only the single nearest claim: coverage needs the best
// similarity, not a ranked list.
const coverageTopK = 1

// TopSimilarity returns the best cosine similarity of text against the corpus
// and whether the corpus had anything to compare against. Blank text covers
// nothing and is never embedded.
func (r *CoverageRetriever) TopSimilarity(ctx context.Context, text string) (float64, bool, error) {
	if strings.TrimSpace(text) == "" {
		return 0, false, nil
	}

	query, err := embedQuery(ctx, r.embedder, text)
	if err != nil {
		return 0, false, err
	}

	hits, err := r.store.Search(ctx, query, coverageTopK)
	if err != nil {
		return 0, false, fmt.Errorf("service: coverage search: %w", err)
	}
	if len(hits) == 0 {
		return 0, false, nil
	}
	return 1 - float64(hits[0].Distance), true, nil
}
