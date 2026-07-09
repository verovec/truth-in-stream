// Package domain holds core models and repository interfaces (ports).
// It imports nothing internal; all layers depend inward on this package.
package domain

import (
	"context"
	"fmt"
)

// EmbeddingDim is the fixed embedding dimension for every vector store. It is
// pinned to the voyage-4-large output dimension and the halfvec(EmbeddingDim)
// columns; ingest and query time must use the same value or similarity scores
// are meaningless. It is the Go half of the one-place dimension contract - the
// SQL half is the halfvec(1024) column type in the migrations - so changing the
// dimension is the model-migration runbook (docs/embedding-model-migration.md),
// not an ad-hoc edit.
const EmbeddingDim = 1024

// HalfvecColumnType is the Postgres type an embedding column declares, derived
// from EmbeddingDim so a dimension change has a single Go source of truth. It is
// what pg_attribute's format_type reports for a halfvec(EmbeddingDim) column, so
// the corpus verifier compares the live column against it rather than a
// hard-coded literal.
func HalfvecColumnType() string {
	return fmt.Sprintf("halfvec(%d)", EmbeddingDim)
}

// ValidCosineThreshold reports whether t is a usable cosine-similarity
// threshold: a real number in [-1, 1]. The inverted comparison also rejects
// NaN, which would otherwise disable whatever threshold filter gates on it. It
// is the single definition every matcher, gate, and config loader checks
// against so they cannot drift on what a valid threshold is.
func ValidCosineThreshold(t float64) bool {
	return t >= -1 && t <= 1
}

// Verdict is the fact-check stance of a claim against what was spoken.
type Verdict string

const (
	// VerdictCorroborates means the claim supports the spoken statement.
	VerdictCorroborates Verdict = "corroborates"
	// VerdictContradicts means the claim refutes the spoken statement.
	VerdictContradicts Verdict = "contradicts"
	// VerdictUnclear means the evidence is mixed or insufficient.
	VerdictUnclear Verdict = "unclear"
)

// Valid reports whether v is one of the known verdicts. It mirrors the CHECK
// constraint on claims.verdict so bad data is rejected before it reaches the
// store.
func (v Verdict) Valid() bool {
	switch v {
	case VerdictCorroborates, VerdictContradicts, VerdictUnclear:
		return true
	default:
		return false
	}
}

// Source is a citation backing a claim. The json tags are the on-disk seed
// format and the claims.sources jsonb wire format, which are identical; the tags
// add no internal imports, keeping domain a leaf package.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Claim is a curated, verified statement and its embedding, as stored in the
// vector store. Embedding is voyage-4-large, EmbeddingDim dimensions.
type Claim struct {
	ID        string
	Text      string
	Verdict   Verdict
	Sources   []Source
	Embedding []float32
}

// ClaimMatch is a retrieval hit. Distance is cosine distance in [0, 2]; lower
// is more similar. The embedding itself is not returned.
type ClaimMatch struct {
	ID       string
	Text     string
	Verdict  Verdict
	Sources  []Source
	Distance float32
}

// ClaimStore is the port for claim storage and approximate nearest-neighbor
// retrieval. The concrete implementation (store/postgres) is the only place
// that knows about the database, so the engine can be swapped behind this
// interface without changing service, ingestion, or handler code.
type ClaimStore interface {
	// Ping verifies the store is reachable.
	Ping(ctx context.Context) error
	// Upsert inserts or replaces claims by ID.
	Upsert(ctx context.Context, claims []Claim) error
	// Search returns the topK claims closest to query by cosine distance,
	// nearest first.
	Search(ctx context.Context, query []float32, topK int) ([]ClaimMatch, error)
}
