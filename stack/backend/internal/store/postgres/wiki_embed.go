package postgres

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// The bulk-embedding pipeline builds the embedded corpus in a staging table and
// swaps it into wiki_chunks atomically. The staging table, its index, and the
// rename swap are runtime DDL that sqlc cannot model (sqlc compiles DML against
// the migration schema, which does not include this transient table), so they
// run as the raw statements below; all DML still goes through generated
// queries. The names are constants, never interpolated user input.
const (
	wikiStagingTable           = "wiki_chunks_staging"
	wikiStagingPK              = "wiki_chunks_staging_pkey"
	wikiStagingHNSWIndex       = "wiki_chunks_staging_embedding_hnsw"
	wikiChunksHNSWIndex        = "wiki_chunks_embedding_hnsw"
	wikiChunksPK               = "wiki_chunks_pkey"
	wikiChunksOldTable         = "wiki_chunks_old"
	wikiChunksOldPK            = "wiki_chunks_old_pkey"
	wikiChunksOldHNSWIndex     = "wiki_chunks_old_embedding_hnsw"
	hnswM                  int = 16
	hnswEfConstruction     int = 200
)

// EmbedWatermark returns the greatest (page_id, chunk_index) already loaded into
// the staging table - the resume point for the bulk-embedding run - or the zero
// cursor when staging is absent or empty.
func (s *Store) EmbedWatermark(ctx context.Context) (domain.WikiCursor, error) {
	exists, err := s.stagingExists(ctx)
	if err != nil {
		return domain.WikiCursor{}, err
	}
	if !exists {
		return domain.WikiCursor{}, nil
	}

	var cur domain.WikiCursor
	err = s.pool.QueryRow(ctx,
		"SELECT page_id, chunk_index FROM "+wikiStagingTable+" ORDER BY page_id DESC, chunk_index DESC LIMIT 1",
	).Scan(&cur.PageID, &cur.ChunkIndex)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WikiCursor{}, nil
	}
	if err != nil {
		return domain.WikiCursor{}, fmt.Errorf("postgres: read staging watermark: %w", err)
	}
	return cur, nil
}

// UnembeddedChunks returns up to limit live chunks ordered after cur, the next
// slice of work for the bulk-embedding run.
func (s *Store) UnembeddedChunks(ctx context.Context, cur domain.WikiCursor, limit int) ([]domain.WikiChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded chunks: limit %d out of range", limit)
	}
	rows, err := s.queries.UnembeddedWikiChunks(ctx, db.UnembeddedWikiChunksParams{
		AfterPageID:     cur.PageID,
		AfterChunkIndex: cur.ChunkIndex,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded chunks: %w", err)
	}
	out := make([]domain.WikiChunk, len(rows))
	for i, r := range rows {
		out[i] = domain.WikiChunk{
			PageID:     r.PageID,
			ChunkIndex: int(r.ChunkIndex),
			Title:      r.Title,
			URL:        r.Url,
			RevisionID: r.RevisionID,
			Corpus:     r.Corpus,
			Content:    r.Content,
		}
	}
	return out, nil
}

// EstimateRemaining counts the live pages, chunks, and content characters still
// to embed beyond cur, feeding the dry-run cost estimate.
func (s *Store) EstimateRemaining(ctx context.Context, cur domain.WikiCursor) (domain.WikiRemaining, error) {
	row, err := s.queries.EstimateRemainingWikiChunks(ctx, db.EstimateRemainingWikiChunksParams{
		AfterPageID:     cur.PageID,
		AfterChunkIndex: cur.ChunkIndex,
	})
	if err != nil {
		return domain.WikiRemaining{}, fmt.Errorf("postgres: estimate remaining: %w", err)
	}
	return domain.WikiRemaining{Pages: row.Pages, Chunks: row.Chunks, Chars: row.Chars}, nil
}

// CreateStaging creates the unindexed staging table if it is absent. LIKE
// copies the columns, NOT NULLs, and defaults but no primary key or HNSW index,
// keeping the COPY load fast; the index is built once, after the load. IF NOT
// EXISTS preserves a partial staging table so an interrupted run resumes into
// it rather than re-embedding from scratch.
func (s *Store) CreateStaging(ctx context.Context) error {
	stmt := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (LIKE wiki_chunks INCLUDING DEFAULTS INCLUDING CONSTRAINTS)",
		wikiStagingTable,
	)
	if _, err := s.pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("postgres: create staging table: %w", err)
	}
	return nil
}

