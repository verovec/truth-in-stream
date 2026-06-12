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
type claimsCoverage struct{ store ClaimSearcher }

func (c claimsCoverage) topSimilarity(ctx context.Context, vec []float32) (float64, bool, error) {
	hits, err := c.store.Search(ctx, vec, coverageTopK)
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
type wikiCoverage struct{ store EvidenceSearcher }

func (w wikiCoverage) topSimilarity(ctx context.Context, vec []float32) (float64, bool, error) {
	hits, err := w.store.SearchWiki(ctx, vec, coverageTopK)
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
	sources := []coverageSource{{corpus: claimsCoverage{store: claims}, threshold: cfg.ClaimsThreshold}}
	if cfg.WikiEnabled {
		sources = append(sources, coverageSource{corpus: wikiCoverage{store: wiki}, threshold: cfg.WikiThreshold})
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
