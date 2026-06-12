package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// WikiCorpusHealth snapshots the live wiki corpus for the reingest verifier:
// the chunk count, the counts of rows that would make the corpus unservable (a
// missing or zero-vector embedding, an absent kind), the embedding column's
// declared type (which proves the dimension, since the column type enforces it
// on every row), and whether the HNSW index exists and is valid. A zero-vector
// is found via the inner product of the embedding with itself: for a halfvec the
// `<#>` operator is the negative inner product, which is zero only when the
// vector's norm is zero, so the corpus's voyage embeddings (never zero) never
// match unless a bug wrote an all-zero vector.
func (s *Store) WikiCorpusHealth(ctx context.Context) (domain.WikiCorpusHealth, error) {
	var h domain.WikiCorpusHealth
	if err := s.pool.QueryRow(
		ctx, `
		SELECT
			count(*)::bigint,
			count(*) FILTER (WHERE embedding IS NULL)::bigint,
			count(*) FILTER (WHERE embedding IS NOT NULL AND (embedding <#> embedding) = 0)::bigint,
			count(*) FILTER (WHERE kind NOT IN ('lead', 'body'))::bigint
		FROM wiki_chunks`,
	).Scan(&h.Chunks, &h.NullEmbeddings, &h.ZeroVectors, &h.MissingMetadata); err != nil {
		return domain.WikiCorpusHealth{}, fmt.Errorf("postgres: wiki corpus counts: %w", err)
	}

	if err := s.pool.QueryRow(
		ctx, `
		SELECT format_type(atttypid, atttypmod)
		FROM pg_attribute
		WHERE attrelid = 'wiki_chunks'::regclass AND attname = 'embedding' AND NOT attisdropped`,
	).Scan(&h.EmbeddingType); err != nil {
		return domain.WikiCorpusHealth{}, fmt.Errorf("postgres: wiki embedding column type: %w", err)
	}

	err := s.pool.QueryRow(ctx, `
		SELECT i.indisvalid
		FROM pg_class c
		JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.relname = $1`, wikiChunksHNSWIndex).Scan(&h.HNSWValid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		h.HNSWPresent = false
	case err != nil:
		return domain.WikiCorpusHealth{}, fmt.Errorf("postgres: wiki hnsw index state: %w", err)
	default:
		h.HNSWPresent = true
	}
	return h, nil
}

// ResetWikiCorpus clears the live corpus so the next bulk run rebuilds it from
// scratch under the current code. It truncates wiki_chunks, deletes the sync
// checkpoint (so StagingPlan no longer short-circuits as "already current" - the
// checkpoint keys on the dump version, not the code, so a code change that alters
// chunking or metadata is otherwise invisible to it), and drops any leftover
// staging or old table from an interrupted swap. It runs in one transaction, so
// a failure leaves the corpus untouched rather than half-reset.
func (s *Store) ResetWikiCorpus(ctx context.Context) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		stmts := []string{
			"DROP TABLE IF EXISTS " + wikiStagingTable,
			"DROP TABLE IF EXISTS " + wikiChunksOldTable,
			"TRUNCATE wiki_chunks",
			"DELETE FROM wiki_sync_state",
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: reset wiki corpus: %w", err)
	}
	return nil
}
