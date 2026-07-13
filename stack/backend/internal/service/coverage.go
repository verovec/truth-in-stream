package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// coverageTopK fetches only the single nearest hit: coverage needs the best
// similarity, not a ranked list.
const coverageTopK = 1

// defaultCoverageEfSearch raises hnsw.ef_search for the coverage probe above the
// session default: coverage decides whether a segment is checkable at all, so
// missing the true nearest neighbor is a false "not covered", and the VER-173
// benchmark showed ef_search 200 reaches full recall for a marginal latency
// cost. The political fast-path and the global matcher searches keep the
// session default (a per-call efSearch of 0), trading recall for latency
// independently, which is the point of threading ef_search per query. It is the
// library default when CoverageConfig leaves EfSearch zero; the env layer
// (PRECHECK_COVERAGE_EF_SEARCH) mirrors it and the two must stay in sync.
const defaultCoverageEfSearch = 200

// corpusCoverage reports the best cosine similarity of an already-embedded
// query vector against one reference corpus, and whether the corpus held
// anything to compare against. Probing a shared vector rather than raw text is
// what lets the combined stage embed a segment once and reuse it across every
// corpus instead of paying a duplicate embedding call per corpus.
type corpusCoverage interface {
	topSimilarity(ctx context.Context, vec []float32) (score float64, found bool, err error)
}

// claimsCoverage probes the curated claims corpus. It asks a lower-bar question
// than the matcher: coverage decides whether to check, the matcher decides the
// verdict, so it takes the single nearest claim's similarity and never drops a
// hit on the match-score threshold.
type claimsCoverage struct {
	store    ClaimSearcher
	efSearch int
}

func (c claimsCoverage) topSimilarity(ctx context.Context, vec []float32) (float64, bool, error) {
	hits, err := c.store.Search(ctx, vec, coverageTopK, c.efSearch)
	if err != nil {
		return 0, false, fmt.Errorf("service: claims coverage search: %w", err)
	}
	if len(hits) == 0 {
		return 0, false, nil
	}
	return 1 - float64(hits[0].Distance), true, nil
}

// wikiCoverage probes the embedded Wikipedia corpus. It lets the large
// knowledge base the system already ingests drive the decision to check, so a
// statement grounded by the corpus is no longer dropped just because the sparse
// curated claims table held no near-duplicate. An empty or absent corpus
// returns found=false, degrading coverage to the other sources rather than
// erroring.
type wikiCoverage struct {
	store    EvidenceSearcher
	efSearch int
}

func (w wikiCoverage) topSimilarity(ctx context.Context, vec []float32) (float64, bool, error) {
	hits, err := w.store.SearchEvidence(ctx, vec, coverageTopK, w.efSearch, nil)
	if err != nil {
		return 0, false, fmt.Errorf("service: wiki coverage search: %w", err)
	}
	if len(hits) == 0 {
		return 0, false, nil
	}
	return 1 - float64(hits[0].Distance), true, nil
}

// coverageSource is one reference corpus paired with the cosine-similarity floor
// a segment must reach in it to count as covered. Each corpus carries its own
// floor: the curated claims corpus is small and trusted (lower floor), the wiki
// corpus is large and noisier (stricter floor), so a single threshold cannot
// serve both.
type coverageSource struct {
	corpus    corpusCoverage
	threshold float64
}

// CoverageConfig bounds the combined coverage stage. ClaimsThreshold is the
// curated-claims floor; WikiThreshold the (stricter) wiki floor; WikiEnabled
// toggles the wiki corpus as a coverage source, falling back to claims-only
// coverage when off.
type CoverageConfig struct {
	ClaimsThreshold float64
	WikiThreshold   float64
	WikiEnabled     bool
	// EfSearch is the HNSW ef_search the coverage probe runs at across both
	// corpora; 0 applies defaultCoverageEfSearch (the former hard-coded 200), so a
	// caller that leaves it zero keeps the prior probe budget unchanged.
	EfSearch int
}

