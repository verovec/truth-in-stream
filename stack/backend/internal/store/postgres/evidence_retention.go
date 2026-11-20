package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CountEvidenceOlderThan returns how many chunks of one source were last synced
// before cutoff - the dry-run half of the retention sweep (VER-203, measure 3).
// synced_at advances on every upsert of a chunk, so a superseded statistical
// period (never re-emitted by a later run) keeps its old timestamp and ages past
// the cutoff, while a still-current series is refreshed and stays. Scoping to one
// source keeps a policy for one corpus from ever touching another that shares the
// table.
func (s *Store) CountEvidenceOlderThan(ctx context.Context, source string, cutoff time.Time) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(
		ctx,
		"SELECT count(*)::bigint FROM evidence_chunks WHERE source = $1 AND synced_at < $2",
		source, cutoff,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count evidence older than %s for source %q: %w", cutoff.Format(time.RFC3339), source, err)
	}
	return n, nil
}

// SweepEvidenceOlderThan deletes the chunks of one source last synced before
// cutoff and returns how many rows it removed - the real-run half of the
// retention sweep. The whole sweep runs in one transaction, so a reader mid-sweep
// sees either the entire generation or none of it and an interrupted sweep rolls
// back cleanly - atomicity the DELETE alone already provides. The preceding
// UPDATE that NULLs the embeddings is not needed for that atomicity (the DELETE
// removes the row and its HNSW entry together); it is deliberate, explicit
// redundant I/O that spells out "the vectors go first" and keeps the accounting
// honest, and can be dropped if the extra pass ever matters at scale.
// Re-ingesting the source restores the rows cleanly, since the upsert recreates
// them from source. A caller wanting the count without deleting uses
// CountEvidenceOlderThan.
func (s *Store) SweepEvidenceOlderThan(ctx context.Context, source string, cutoff time.Time) (int64, error) {
	var deleted int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			"UPDATE evidence_chunks SET embedding = NULL WHERE source = $1 AND synced_at < $2 AND embedding IS NOT NULL",
			source, cutoff,
		); err != nil {
			return fmt.Errorf("clear embeddings: %w", err)
		}
		tag, err := tx.Exec(
			ctx,
			"DELETE FROM evidence_chunks WHERE source = $1 AND synced_at < $2",
			source, cutoff,
		)
		if err != nil {
			return fmt.Errorf("delete rows: %w", err)
		}
		deleted = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: sweep evidence older than %s for source %q: %w", cutoff.Format(time.RFC3339), source, err)
	}
	return deleted, nil
}
