package postgres

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// DefaultRRFConstant is the conventional Reciprocal Rank Fusion smoothing
// constant (Cormack, Clarke & Büttcher, SIGIR 2009), copied by Supabase,
// Elastic, and pgvector's own hybrid-search guide as a safe default. It only
// de-weights how much the very top rank dominates; larger flattens the rank
// contribution. Callers thread their own value (config) and fall back to this.
const DefaultRRFConstant = 60

// reciprocalRankFusion computes Reciprocal Rank Fusion scores over ordered
// candidate lists. Each list holds candidate keys best-first; a key's fused
// score is the sum over the lists of 1/(k + rank), with rank one-based (a
// list's first element is rank 1, matching the original paper), and a key
// absent from a list contributes nothing for that list. k is the smoothing
// constant and must be positive. This is the pure, database-free core of hybrid
// retrieval, kept a free function so it is unit-tested directly without a store.
func reciprocalRankFusion[K comparable](lists [][]K, k int) map[K]float64 {
	scores := make(map[K]float64)
	kf := float64(k)
	for _, list := range lists {
		for i, key := range list {
			scores[key] += 1.0 / (kf + float64(i+1))
		}
	}
	return scores
}

// fuseHybrid fuses a vector-ordered and a lexical-ordered candidate list of one
// corpus by Reciprocal Rank Fusion and returns the topK distinct hits,
// nearest-first by cosine distance. RRF (over rrfK) decides only which
// candidates survive into the topK - this is how a lexically exact passage that
// cosine ranked below the vector top-k is rescued into the result - while the
// returned ordering stays strict cosine distance ascending, so the wire-visible
// nearest-first shape and every caller's break-on-threshold contract are
// unchanged. A key present in both lists keeps its first (vector-branch) hit;
// both branches carry the same cosine distance, so the choice is immaterial.
// Ranking ties break on cosine distance then first-seen order, so the selection
// is deterministic across runs regardless of map iteration order.
func fuseHybrid[T any, K comparable](vector, lexical []T, keyOf func(T) K, distOf func(T) float32, topK, rrfK int) []T {
	type candidate struct {
		hit   T
		key   K
		dist  float32
		order int
	}

	seen := make(map[K]int, len(vector)+len(lexical))
	candidates := make([]candidate, 0, len(vector)+len(lexical))
	vecKeys := make([]K, 0, len(vector))
	lexKeys := make([]K, 0, len(lexical))

	add := func(hit T) K {
		key := keyOf(hit)
		if _, ok := seen[key]; !ok {
			seen[key] = len(candidates)
			candidates = append(candidates, candidate{hit: hit, key: key, dist: distOf(hit), order: len(candidates)})
		}
		return key
	}
	for _, h := range vector {
		vecKeys = append(vecKeys, add(h))
	}
	for _, h := range lexical {
		lexKeys = append(lexKeys, add(h))
	}

	scores := reciprocalRankFusion([][]K{vecKeys, lexKeys}, rrfK)

	byScore := make([]candidate, len(candidates))
	copy(byScore, candidates)
	sort.SliceStable(byScore, func(i, j int) bool {
		si, sj := scores[byScore[i].key], scores[byScore[j].key]
		if si != sj {
			return si > sj
		}
		if byScore[i].dist != byScore[j].dist {
			return byScore[i].dist < byScore[j].dist
		}
		return byScore[i].order < byScore[j].order
	})
	if topK < len(byScore) {
		byScore = byScore[:topK]
	}

	sort.SliceStable(byScore, func(i, j int) bool {
		if byScore[i].dist != byScore[j].dist {
			return byScore[i].dist < byScore[j].dist
		}
		return byScore[i].order < byScore[j].order
	})
	out := make([]T, len(byScore))
	for i, c := range byScore {
		out[i] = c.hit
	}
	return out
}

// evidenceKey is the natural key of an evidence chunk, used as the comparable
// fusion key so two hits of the same chunk (one from each branch) coalesce.
type evidenceKey struct {
	source     string
	externalID string
	chunkIndex int
}

// SearchHybrid returns the topK curated claims for a query by fusing the vector
// (cosine) branch with the French lexical full-text branch via Reciprocal Rank
// Fusion. text is the raw query for the lexical branch; query is its embedding
// for the vector branch. lexicalK bounds the lexical candidate pool and the
// vector branch over-fetches to at least lexicalK so a lexically rescued claim
// also earns a vector rank in the fusion. rrfK is the RRF constant (<=0 uses
// DefaultRRFConstant). The result is nearest-first by cosine distance and carries
// the same wire shape Search returns, so callers are unchanged. An empty query
// text has no lexical signal, so it falls back to the pure vector Search with no
// extra round trip.
func (s *Store) SearchHybrid(ctx context.Context, text string, query []float32, topK, lexicalK, rrfK, efSearch int) ([]domain.ClaimMatch, error) {
	if topK <= 0 || topK > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: search hybrid: topK %d out of range", topK)
	}
	if len(query) != domain.EmbeddingDim {
		return nil, fmt.Errorf("postgres: search hybrid: query has %d dims, want %d", len(query), domain.EmbeddingDim)
	}
	if strings.TrimSpace(text) == "" {
		return s.Search(ctx, query, topK, efSearch)
	}
	lexicalK, rrfK, poolK, err := hybridBounds(topK, lexicalK, rrfK)
	if err != nil {
		return nil, fmt.Errorf("postgres: search hybrid: %w", err)
	}

	vec := pgvector.NewHalfVector(query)
	var vecRows []db.SearchClaimsRow
	var lexRows []db.LexicalSearchClaimsRow
	err = s.searchTuned(ctx, efSearch, false, func(q *db.Queries) error {
		var e error
		if vecRows, e = q.SearchClaims(ctx, db.SearchClaimsParams{QueryEmbedding: vec, ResultLimit: int32(poolK)}); e != nil {
			return e
		}
		lexRows, e = q.LexicalSearchClaims(ctx, db.LexicalSearchClaimsParams{QueryEmbedding: vec, QueryText: text, ResultLimit: int32(lexicalK)})
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: search hybrid: %w", err)
	}

	vecMatches := make([]domain.ClaimMatch, 0, len(vecRows))
	for _, r := range vecRows {
		m, err := toClaimMatch(r.ID, r.Content, r.Verdict, r.Sources, r.Distance)
		if err != nil {
			return nil, fmt.Errorf("postgres: search hybrid: %w", err)
		}
		vecMatches = append(vecMatches, m)
	}
	lexMatches := make([]domain.ClaimMatch, 0, len(lexRows))
	for _, r := range lexRows {
		m, err := toClaimMatch(r.ID, r.Content, r.Verdict, r.Sources, r.Distance)
		if err != nil {
			return nil, fmt.Errorf("postgres: search hybrid: %w", err)
		}
		lexMatches = append(lexMatches, m)
	}

	return fuseHybrid(vecMatches, lexMatches,
		func(m domain.ClaimMatch) string { return m.ID },
		func(m domain.ClaimMatch) float32 { return m.Distance },
		topK, rrfK), nil
}