// CopyStagingChunks bulk-loads embedded chunks into the staging table via a
// text-format COPY. The COPY runs in CSV text format, not pgx's binary
// CopyFrom: pgvector-go's HalfVector has no binary wire encoder, so a binary
// COPY corrupts the stream, whereas the server parses the half-vector's text
// form natively. Every chunk must carry a full-dimension embedding; synced_at
// defaults.
func (s *Store) CopyStagingChunks(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	for _, c := range chunks {
		if len(c.Embedding) != domain.EmbeddingDim {
			return fmt.Errorf("postgres: copy staging chunk page %d: embedding has %d dims, want %d", c.PageID, len(c.Embedding), domain.EmbeddingDim)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: copy staging chunk page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		record := []string{
			strconv.FormatInt(c.PageID, 10),
			strconv.Itoa(c.ChunkIndex),
			c.Title,
			c.URL,
			strconv.FormatInt(c.RevisionID, 10),
			c.Corpus,
			c.Content,
			formatHalfVec(c.Embedding),
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("postgres: encode staging row for page %d: %w", c.PageID, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("postgres: encode staging rows: %w", err)
	}

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire connection for staging copy: %w", err)
	}
	defer conn.Release()

	copySQL := fmt.Sprintf(
		"COPY %s (page_id, chunk_index, title, url, revision_id, corpus, content, embedding) FROM STDIN WITH (FORMAT csv)",
		wikiStagingTable,
	)
	tag, err := conn.Conn().PgConn().CopyFrom(ctx, &buf, copySQL)
	if err != nil {
		return fmt.Errorf("postgres: copy staging chunks: %w", err)
	}
	if tag.RowsAffected() != int64(len(chunks)) {
		return fmt.Errorf("postgres: copy staging chunks: loaded %d of %d", tag.RowsAffected(), len(chunks))
	}
	return nil
}

// formatHalfVec renders an embedding as pgvector's text form, "[1,2,3]", for a
// text-format COPY.
func formatHalfVec(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// FinalizeStaging builds the staging index, then swaps it into the live table
// and checkpoints the corpus. The swap revalidates that staging mirrors the live
// corpus inside its own transaction, so it never swaps a partial or unembedded
// corpus and a concurrent ingest cannot slip rows into the table being dropped.
func (s *Store) FinalizeStaging(ctx context.Context, corpus, maintenanceWorkMem string, maxParallelWorkers int) error {
	if err := s.buildStagingIndex(ctx, maintenanceWorkMem, maxParallelWorkers); err != nil {
		return err
	}
	if err := s.swapStaging(ctx, corpus); err != nil {
		return err
	}
	return nil
}

// validateStagingTx refuses the swap unless staging mirrors the live corpus
// exactly and every staged chunk is embedded. Running inside the swap
// transaction makes the check atomic with the rename: a concurrent ingest is
// either counted here or blocked by the swap's lock, never lost.
func (s *Store) validateStagingTx(ctx context.Context, tx pgx.Tx) error {
	live, err := s.queries.WithTx(tx).CountWikiChunks(ctx)
	if err != nil {
		return fmt.Errorf("count live chunks: %w", err)
	}
	if live == 0 {
		return errors.New("live corpus is empty, nothing to embed")
	}

	var staging, nullEmbeddings int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+wikiStagingTable).Scan(&staging); err != nil {
		return fmt.Errorf("count staging chunks: %w", err)
	}
	if staging != live {
		return fmt.Errorf("staging has %d chunks, live has %d; refusing to swap a partial corpus", staging, live)
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+wikiStagingTable+" WHERE embedding IS NULL").Scan(&nullEmbeddings); err != nil {
		return fmt.Errorf("count staging null embeddings: %w", err)
	}
	if nullEmbeddings != 0 {
		return fmt.Errorf("%d staged chunks are unembedded", nullEmbeddings)
	}
	return nil
}

// buildStagingIndex adds the primary key and builds the HNSW index on the loaded
// staging table, with the index-build memory and parallelism raised for the
// transaction only. Both steps are idempotent so a resumed finalize is safe.
func (s *Store) buildStagingIndex(ctx context.Context, maintenanceWorkMem string, maxParallelWorkers int) error {
	addPK := fmt.Sprintf(`DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '%s') THEN
        ALTER TABLE %s ADD CONSTRAINT %s PRIMARY KEY (page_id, chunk_index);
    END IF;
END $$;`, wikiStagingPK, wikiStagingTable, wikiStagingPK)

	createIndex := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding halfvec_cosine_ops) WITH (m = %d, ef_construction = %d)",
		wikiStagingHNSWIndex, wikiStagingTable, hnswM, hnswEfConstruction,
	)

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// set_config(..., true) is transaction-local, so the raised limits reset
		// when this transaction ends and never leak onto the pooled connection.
		if _, err := tx.Exec(ctx, "SELECT set_config('maintenance_work_mem', $1, true)", maintenanceWorkMem); err != nil {
			return fmt.Errorf("set maintenance_work_mem: %w", err)
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('max_parallel_maintenance_workers', $1, true)", strconv.Itoa(maxParallelWorkers)); err != nil {
			return fmt.Errorf("set max_parallel_maintenance_workers: %w", err)
		}
		if _, err := tx.Exec(ctx, addPK); err != nil {
			return fmt.Errorf("add staging primary key: %w", err)
		}
		if _, err := tx.Exec(ctx, createIndex); err != nil {
			return fmt.Errorf("build staging hnsw index: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: build staging index: %w", err)
	}
	return nil
}

