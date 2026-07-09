package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
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
// approximate nearest-neighbor retrieval over the curated claims corpus.
type ClaimSearcher interface {
	Search(ctx context.Context, query []float32, topK, efSearch int) ([]domain.ClaimMatch, error)
}

// EvidenceSearcher is the slice of the store the matcher needs for the
// Wikipedia corpus: approximate nearest-neighbor retrieval of supporting
// evidence. Kept separate from ClaimSearcher so the two corpora stay
// independently swappable.
type EvidenceSearcher interface {
	SearchEvidence(ctx context.Context, query []float32, topK, efSearch int, sources []string) ([]domain.EvidenceHit, error)
}

// ErrEmptySegment is returned when a segment contains no text to match.
var ErrEmptySegment = errors.New("service: segment text is empty")

// Match is one fact-check hit for a transcript segment. Kind tells a curated
// claim (with Verdict and Sources) from Wikipedia evidence (with Article and no
// verdict). Text is the matched reference text - the claim statement or the
// article excerpt. WikiKind is the chunk classification of an evidence hit (lead
// or body), empty for a claim; confidence scoring weights evidence by it. Score
// is cosine similarity in [-1, 1]; higher is more similar.
//
// EvidenceID is the passage's stable source coordinate (domain.ComposeEvidenceID
// over kind + source id + chunk index): a curated claim's own id at chunk 0, a
// Wikipedia chunk's page id and chunk index. It lets a downstream verifier cite
// a passage by id and have that citation round-trip back to the exact source row
// via domain.ParseEvidenceID.
type Match struct {
	Kind       domain.MatchKind
	ClaimID    string
	Text       string
	Verdict    domain.Verdict
	Sources    []domain.Source
	Article    domain.Article
	WikiKind   domain.EvidenceChunkKind
	EvidenceID string
	Score      float64
}

// MatcherConfig bounds a Matcher. TopK and ScoreThreshold govern the curated
// claims corpus; EvidenceTopK and EvidenceThreshold govern the Wikipedia corpus
// (a higher evidence threshold is sensible since that corpus is far larger).
// EvidenceTopK 0 disables evidence retrieval entirely. MaxResults caps the
// merged, similarity-ranked output. EmbedConcurrency caps in-flight embedding
// API calls across all segments; Timeout bounds one segment end to end.
//
// ConfidenceClusterSize caps how many of the strongest matches feed the
// corroboration score; ConfidenceLeadWeight and ConfidenceBodyWeight scale a
// Wikipedia evidence hit's weight by its chunk kind (a lead summary outweighs
// buried body prose). Both weights live in [0, 1] - a curated claim is the unit
// weight and evidence corroborates no more strongly than that.
type MatcherConfig struct {
	TopK                  int
	ScoreThreshold        float64
	EvidenceTopK          int
	EvidenceThreshold     float64
	MaxResults            int
	EmbedConcurrency      int
	Timeout               time.Duration
	ConfidenceClusterSize int
	ConfidenceLeadWeight  float64
	ConfidenceBodyWeight  float64
}

