package embed

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"math/rand/v2"
)

// Deterministic produces stable pseudo-random unit vectors from text alone. It
// exists to generate the committed embedding cache offline, without an embedding
// API key, so local seeding and CI never need one.
//
// The vectors are NOT semantically meaningful: identical text always yields the
// identical vector - so a query for a stored document's exact text lands on the
// same vector and similarity search returns it - while unrelated texts are
// near-orthogonal. Run refresh-embeddings with a real EMBEDDING_API_KEY to
// replace the committed cache with genuine voyage-4 vectors before relying on
// real semantic similarity.
type Deterministic struct {
	dim int
}

// NewDeterministic returns a Deterministic embedder producing dim-length
// vectors.
func NewDeterministic(dim int) *Deterministic {
	return &Deterministic{dim: dim}
}

// EmbedDocuments embeds texts; the input type is irrelevant to a Deterministic
// embedder, so documents and queries of the same text match.
func (d *Deterministic) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	return d.embed(texts), nil
}

// EmbedQueries embeds texts identically to EmbedDocuments so a query for a
// stored document's text matches it.
func (d *Deterministic) EmbedQueries(_ context.Context, texts []string) ([][]float32, error) {
	return d.embed(texts), nil
}

func (d *Deterministic) embed(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		out[i] = d.vector(txt)
	}
	return out
}

// vector derives a unit-length pseudo-random vector seeded by the normalized
// text, so the same text always produces the same direction.
func (d *Deterministic) vector(text string) []float32 {
	sum := sha256.Sum256([]byte(NormalizeText(text)))
	seed1 := binary.LittleEndian.Uint64(sum[0:8])
	seed2 := binary.LittleEndian.Uint64(sum[8:16])
	r := rand.New(rand.NewPCG(seed1, seed2))

	vec := make([]float32, d.dim)
	var norm float64
	for i := range vec {
		// NormFloat64 gives a spherically symmetric direction once normalized.
		f := r.NormFloat64()
		vec[i] = float32(f)
		norm += f * f
	}
	if norm == 0 {
		// Degenerate only if every draw was exactly zero; fall back to a unit
		// basis vector so the result is still usable.
		vec[0] = 1
		return vec
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}
