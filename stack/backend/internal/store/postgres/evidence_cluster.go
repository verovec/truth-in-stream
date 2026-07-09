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
// The clustering job pages the whole embedded encyclopedic corpus through it, so
// it reads only the identity and vector each chunk needs to be clustered; the
// statistical sources are excluded (they are not clustered).
func (s *Store) EmbeddedChunks(ctx context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: embedded chunks: limit %d out of range", limit)
	}
	rows, err := s.queries.EmbeddedEvidenceChunks(ctx, db.EmbeddedEvidenceChunksParams{
		ExcludeSources:  domain.StatCorpora(),
		AfterSource:     cur.Source,
		AfterExternalID: cur.ExternalID,
		AfterChunkIndex: cur.ChunkIndex,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: embedded chunks: %w", err)
	}
	out := make([]domain.EvidenceChunk, len(rows))
	for i, r := range rows {
		if r.Embedding == nil {
			// The query filters embedding IS NOT NULL, so this is a contract
			// violation, not a normal state; surface it rather than cluster a nil.
			return nil, fmt.Errorf("postgres: embedded chunk %s/%s#%d has a null embedding", r.Source, r.ExternalID, r.ChunkIndex)
		}
		out[i] = domain.EvidenceChunk{
			Source:     r.Source,
			ExternalID: r.ExternalID,
			ChunkIndex: int(r.ChunkIndex),
			Embedding:  r.Embedding.Slice(),
		}
	}
	return out, nil
}

// SetChunkClustering merges each chunk's cluster id and importance into the live
// metadata jsonb in one batch, carried on the chunk's Metadata (WikiMetadata's
// cluster_id and importance keys). Every chunk must carry both, since the
// clustering job assigns them together; the write is idempotent, so re-running
// the job over an unchanged corpus rewrites the same values.
func (s *Store) SetChunkClustering(ctx context.Context, chunks []domain.EvidenceChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	params := make([]db.SetEvidenceChunkClusteringParams, len(chunks))
	for i, c := range chunks {
		wm, err := domain.ParseWikiMetadata(c.Metadata)
		if err != nil {
			return fmt.Errorf("postgres: set clustering %s/%s#%d: %w", c.Source, c.ExternalID, c.ChunkIndex, err)
		}
		if wm.ClusterID == nil || wm.Importance == nil {
			return fmt.Errorf("postgres: set clustering %s/%s#%d: cluster id and importance are required", c.Source, c.ExternalID, c.ChunkIndex)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: set clustering %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
		}
		params[i] = db.SetEvidenceChunkClusteringParams{
			ClusterID:  *wm.ClusterID,
			Importance: *wm.Importance,
			Source:     c.Source,
			ExternalID: c.ExternalID,
			ChunkIndex: int32(c.ChunkIndex),
		}
	}
	if err := firstBatchError(s.queries.SetEvidenceChunkClustering(ctx, params)); err != nil {
		return fmt.Errorf("postgres: set evidence chunk clustering: %w", err)
	}
	return nil
}