// SearchEvidenceHybrid is SearchHybrid over the evidence corpus: it fuses the
// vector and French lexical branches by Reciprocal Rank Fusion and returns the
// topK evidence hits nearest-first by cosine distance, preserving the wire shape
// SearchEvidence returns. sources scopes both branches to a set of sources
// exactly as SearchEvidence does (nil or empty is the global search). The vector
// branch is always the single-stage halfvec search here; binary quantization
// (the opt-in extreme-scale path) is orthogonal to fusion and not combined with
// it. An empty query text falls back to the pure vector SearchEvidence.
func (s *Store) SearchEvidenceHybrid(ctx context.Context, text string, query []float32, topK, lexicalK, rrfK, efSearch int, sources []string) ([]domain.EvidenceHit, error) {
	if topK <= 0 || topK > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: search evidence hybrid: topK %d out of range", topK)
	}
	if len(query) != domain.EmbeddingDim {
		return nil, fmt.Errorf("postgres: search evidence hybrid: query has %d dims, want %d", len(query), domain.EmbeddingDim)
	}
	if strings.TrimSpace(text) == "" {
		return s.SearchEvidence(ctx, query, topK, efSearch, sources)
	}
	lexicalK, rrfK, poolK, err := hybridBounds(topK, lexicalK, rrfK)
	if err != nil {
		return nil, fmt.Errorf("postgres: search evidence hybrid: %w", err)
	}
	if len(sources) == 0 {
		sources = nil
	}
	scoped := sources != nil

	vec := pgvector.NewHalfVector(query)
	var vecRows []db.SearchEvidenceChunksRow
	var lexRows []db.LexicalSearchEvidenceChunksRow
	err = s.searchTuned(ctx, efSearch, scoped, func(q *db.Queries) error {
		var e error
		if vecRows, e = q.SearchEvidenceChunks(ctx, db.SearchEvidenceChunksParams{QueryEmbedding: &vec, Sources: sources, ResultLimit: int32(poolK)}); e != nil {
			return e
		}
		lexRows, e = q.LexicalSearchEvidenceChunks(ctx, db.LexicalSearchEvidenceChunksParams{QueryEmbedding: &vec, QueryText: text, Sources: sources, ResultLimit: int32(lexicalK)})
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: search evidence hybrid: %w", err)
	}

	vecHits := make([]domain.EvidenceHit, 0, len(vecRows))
	for _, r := range vecRows {
		h, err := toEvidenceHit(r.Source, r.ExternalID, r.ChunkIndex, r.Title, r.Url, r.Content, r.Kind, r.Metadata, r.PublishedAt, r.Distance)
		if err != nil {
			return nil, fmt.Errorf("postgres: search evidence hybrid: %w", err)
		}
		vecHits = append(vecHits, h)
	}
	lexHits := make([]domain.EvidenceHit, 0, len(lexRows))
	for _, r := range lexRows {
		h, err := toEvidenceHit(r.Source, r.ExternalID, r.ChunkIndex, r.Title, r.Url, r.Content, r.Kind, r.Metadata, r.PublishedAt, r.Distance)
		if err != nil {
			return nil, fmt.Errorf("postgres: search evidence hybrid: %w", err)
		}
		lexHits = append(lexHits, h)
	}

	return fuseHybrid(vecHits, lexHits,
		func(h domain.EvidenceHit) evidenceKey {
			return evidenceKey{source: h.Source, externalID: h.ExternalID, chunkIndex: h.ChunkIndex}
		},
		func(h domain.EvidenceHit) float32 { return h.Distance },
		topK, rrfK), nil
}

// hybridBounds validates and defaults the hybrid knobs shared by both corpora.
// It floors an unset rrfK at DefaultRRFConstant, requires a positive in-range
// lexicalK, and returns the vector-branch pool size (the larger of topK and
// lexicalK) so the vector branch over-fetches enough that a lexically rescued
// hit also earns a vector rank for the fusion.
func hybridBounds(topK, lexicalK, rrfK int) (lexK, rrf, poolK int, err error) {
	if lexicalK <= 0 || lexicalK > math.MaxInt32 {
		return 0, 0, 0, fmt.Errorf("lexicalK %d out of range", lexicalK)
	}
	if rrfK <= 0 {
		rrfK = DefaultRRFConstant
	}
	poolK = topK
	if lexicalK > poolK {
		poolK = lexicalK
	}
	return lexicalK, rrfK, poolK, nil
}