func (c MatcherConfig) validate() error {
	switch {
	case c.TopK < 1 || c.TopK > math.MaxInt32:
		return fmt.Errorf("service: matcher topK must be in [1, %d], got %d", math.MaxInt32, c.TopK)
	case !domain.ValidCosineThreshold(c.ScoreThreshold):
		return fmt.Errorf("service: matcher score threshold %v outside cosine similarity range [-1, 1]", c.ScoreThreshold)
	case c.EvidenceTopK < 0 || c.EvidenceTopK > math.MaxInt32:
		return fmt.Errorf("service: matcher evidence topK must be in [0, %d], got %d", math.MaxInt32, c.EvidenceTopK)
	case !(c.EvidenceThreshold >= -1 && c.EvidenceThreshold <= 1):
		return fmt.Errorf("service: matcher evidence threshold %v outside cosine similarity range [-1, 1]", c.EvidenceThreshold)
	case c.MaxResults < 1 || c.MaxResults > math.MaxInt32:
		return fmt.Errorf("service: matcher max results must be in [1, %d], got %d", math.MaxInt32, c.MaxResults)
	case c.EmbedConcurrency < 1:
		return fmt.Errorf("service: matcher embed concurrency must be at least 1, got %d", c.EmbedConcurrency)
	case c.Timeout <= 0:
		return fmt.Errorf("service: matcher timeout must be positive, got %s", c.Timeout)
	case c.ConfidenceClusterSize < 1 || c.ConfidenceClusterSize > math.MaxInt32:
		return fmt.Errorf("service: matcher confidence cluster size must be in [1, %d], got %d", math.MaxInt32, c.ConfidenceClusterSize)
	case !validWeight(c.ConfidenceLeadWeight):
		return fmt.Errorf("service: matcher confidence lead weight %v outside [0, 1]", c.ConfidenceLeadWeight)
	case !validWeight(c.ConfidenceBodyWeight):
		return fmt.Errorf("service: matcher confidence body weight %v outside [0, 1]", c.ConfidenceBodyWeight)
	}
	return nil
}

// validWeight reports whether w is a usable corroboration weight: a real number
// in [0, 1]. The inverted comparison also rejects NaN, which would otherwise
// poison every score it touched.
func validWeight(w float64) bool {
	return w >= 0 && w <= 1
}

// Matcher turns transcript segments into ranked fact-check matches: embed the
// segment as a retrieval query, search both the curated claims and the
// Wikipedia evidence corpora, keep hits at or above each corpus's threshold,
// and merge them by similarity into a single ranked, capped result.
type Matcher struct {
	embedder QueryEmbedder
	claims   ClaimSearcher
	evidence EvidenceSearcher
	cfg      MatcherConfig
	embedSem chan struct{}
}

// NewMatcher builds a Matcher over the given embedder and corpora, failing on a
// configuration that would make matching meaningless.
func NewMatcher(embedder QueryEmbedder, claims ClaimSearcher, evidence EvidenceSearcher, cfg MatcherConfig) (*Matcher, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Matcher{
		embedder: embedder,
		claims:   claims,
		evidence: evidence,
		cfg:      cfg,
		embedSem: make(chan struct{}, cfg.EmbedConcurrency),
	}, nil
}

// MatchSegment embeds segment text once, searches both corpora, and returns the
// merged matches ranked by similarity (most similar first), each corpus filtered
// by its own threshold and the whole capped at MaxResults. The returned matches
// slice is empty (never nil) when nothing clears either threshold. It also
// returns the query embedding it computed, so a caller can reuse that vector
// rather than embed the same text again.
func (m *Matcher) MatchSegment(ctx context.Context, segment string) ([]Match, []float32, error) {
	if strings.TrimSpace(segment) == "" {
		return nil, nil, ErrEmptySegment
	}

	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	query, err := m.embedSegment(ctx, segment)
	if err != nil {
		return nil, nil, err
	}

	matches, err := m.claimMatches(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	evidence, err := m.evidenceMatches(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	matches = append(matches, evidence...)

	// Stable sort by descending score; appending claims before evidence makes
	// claims win ties, preferring a curated verdict over supporting context.
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].Score > matches[j].Score
	})
	if len(matches) > m.cfg.MaxResults {
		matches = matches[:m.cfg.MaxResults]
	}
	return matches, query, nil
}

// Confidence aggregates a matched cluster into the statement's corroboration
// score, using the matcher's configured cluster cap and chunk-kind weights. It
// is pure over the supplied matches - the cluster MatchSegment already returned -
// and runs no further retrieval.
func (m *Matcher) Confidence(matches []Match) domain.Confidence {
	return computeConfidence(matches, confidenceParams{
		clusterSize: m.cfg.ConfidenceClusterSize,
		leadWeight:  m.cfg.ConfidenceLeadWeight,
		bodyWeight:  m.cfg.ConfidenceBodyWeight,
	})
}

