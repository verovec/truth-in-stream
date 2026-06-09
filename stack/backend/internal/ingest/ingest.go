// Package ingest loads curated claims, embeds them, and upserts them into the
// claim store. It is the read -> embed -> store path used by cmd/ingest.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// defaultBatchSize bounds how many claims are embedded and upserted per round.
// Voyage allows up to 1000 inputs per request; 128 is a conservative default
// that keeps request and batch sizes modest.
const defaultBatchSize = 128

// Store is the slice of the claim store ingestion needs.
type Store interface {
	Upsert(ctx context.Context, claims []domain.Claim) error
}

// Embedder embeds texts for storage (input_type=document).
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// SeedClaim is the on-disk shape of a claim before embedding. Sources reuse
// the domain type directly: the seed JSON and the stored jsonb share the same
// {title, url} shape.
type SeedClaim struct {
	ID      string          `json:"id"`
	Text    string          `json:"text"`
	Verdict domain.Verdict  `json:"verdict"`
	Sources []domain.Source `json:"sources"`
}

// LoadSeed decodes and validates a JSON array of seed claims. Validation
// mirrors the store's invariants (known verdict, present sources) so bad data
// is rejected before any embedding spend.
func LoadSeed(r io.Reader) ([]SeedClaim, error) {
	var seeds []SeedClaim
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&seeds); err != nil {
		return nil, fmt.Errorf("ingest: decode seed: %w", err)
	}

	ids := make(map[string]struct{}, len(seeds))
	for i, s := range seeds {
		switch {
		case s.ID == "":
			return nil, fmt.Errorf("ingest: seed %d: empty id", i)
		case s.Text == "":
			return nil, fmt.Errorf("ingest: seed %q: empty text", s.ID)
		case !s.Verdict.Valid():
			return nil, fmt.Errorf("ingest: seed %q: invalid verdict %q", s.ID, s.Verdict)
		case len(s.Sources) == 0:
			return nil, fmt.Errorf("ingest: seed %q: at least one source required", s.ID)
		}
		for _, src := range s.Sources {
			if src.Title == "" || src.URL == "" {
				return nil, fmt.Errorf("ingest: seed %q: source needs title and url", s.ID)
			}
		}
		if _, dup := ids[s.ID]; dup {
			return nil, fmt.Errorf("ingest: duplicate seed id %q", s.ID)
		}
		ids[s.ID] = struct{}{}
	}
	return seeds, nil
}

// Run embeds the seed claims and upserts them in batches, returning the number
// of claims written. Stable IDs plus upsert make re-runs idempotent.
func Run(ctx context.Context, store Store, embedder Embedder, seeds []SeedClaim, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	total := 0
	for start := 0; start < len(seeds); start += batchSize {
		end := min(start+batchSize, len(seeds))
		batch := seeds[start:end]

		texts := make([]string, len(batch))
		for i, s := range batch {
			texts[i] = s.Text
		}

		embeddings, err := embedder.EmbedDocuments(ctx, texts)
		if err != nil {
			return total, fmt.Errorf("ingest: embed batch [%d:%d]: %w", start, end, err)
		}
		if len(embeddings) != len(batch) {
			return total, fmt.Errorf("ingest: embed batch [%d:%d]: got %d embeddings, want %d", start, end, len(embeddings), len(batch))
		}

		claims := make([]domain.Claim, len(batch))
		for i, s := range batch {
			claims[i] = domain.Claim{
				ID:        s.ID,
				Text:      s.Text,
				Verdict:   s.Verdict,
				Sources:   s.Sources,
				Embedding: embeddings[i],
			}
		}
		if err := store.Upsert(ctx, claims); err != nil {
			return total, fmt.Errorf("ingest: upsert batch [%d:%d]: %w", start, end, err)
		}
		total += len(batch)
	}
	return total, nil
}
