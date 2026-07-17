package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ContentAlreadyEmbedded reports whether the corpus already holds the chunk's
// exact content at its natural key with an embedding, so a single-write ingest
// worker can skip re-embedding an unchanged re-crawl (VER-203, measure 1). It
// compares the incoming content's sha256 against the database-maintained
// content_hash column, so an identical re-ingest is an index probe on (source,
// content_hash) rather than a re-embed of text the store already carries. The
// sha256 of a text's UTF-8 bytes in Go matches the generated column's
// immutable_sha256 (sha256 over convert_to(content, 'UTF8')), so the
// fingerprints line up for every content, backslashes included.
//
// A missing row, changed content, or a row that exists but is not yet embedded
// all report false, so the worker embeds exactly when there is fresh work: a new
// chunk, an edited chunk, or one still awaiting its first vector.
func (s *Store) ContentAlreadyEmbedded(ctx context.Context, c domain.EvidenceChunk) (bool, error) {
	if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
		return false, fmt.Errorf("postgres: content already embedded %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
	}
	sum := sha256.Sum256([]byte(c.Content))
	var embedded bool
	err := s.pool.QueryRow(
		ctx, `
		SELECT embedding IS NOT NULL
		FROM evidence_chunks
		WHERE source = $1 AND external_id = $2 AND chunk_index = $3 AND content_hash = $4`,
		c.Source, c.ExternalID, int32(c.ChunkIndex), sum[:],
	).Scan(&embedded)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("postgres: content already embedded %s/%s#%d: %w", c.Source, c.ExternalID, c.ChunkIndex, err)
	}
	return embedded, nil
}

// UpsertEmbeddedChunkDeduped upserts a freshly embedded chunk, applying the
// near-duplicate gate (VER-203, measure 2) when nearDupSimilarity is positive.
//
// With the gate off (nearDupSimilarity <= 0, the default) it is exactly
// UpsertEmbeddedChunk. With it on, it first measures the chunk's vector against
// the closest OTHER embedded chunk in the same source: when that cosine
// similarity meets the bar the chunk is a redundant re-rendering (boilerplate, a
// re-crawl with a trivial diff, the same statistic restated), so it is stored
// WITHOUT its vector and flagged duplicate in metadata - kept for provenance but
// never served in search (every search filters embedding IS NOT NULL) and never
// carried in the HNSW index. flagged reports whether the chunk was gated.
//
// The intended bar sits well above the evidence borrow threshold so only true
// near-identities are withheld, and the gate is off by default until the golden
// eval proves no recall loss.
func (s *Store) UpsertEmbeddedChunkDeduped(ctx context.Context, c domain.EvidenceChunk, nearDupSimilarity float64) (bool, error) {
	if nearDupSimilarity > 0 && len(c.Embedding) == domain.EmbeddingDim {
		sim, found, err := s.nearestSourceSimilarity(ctx, c.Source, c.ExternalID, c.ChunkIndex, c.Embedding)
		if err != nil {
			return false, err
		}
		if found && sim >= nearDupSimilarity {
			if err := s.upsertDuplicateChunk(ctx, c, sim); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	if err := s.UpsertEmbeddedChunk(ctx, c); err != nil {
		return false, err
	}
	return false, nil
}

// nearestSourceSimilarity returns the cosine similarity of the closest OTHER
// embedded chunk in the same source to vec, and whether any such neighbor
// exists. It powers the near-duplicate gate. The scan excludes the chunk's own
// natural key so a re-ingest never measures a chunk against its previous self,
// runs on the HNSW index (embedding IS NOT NULL), and reads a single row. The
// vector travels in pgvector's text form and is cast server-side, the same
// halfvec-safe path the bulk writers use.
func (s *Store) nearestSourceSimilarity(ctx context.Context, source, externalID string, chunkIndex int, vec []float32) (float64, bool, error) {
	if chunkIndex < 0 || chunkIndex > math.MaxInt32 {
		return 0, false, fmt.Errorf("postgres: nearest in source %s: chunk index %d out of range", source, chunkIndex)
	}
	var distance float64
	err := s.pool.QueryRow(
		ctx, `
		SELECT (embedding <=> $1::halfvec)::float8
		FROM evidence_chunks
		WHERE source = $2 AND embedding IS NOT NULL
		  AND NOT (external_id = $3 AND chunk_index = $4)
		ORDER BY embedding <=> $1::halfvec
		LIMIT 1`,
		formatHalfVec(vec), source, externalID, int32(chunkIndex),
	).Scan(&distance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("postgres: nearest in source %s for %s#%d: %w", source, externalID, chunkIndex, err)
	}
	return 1 - distance, true, nil
}

// upsertDuplicateChunk stores a near-duplicate chunk for provenance with no
// embedding and the duplicate metadata flag. It forces embedding to NULL on both
// insert and conflict - unlike UpsertEvidenceChunk, which keeps an existing
// vector when the content is unchanged - so a chunk that was previously served
// and is now judged a duplicate is withheld from search on the spot. The
// metadata merges (existing || duplicate flag) so provenance keys survive, and
// content_hash regenerates from the unchanged content.
func (s *Store) upsertDuplicateChunk(ctx context.Context, c domain.EvidenceChunk, similarity float64) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("postgres: upsert duplicate chunk %s/%s: invalid kind %q", c.Source, c.ExternalID, c.Kind)
	}
	if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
		return fmt.Errorf("postgres: upsert duplicate chunk %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
	}
	meta, err := marshalMetadata(domain.WithDuplicateFlag(c.Metadata, similarity))
	if err != nil {
		return fmt.Errorf("postgres: upsert duplicate chunk %s/%s: %w", c.Source, c.ExternalID, err)
	}
	_, err = s.pool.Exec(
		ctx, `
		INSERT INTO evidence_chunks (source, external_id, chunk_index, title, url, content, kind, metadata, embedding, synced_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL, now())
		ON CONFLICT (source, external_id, chunk_index) DO UPDATE
		    SET title = EXCLUDED.title,
		        url = EXCLUDED.url,
		        content = EXCLUDED.content,
		        kind = EXCLUDED.kind,
		        metadata = evidence_chunks.metadata || EXCLUDED.metadata,
		        embedding = NULL,
		        synced_at = now()`,
		c.Source, c.ExternalID, int32(c.ChunkIndex), c.Title, c.URL, c.Content, string(c.Kind), meta,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert duplicate chunk %s/%s#%d: %w", c.Source, c.ExternalID, c.ChunkIndex, err)
	}
	return nil
}
