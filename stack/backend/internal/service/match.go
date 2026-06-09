package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// QueryEmbedder is the slice of the embedding client the matcher needs:
// embedding retrieval queries (input_type=query). The claims were ingested
// with input_type=document; mixing the two sides up skews scores, so the
// matcher never sees the document method.
type QueryEmbedder interface {
	EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)
}

// ClaimSearcher is the slice of the claim store the matcher needs:
// approximate nearest-neighbor retrieval.
type ClaimSearcher interface {
	Search(ctx context.Context, query []float32, topK int) ([]domain.ClaimMatch, error)
}

// ErrEmptySegment is returned when a segment contains no text to match.
var ErrEmptySegment = errors.New("service: segment text is empty")

// Match is one fact-check hit for a transcript segment. Score is cosine
// similarity in [-1, 1]; higher is more similar.
type Match struct {
	ClaimID string
	Text    string
	Verdict domain.Verdict
	Sources []domain.Source
	Score   float64
}

// MatcherConfig bounds a Matcher. TopK is the number of nearest claims
// fetched per segment; ScoreThreshold is the minimum cosine similarity a
// match must reach to surface; EmbedConcurrency caps in-flight embedding API
// calls across all segments; Timeout bounds one segment end to end.
type MatcherConfig struct {
	TopK             int
	ScoreThreshold   float64
	EmbedConcurrency int
	Timeout          time.Duration
}

func (c MatcherConfig) validate() error {
	switch {
	case c.TopK < 1:
		return fmt.Errorf("service: matcher topK must be at least 1, got %d", c.TopK)
	case c.ScoreThreshold < -1 || c.ScoreThreshold > 1:
		return fmt.Errorf("service: matcher score threshold %v outside cosine similarity range [-1, 1]", c.ScoreThreshold)
	case c.EmbedConcurrency < 1:
		return fmt.Errorf("service: matcher embed concurrency must be at least 1, got %d", c.EmbedConcurrency)
	case c.Timeout <= 0:
		return fmt.Errorf("service: matcher timeout must be positive, got %s", c.Timeout)
	}
	return nil
}

// Matcher turns transcript segments into ranked fact-check matches: embed the
// segment as a retrieval query, search the claim store for the nearest
// claims, and keep those at or above the score threshold.
type Matcher struct {
	embedder QueryEmbedder
	store    ClaimSearcher
	cfg      MatcherConfig
	embedSem chan struct{}
}

// NewMatcher builds a Matcher over the given embedder and store, failing on a
// configuration that would make matching meaningless.
func NewMatcher(embedder QueryEmbedder, store ClaimSearcher, cfg MatcherConfig) (*Matcher, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Matcher{
		embedder: embedder,
		store:    store,
		cfg:      cfg,
		embedSem: make(chan struct{}, cfg.EmbedConcurrency),
	}, nil
}

// MatchSegment embeds segment text and returns the claims most similar to it,
// nearest first, dropping matches below the configured score threshold. The
// returned slice is empty (never nil) when nothing clears the threshold.
func (m *Matcher) MatchSegment(ctx context.Context, segment string) ([]Match, error) {
	if strings.TrimSpace(segment) == "" {
		return nil, ErrEmptySegment
	}

	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	query, err := m.embedSegment(ctx, segment)
	if err != nil {
		return nil, err
	}

	hits, err := m.store.Search(ctx, query, m.cfg.TopK)
	if err != nil {
		return nil, fmt.Errorf("service: search claims: %w", err)
	}

	matches := make([]Match, 0, len(hits))
	for _, h := range hits {
		score := 1 - float64(h.Distance)
		if score < m.cfg.ScoreThreshold {
			// Hits arrive nearest first, so everything after this is weaker.
			break
		}
		matches = append(matches, Match{
			ClaimID: h.ID,
			Text:    h.Text,
			Verdict: h.Verdict,
			Sources: h.Sources,
			Score:   score,
		})
	}
	return matches, nil
}

// embedSegment embeds one segment as a retrieval query under the shared
// concurrency cap and verifies the result lives in the pinned vector space; a
// dimension mismatch means the embedder disagrees with the store and any
// distance would be garbage.
func (m *Matcher) embedSegment(ctx context.Context, segment string) ([]float32, error) {
	select {
	case m.embedSem <- struct{}{}:
		defer func() { <-m.embedSem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("service: waiting for embed slot: %w", ctx.Err())
	}

	vecs, err := m.embedder.EmbedQueries(ctx, []string{segment})
	if err != nil {
		return nil, fmt.Errorf("service: embed segment: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("service: embed segment: got %d embeddings, want 1", len(vecs))
	}
	if len(vecs[0]) != domain.EmbeddingDim {
		return nil, fmt.Errorf("service: embed segment: embedding has %d dims, want %d", len(vecs[0]), domain.EmbeddingDim)
	}
	return vecs[0], nil
}
