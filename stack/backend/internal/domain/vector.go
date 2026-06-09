// Package domain holds core models and repository interfaces (ports).
// It imports nothing internal; all layers depend inward on this package.
package domain

import "context"

// Document is a content chunk and its embedding, as stored in the vector store.
type Document struct {
	ID        string
	Content   string
	Metadata  map[string]any
	Embedding []float32 // voyage-4, 1024 dims
}

// Match is a retrieval hit. Distance is cosine distance in [0, 2]; lower is
// more similar.
type Match struct {
	ID       string
	Content  string
	Metadata map[string]any
	Distance float32
}

// VectorStore is the port for embedding storage and approximate
// nearest-neighbor retrieval. The concrete implementation (store/postgres) is
// the only place that knows about the database, so the engine can be swapped
// behind this interface without changing service or handler code.
type VectorStore interface {
	// Ping verifies the store is reachable.
	Ping(ctx context.Context) error
	// Upsert inserts or replaces documents by ID.
	Upsert(ctx context.Context, docs []Document) error
	// Search returns the topK documents closest to query by cosine distance,
	// nearest first.
	Search(ctx context.Context, query []float32, topK int) ([]Match, error)
}