// Contributions returns, in match order, the stance-bearing weight each match
// added to the cluster's Confidence, under the same cluster cap and chunk-kind
// weights the score uses. It is the per-match companion to Confidence: a caller
// can attach each weight to its match so the corroboration score is explainable
// down to the evidence that produced it. The result has one entry per input
// match.
func (m *Matcher) Contributions(matches []Match) []float64 {
	return matchContributions(matches, confidenceParams{
		clusterSize: m.cfg.ConfidenceClusterSize,
		leadWeight:  m.cfg.ConfidenceLeadWeight,
		bodyWeight:  m.cfg.ConfidenceBodyWeight,
	})
}

// claimMatches retrieves and threshold-filters curated claim hits.
func (m *Matcher) claimMatches(ctx context.Context, query []float32) ([]Match, error) {
	hits, err := m.claims.Search(ctx, query, m.cfg.TopK, 0)
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
			Kind:       domain.MatchKindClaim,
			ClaimID:    h.ID,
			Text:       h.Text,
			Verdict:    h.Verdict,
			Sources:    h.Sources,
			EvidenceID: domain.ComposeEvidenceID(domain.MatchKindClaim, h.ID, 0),
			Score:      score,
		})
	}
	return matches, nil
}

// evidenceMatches retrieves and threshold-filters Wikipedia evidence hits. It is
// a no-op (empty result, no query) when evidence retrieval is disabled.
func (m *Matcher) evidenceMatches(ctx context.Context, query []float32) ([]Match, error) {
	if m.cfg.EvidenceTopK == 0 {
		return nil, nil
	}
	hits, err := m.evidence.SearchEvidence(ctx, query, m.cfg.EvidenceTopK, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("service: search evidence: %w", err)
	}
	matches := make([]Match, 0, len(hits))
	for _, h := range hits {
		score := 1 - float64(h.Distance)
		if score < m.cfg.EvidenceThreshold {
			// Hits arrive nearest first, so everything after this is weaker.
			break
		}
		matches = append(matches, Match{
			Kind:     domain.MatchKindEvidence,
			Text:     h.Content,
			Article:  domain.Article{Title: h.Title, URL: h.URL},
			WikiKind: h.Kind,
			// The source coordinate is (source, external_id): external_id is unique
			// only within a source, so composing on external_id alone would collide
			// two sources that share a page-id space (a wiki corpus and its crawl),
			// dropping one as a duplicate downstream. source has no ':' so it
			// round-trips through ParseEvidenceID's kind:source:chunk split.
			EvidenceID: domain.ComposeEvidenceID(domain.MatchKindEvidence, h.Source+"/"+h.ExternalID, h.ChunkIndex),
			Score:      score,
		})
	}
	return matches, nil
}

// embedSegment embeds one segment as a retrieval query under the shared
// concurrency cap. The semaphore is the matcher's only addition over the shared
// embedQuery helper.
func (m *Matcher) embedSegment(ctx context.Context, segment string) ([]float32, error) {
	select {
	case m.embedSem <- struct{}{}:
		defer func() { <-m.embedSem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("service: waiting for embed slot: %w", ctx.Err())
	}
	return embedQuery(ctx, m.embedder, segment)
}

// embedQuery embeds one text as a retrieval query and verifies it lives in the
// pinned vector space; a dimension mismatch means the embedder disagrees with
// the store and any distance would be garbage. It is the single home of that
// invariant, shared by the matcher and the precheck coverage stage.
func embedQuery(ctx context.Context, embedder QueryEmbedder, text string) ([]float32, error) {
	vecs, err := embedder.EmbedQueries(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("service: embed query: %w", err)
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("service: embed query: got %d embeddings, want 1", len(vecs))
	}
	if len(vecs[0]) != domain.EmbeddingDim {
		return nil, fmt.Errorf("service: embed query: embedding has %d dims, want %d", len(vecs[0]), domain.EmbeddingDim)
	}
	return vecs[0], nil
}
