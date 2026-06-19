package postgres

import (
	"context"
	"fmt"
	"math"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// EmbeddedChunks returns up to limit live chunks that carry an embedding,
// ordered after cur in keyset order, with their vectors decoded to []float32.
// The clustering job pages the whole embedded corpus through it, so it reads
// only the identity and vector each chunk needs to be clustered.
func (s *Store) EmbeddedChunks(ctx context.Context, cur domain.WikiCursor, limit int) ([]domain.WikiChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: embedded chunks: limit %d out of range", limit)
	}
	rows, err := s.queries.EmbeddedWikiChunks(ctx, db.EmbeddedWikiChunksParams{
		ExcludeCorpus:   domain.StatCorpus,
		AfterPageID:     cur.PageID,
		AfterChunkIndex: cur.ChunkIndex,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: embedded chunks: %w", err)
	}
	out := make([]domain.WikiChunk, len(rows))
	for i, r := range rows {
		if r.Embedding == nil {
			// The query filters embedding IS NOT NULL, so this is a contract
			// violation, not a normal state; surface it rather than cluster a nil.
			return nil, fmt.Errorf("postgres: embedded chunk page %d chunk %d has a null embedding", r.PageID, r.ChunkIndex)
		}
		out[i] = domain.WikiChunk{
			PageID:     r.PageID,
			ChunkIndex: int(r.ChunkIndex),
			Embedding:  r.Embedding.Slice(),
		}
	}
	return out, nil
}

// SetChunkClustering writes each chunk's cluster id and importance into the live
// table in one batch. Every chunk must carry both, since the clustering job
// assigns them together; the write is idempotent, so re-running the job over an
// unchanged corpus rewrites the same values.
func (s *Store) SetChunkClustering(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	params := make([]db.SetWikiChunkClusteringParams, len(chunks))
	for i, c := range chunks {
		if c.ClusterID == nil || c.Importance == nil {
			return fmt.Errorf("postgres: set clustering page %d chunk %d: cluster id and importance are required", c.PageID, c.ChunkIndex)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: set clustering page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		params[i] = db.SetWikiChunkClusteringParams{
			ClusterID:  *c.ClusterID,
			Importance: *c.Importance,
			PageID:     c.PageID,
			ChunkIndex: int32(c.ChunkIndex),
		}
	}
	if err := firstBatchError(s.queries.SetWikiChunkClustering(ctx, params)); err != nil {
		return fmt.Errorf("postgres: set wiki chunk clustering: %w", err)
	}
	return nil
}