// CombinedCoverage is the coverage stage's decision: a segment is covered when
// ANY reference corpus holds a hit at or above that corpus's own threshold. It
// embeds the segment once and reuses the vector across every corpus, so adding
// a corpus never adds an embedding round-trip. With wiki coverage disabled it
// reproduces the original claims-only coverage exactly.
type CombinedCoverage struct {
	embedder QueryEmbedder
	sources  []coverageSource
}

// NewCombinedCoverage builds the coverage decider over the curated claims
// corpus and, when enabled, the wiki corpus. It rejects either threshold
// outside cosine similarity's [-1, 1] range, which would make a coverage stage
// meaningless. Both thresholds are validated even when wiki coverage is off, so
// a malformed wiki threshold fails fast at startup rather than lurking until
// the corpus is enabled - matching the config loader, which validates every
// supplied value regardless of the toggles.
func NewCombinedCoverage(embedder QueryEmbedder, claims ClaimSearcher, wiki EvidenceSearcher, cfg CoverageConfig) (*CombinedCoverage, error) {
	if !domain.ValidCosineThreshold(cfg.ClaimsThreshold) {
		return nil, fmt.Errorf("service: claims coverage threshold %v outside cosine similarity range [-1, 1]", cfg.ClaimsThreshold)
	}
	if !domain.ValidCosineThreshold(cfg.WikiThreshold) {
		return nil, fmt.Errorf("service: wiki coverage threshold %v outside cosine similarity range [-1, 1]", cfg.WikiThreshold)
	}
	efSearch := cfg.EfSearch
	if efSearch <= 0 {
		efSearch = defaultCoverageEfSearch
	}
	sources := []coverageSource{{corpus: claimsCoverage{store: claims, efSearch: efSearch}, threshold: cfg.ClaimsThreshold}}
	if cfg.WikiEnabled {
		sources = append(sources, coverageSource{corpus: wikiCoverage{store: wiki, efSearch: efSearch}, threshold: cfg.WikiThreshold})
	}
	return &CombinedCoverage{embedder: embedder, sources: sources}, nil
}

// Covered reports whether any reference corpus grounds the segment at or above
// its threshold. Blank text covers nothing and is never embedded. The segment
// is embedded once and the vector reused across corpora; the scan short-circuits
// on the first covering corpus, so the embedding is the only guaranteed cost.
func (c *CombinedCoverage) Covered(ctx context.Context, text string) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, nil
	}
	vec, err := embedQuery(ctx, c.embedder, text)
	if err != nil {
		return false, err
	}
	return c.coveredVec(ctx, vec)
}

// EmbedQuery embeds text as a retrieval query, exposing the coverage stage's
// embedding step so the legacy path can embed a checkable unit once and hand the
// same vector to CoveredVec and the matcher, instead of embedding it twice. It is
// the single embed the embed-once orchestration relies on.
func (c *CombinedCoverage) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return embedQuery(ctx, c.embedder, text)
}

// CoveredVec answers the coverage question against an already-embedded query
// vector, so a caller that embedded the segment for another stage reuses that
// vector here rather than paying a second embedding call. It is the vector-only
// core of Covered, which embeds first and then delegates here.
func (c *CombinedCoverage) CoveredVec(ctx context.Context, vec []float32) (bool, error) {
	return c.coveredVec(ctx, vec)
}

// coveredVec reports whether any reference corpus grounds the query vector at or
// above its threshold, short-circuiting on the first covering corpus. It is the
// shared body of Covered (which embeds first) and CoveredVec (which is handed the
// vector), so both decide coverage identically for the same vector.
func (c *CombinedCoverage) coveredVec(ctx context.Context, vec []float32) (bool, error) {
	for _, s := range c.sources {
		score, found, err := s.corpus.topSimilarity(ctx, vec)
		if err != nil {
			return false, err
		}
		if found && score >= s.threshold {
			return true, nil
		}
	}
	return false, nil
}
