package eval

import (
	"context"
	"fmt"
)

// PassageRanker ranks candidate passage texts against a claim, best first,
// returning indexes into the given documents. It is the eval-side port of the
// retrieval reranker, so a live rerank client (or a test fake) plugs into the
// same recall bookkeeping as the offline oracle without this package importing
// the client.
type PassageRanker interface {
	Rank(ctx context.Context, query string, documents []string) ([]int, error)
}

// RunRetrievalReranked scores the golden retrieval cases with the ranking
// delegated to ranker, producing a report directly comparable to
// RunRetrieval's oracle report: same cases, same recall bookkeeping, only the
// ranking differs. Unlike the matcher's fail-open production stage, a ranker
// failure here is a hard error - a comparison built on a silent fallback would
// report the oracle's numbers as the reranker's.
func RunRetrievalReranked(ctx context.Context, g Golden, ranker PassageRanker) (RetrievalReport, error) {
	return runRetrieval(g, func(c Case) ([]string, error) {
		docs := make([]string, len(c.Passages))
		for i, p := range c.Passages {
			docs[i] = p.Text
		}
		order, err := ranker.Rank(ctx, c.Statement, docs)
		if err != nil {
			return nil, err
		}
		if len(order) != len(docs) {
			return nil, fmt.Errorf("ranker returned %d indexes for %d passages", len(order), len(docs))
		}
		ranked := make([]string, len(order))
		seen := make([]bool, len(docs))
		for rank, idx := range order {
			if idx < 0 || idx >= len(docs) || seen[idx] {
				return nil, fmt.Errorf("ranker index %d invalid or duplicated over %d passages", idx, len(docs))
			}
			seen[idx] = true
			ranked[rank] = c.Passages[idx].ID
		}
		return ranked, nil
	})
}
