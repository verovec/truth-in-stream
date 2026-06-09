// Package domain holds core models and repository interfaces (ports).
// It imports nothing internal; all layers depend inward on this package.
package domain

import "context"

// EmbeddingDim is the fixed embedding dimension for the claim store. It is
// pinned to the voyage-4 output dimension and the claims.embedding
// halfvec(1024) column; ingest and query time must use the same value or
// similarity scores are meaningless.
const EmbeddingDim = 1024

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
// vector store. Embedding is voyage-4, EmbeddingDim dimensions.
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
