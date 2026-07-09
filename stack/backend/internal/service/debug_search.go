package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// WikiHit is one nearest-neighbor result from the embedded Wikipedia corpus,
// returned by the developer search probe. Similarity is cosine similarity in
// [-1, 1] (1 - distance); higher is more similar. Unlike the matcher's Match it
// carries no verdict and is subject to no threshold - the probe reports the raw
// neighbors so a developer can see exactly what the corpus returns.
type WikiHit struct {
	Title      string
	URL        string
	Content    string
	Similarity float64
}

// WikiSearchConfig bounds a WikiSearch. TopK caps the neighbors returned;
// Timeout bounds one query (embed plus search) end to end.
type WikiSearchConfig struct {
	TopK    int
	Timeout time.Duration
}

func (c WikiSearchConfig) validate() error {
	switch {
	case c.TopK < 1 || c.TopK > math.MaxInt32:
		return fmt.Errorf("service: wiki search topK must be in [1, %d], got %d", math.MaxInt32, c.TopK)
	case c.Timeout <= 0:
		return fmt.Errorf("service: wiki search timeout must be positive, got %s", c.Timeout)
	}
	return nil
}

// WikiSearch is the developer search probe over the embedded Wikipedia corpus:
// embed a query (input_type=query), run the same approximate nearest-neighbor
// search the matcher's evidence corpus uses, and return the raw neighbors with
// their similarity. It applies no threshold and assigns no verdict - it exists
// to inspect what the corpus actually returns, not to fact-check. It reuses the
// matcher's QueryEmbedder and EvidenceSearcher ports so the two never drift on
// how a query is embedded or how the corpus is searched.
type WikiSearch struct {
	embedder QueryEmbedder
	evidence EvidenceSearcher
	cfg      WikiSearchConfig
}

// NewWikiSearch builds a WikiSearch over the given embedder and evidence corpus,
// failing on a configuration that would make a search meaningless.
func NewWikiSearch(embedder QueryEmbedder, evidence EvidenceSearcher, cfg WikiSearchConfig) (*WikiSearch, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &WikiSearch{embedder: embedder, evidence: evidence, cfg: cfg}, nil
}

// Search embeds the query and returns the corpus's nearest neighbors, already
// ranked most-similar-first by the store. A blank query short-circuits to an
// empty result with no embedding call, so a cleared search box costs nothing.
// The returned slice is empty, never nil.
func (s *WikiSearch) Search(ctx context.Context, query string) ([]WikiHit, error) {
	if strings.TrimSpace(query) == "" {
		return []WikiHit{}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	vec, err := embedQuery(ctx, s.embedder, query)
	if err != nil {
		return nil, err
	}
	hits, err := s.evidence.SearchEvidence(ctx, vec, s.cfg.TopK, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("service: search wiki: %w", err)
	}
	out := make([]WikiHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, WikiHit{
			Title:      h.Title,
			URL:        h.URL,
			Content:    h.Content,
			Similarity: 1 - float64(h.Distance),
		})
	}
	return out, nil
}