// swapStaging replaces the live wiki_chunks with the freshly indexed staging
// table in one transaction: readers see the old corpus until commit and the new
// one after, never a partial one. The old table's index and constraint are
// renamed aside first so the staging objects can take the canonical names that
// the migration schema and later ingests expect. The corpus checkpoint advances
// in the same transaction.
func (s *Store) swapStaging(ctx context.Context, corpus string) error {
	stmts := []string{
		"DROP TABLE IF EXISTS " + wikiChunksOldTable,
		// IF EXISTS so a corpus whose HNSW index was dropped (e.g. for an ops
		// rebuild) still swaps; the staging index takes the canonical name next.
		fmt.Sprintf("ALTER INDEX IF EXISTS %s RENAME TO %s", wikiChunksHNSWIndex, wikiChunksOldHNSWIndex),
		fmt.Sprintf("ALTER TABLE wiki_chunks RENAME CONSTRAINT %s TO %s", wikiChunksPK, wikiChunksOldPK),
		"ALTER TABLE wiki_chunks RENAME TO " + wikiChunksOldTable,
		fmt.Sprintf("ALTER INDEX %s RENAME TO %s", wikiStagingHNSWIndex, wikiChunksHNSWIndex),
		fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s", wikiStagingTable, wikiStagingPK, wikiChunksPK),
		fmt.Sprintf("ALTER TABLE %s RENAME TO wiki_chunks", wikiStagingTable),
		"DROP TABLE " + wikiChunksOldTable,
	}

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.validateStagingTx(ctx, tx); err != nil {
			return err
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("swap step %q: %w", stmt, err)
			}
		}
		if err := s.queries.WithTx(tx).MarkWikiCorpusEmbedded(ctx, corpus); err != nil {
			return fmt.Errorf("checkpoint corpus: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: swap staging into place: %w", err)
	}
	return nil
}

// EmbedInProgress reports whether a bulk embed is mid-flight. The staging table
// exists only between a started and a completed bulk embed (the swap drops it),
// so its presence means the live corpus is not yet fully embedded - the delta
// sync refuses to run while one exists.
func (s *Store) EmbedInProgress(ctx context.Context) (bool, error) {
	return s.stagingExists(ctx)
}

// stagingExists reports whether the staging table is present.
func (s *Store) stagingExists(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", wikiStagingTable).Scan(&exists); err != nil {
		return false, fmt.Errorf("postgres: check staging table: %w", err)
	}
	return exists, nil
}
