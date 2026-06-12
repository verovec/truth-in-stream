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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// The bulk-embedding pipeline builds the next corpus in a staging table and
// swaps it into wiki_chunks atomically: ingest fills staging from the dump,
// unchanged embeddings carry forward from live, the remaining chunks embed in
// place, and a rename swaps staging over live. The staging table, its index,
// and the rename are runtime DDL that sqlc cannot model (it compiles DML
// against the migration schema, which omits this transient table), so they run
// as the raw statements below; the live-corpus DML still goes through generated
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

// Staging is stamped with its lifecycle phase and the dump version it is being
// built for, recorded as the table comment (see stampStaging). The phase lets a
// later run tell a fully-materialized staging it can resume embedding into
// (ready) from one interrupted mid-build (building), which it must rebuild.
const (
	stampBuilding = "building"
	stampReady    = "ready"
)

func stagingStamp(phase, version string) string { return phase + ":" + version }

// readStaging reports whether the staging table exists and its (phase, version)
// stamp. An absent or unstamped table reads as building with an empty version,
// which both classify as "rebuild" against any real dump. to_regclass yields
// NULL for an absent table, so obj_description never throws if it vanishes
// mid-check.
func (s *Store) readStaging(ctx context.Context) (exists bool, phase, version string, err error) {
	var raw *string
	if err = s.pool.QueryRow(
		ctx,
		"SELECT to_regclass($1) IS NOT NULL, obj_description(to_regclass($1), 'pg_class')",
		wikiStagingTable,
	).Scan(&exists, &raw); err != nil {
		return false, "", "", fmt.Errorf("postgres: read staging stamp: %w", err)
	}
	if !exists || raw == nil {
		return exists, stampBuilding, "", nil
	}
	if p, v, ok := strings.Cut(*raw, ":"); ok {
		return true, p, v, nil
	}
	return true, stampBuilding, "", nil
}

// StagingPlan decides what a bulk run should do for the dump version about to be
// embedded: resume a staging already materialized for it, skip a corpus already
// live and embedded at that version, or build from scratch (the default, which
// also covers an interrupted build or a staging left from a different dump).
func (s *Store) StagingPlan(ctx context.Context, version string) (wiki.BulkPlan, error) {
	exists, phase, stampVersion, err := s.readStaging(ctx)
	if err != nil {
		return 0, err
	}
	if exists {
		if phase == stampReady && stampVersion == version {
			return wiki.PlanResumeEmbed, nil
		}
		return wiki.PlanBuild, nil
	}
	current, err := s.liveCurrentAt(ctx, version)
	if err != nil {
		return 0, err
	}
	if current {
		return wiki.PlanAlreadyCurrent, nil
	}
	return wiki.PlanBuild, nil
}

// liveCurrentAt reports whether the live corpus is checkpointed at version and
// holds no unembedded chunks - the state a completed swap leaves. The checkpoint
// advances only inside the swap transaction, so a stored version equal to the
// dump's means that dump is already live and fully embedded.
func (s *Store) liveCurrentAt(ctx context.Context, version string) (bool, error) {
	var (
		stored *string
		nulls  int64
	)
	if err := s.pool.QueryRow(
		ctx, `
		SELECT (SELECT dump_version FROM wiki_sync_state LIMIT 1),
		       (SELECT count(*) FROM wiki_chunks WHERE embedding IS NULL)`,
	).Scan(&stored, &nulls); err != nil {
		return false, fmt.Errorf("postgres: read live currency: %w", err)
	}
	return stored != nil && *stored == version && nulls == 0, nil
}

// ResetStaging drops any surviving staging table and creates a fresh unindexed
// one stamped building:version. LIKE copies the columns, NOT NULLs, and
// defaults but no primary key or HNSW index, keeping the bulk load fast; the
// index and key are built once at finalize. The drop, create, and stamp run in
// one transaction so a concurrent stagingExists/EmbedInProgress check never sees
// the table briefly absent mid-rebuild: it observes the old table until commit,
// then the new one, never a window where a delta sync could slip past the guard.
func (s *Store) ResetStaging(ctx context.Context, version string) error {
	stamp := stagingStamp(stampBuilding, version)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+wikiStagingTable); err != nil {
			return fmt.Errorf("drop staging: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (LIKE wiki_chunks INCLUDING DEFAULTS)", wikiStagingTable)); err != nil {
			return fmt.Errorf("create staging: %w", err)
		}
		return stampStagingTx(ctx, tx, stamp)
	})
	if err != nil {
		return fmt.Errorf("postgres: reset staging: %w", err)
	}
	return nil
}

// MarkStagingReady re-stamps a fully ingested, embedding-carried staging table
// as a resume target for version. A run that crashes before this leaves a
// building stamp the next run rebuilds.
func (s *Store) MarkStagingReady(ctx context.Context, version string) error {
	return s.stampStaging(ctx, stagingStamp(stampReady, version))
}

// UpsertStagingChunks inserts chunks (NULL embedding) into the staging table in
// one batch. ResetStaging leaves staging empty and multistream page ids are
// unique, so a plain INSERT never conflicts; the primary key is added at
// finalize.
func (s *Store) UpsertStagingChunks(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	stmt := fmt.Sprintf(
		"INSERT INTO %s (page_id, chunk_index, title, url, revision_id, corpus, content, section, kind) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		wikiStagingTable,
	)
	batch := &pgx.Batch{}
	for _, c := range chunks {
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: stage chunk page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		if !c.Kind.Valid() {
			return fmt.Errorf("postgres: stage chunk page %d: invalid kind %q", c.PageID, c.Kind)
		}
		batch.Queue(stmt, c.PageID, int32(c.ChunkIndex), c.Title, c.URL, c.RevisionID, c.Corpus, c.Content, c.Section, string(c.Kind))
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range chunks {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: upsert staging chunks: %w", err)
		}
	}
	return nil
}

// CarryForwardEmbeddings copies each live chunk's embedding onto the matching
// staging row whose content is byte-identical, so unchanged chunks are not
// re-embedded; it returns the number of rows carried. Changed and new chunks
// keep their NULL embedding and are embedded by the run, and chunks that left
// the corpus never entered staging - so orphans cannot survive the rebuild.
func (s *Store) CarryForwardEmbeddings(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s s SET embedding = l.embedding
		FROM wiki_chunks l
		WHERE s.page_id = l.page_id
		  AND s.chunk_index = l.chunk_index
		  AND s.content = l.content
		  AND l.embedding IS NOT NULL`, wikiStagingTable))
	if err != nil {
		return 0, fmt.Errorf("postgres: carry forward embeddings: %w", err)
	}
	return tag.RowsAffected(), nil
}

// StagingRemaining counts the staging chunks still to embed and their content
// characters, feeding the run's pending total and the dry-run cost estimate.
func (s *Store) StagingRemaining(ctx context.Context) (domain.WikiRemaining, error) {
	var r domain.WikiRemaining
	if err := s.pool.QueryRow(
		ctx, fmt.Sprintf(`
		SELECT count(*)::bigint, count(DISTINCT page_id)::bigint,
		       COALESCE(sum(length(content)), 0)::bigint
		FROM %s WHERE embedding IS NULL`, wikiStagingTable),
	).Scan(&r.Chunks, &r.Pages, &r.Chars); err != nil {
		return domain.WikiRemaining{}, fmt.Errorf("postgres: staging remaining: %w", err)
	}
	return r, nil
}

// UnembeddedStaging returns up to limit staging chunks still lacking an
// embedding, in keyset order. The WHERE embedding IS NULL filter is the resume
// cursor: a crash leaves the embedded rows committed and the next run reads only
// what is left.
func (s *Store) UnembeddedStaging(ctx context.Context, limit int) ([]domain.WikiChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded staging: limit %d out of range", limit)
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT page_id, chunk_index, title, url, revision_id, corpus, content
		FROM %s WHERE embedding IS NULL
		ORDER BY page_id, chunk_index
		LIMIT $1`, wikiStagingTable), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded staging: %w", err)
	}
	defer rows.Close()
	out := []domain.WikiChunk{}
	for rows.Next() {
		var (
			c   domain.WikiChunk
			idx int32
		)
		if err := rows.Scan(&c.PageID, &idx, &c.Title, &c.URL, &c.RevisionID, &c.Corpus, &c.Content); err != nil {
			return nil, fmt.Errorf("postgres: scan staging chunk: %w", err)
		}
		c.ChunkIndex = int(idx)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: read staging chunks: %w", err)
	}
	return out, nil
}

// UnembeddedChunks returns up to limit live chunks ordered after cur. The delta
// sync embeds changed chunks in the live table, reading the ones it just
// upserted back through this keyset scan.
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

// UpdateStagingEmbeddings writes embeddings onto existing staging rows. It loads
// (page_id, chunk_index, embedding) into a temp table via a text-format COPY
// (pgvector-go has no binary halfvec encoder, so a binary COPY corrupts the
// stream) and joins it back, so a batch update is one COPY plus one UPDATE. The
// temp table is dropped at commit. Every chunk must carry a full-dimension
// embedding.
func (s *Store) UpdateStagingEmbeddings(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	for _, c := range chunks {
		if len(c.Embedding) != domain.EmbeddingDim {
			return fmt.Errorf("postgres: update staging page %d: embedding has %d dims, want %d", c.PageID, len(c.Embedding), domain.EmbeddingDim)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: update staging page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		if err := w.Write([]string{
			strconv.FormatInt(c.PageID, 10),
			strconv.Itoa(c.ChunkIndex),
			formatHalfVec(c.Embedding),
		}); err != nil {
			return fmt.Errorf("postgres: encode staging update row for page %d: %w", c.PageID, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("postgres: encode staging update rows: %w", err)
	}

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			"CREATE TEMP TABLE staging_embed_update (page_id bigint, chunk_index integer, embedding halfvec(%d)) ON COMMIT DROP",
			domain.EmbeddingDim,
		)); err != nil {
			return fmt.Errorf("create temp table: %w", err)
		}
		if _, err := tx.Conn().PgConn().CopyFrom(ctx, &buf,
			"COPY staging_embed_update (page_id, chunk_index, embedding) FROM STDIN WITH (FORMAT csv)"); err != nil {
			return fmt.Errorf("copy temp embeddings: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s s SET embedding = u.embedding
			FROM staging_embed_update u
			WHERE s.page_id = u.page_id AND s.chunk_index = u.chunk_index`, wikiStagingTable)); err != nil {
			return fmt.Errorf("apply staging embeddings: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: update staging embeddings: %w", err)
	}
	return nil
}

// undefinedTableCode is PostgreSQL's SQLSTATE for "relation does not exist". A
// single-chunk embedding write hits it when the staging table is gone because
// the corpus was already swapped live, which the worker treats as an obsolete
// job rather than an error.
const undefinedTableCode = "42P01"

// SetStagingChunkEmbedding writes one embedding onto the staging row identified
// by (pageID, chunkIndex). The vector is sent in pgvector's text form and cast
// server-side - the same text-format path UpdateStagingEmbeddings uses, because
// the runtime staging table is outside the sqlc-modeled schema and pgvector-go
// has no binary halfvec encoder. The write is idempotent: a redelivered job
// rewrites the same vector. updated is false, with no error, when nothing
// matched - either no staging row carries that identity, or the staging table is
// gone because the corpus was already swapped live - so the embedding worker
// drops a late or duplicate job instead of retrying it forever.
func (s *Store) SetStagingChunkEmbedding(ctx context.Context, pageID int64, chunkIndex int, embedding []float32) (bool, error) {
	if len(embedding) != domain.EmbeddingDim {
		return false, fmt.Errorf("postgres: set staging embedding page %d: embedding has %d dims, want %d", pageID, len(embedding), domain.EmbeddingDim)
	}
	if chunkIndex < 0 || chunkIndex > math.MaxInt32 {
		return false, fmt.Errorf("postgres: set staging embedding page %d: chunk index %d out of range", pageID, chunkIndex)
	}
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET embedding = $1::halfvec WHERE page_id = $2 AND chunk_index = $3", wikiStagingTable,
	),
		formatHalfVec(embedding), pageID, int32(chunkIndex))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == undefinedTableCode {
			return false, nil
		}
		return false, fmt.Errorf("postgres: set staging embedding page %d chunk %d: %w", pageID, chunkIndex, err)
	}
	return tag.RowsAffected() > 0, nil
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

// stampStaging records stamp as the staging table's comment. The stamp lives on
// the table and is dropped with it, so it never outlives staging.
func (s *Store) stampStaging(ctx context.Context, stamp string) error {
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return stampStagingTx(ctx, tx, stamp)
	}); err != nil {
		return fmt.Errorf("postgres: stamp staging table: %w", err)
	}
	return nil
}

// stampStagingTx writes the staging comment within tx. COMMENT takes no bind
// parameters, so the statement is assembled server-side with format(%L), which
// escapes the value safely; the table name is a trusted constant.
func stampStagingTx(ctx context.Context, tx pgx.Tx, stamp string) error {
	var stmt string
	if err := tx.QueryRow(
		ctx,
		fmt.Sprintf("SELECT format('COMMENT ON TABLE %s IS %%L', $1::text)", wikiStagingTable),
		stamp,
	).Scan(&stmt); err != nil {
		return fmt.Errorf("build staging stamp: %w", err)
	}
	if _, err := tx.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("stamp staging table: %w", err)
	}
	return nil
}

// FinalizeStaging builds the staging index, then swaps it into the live table
// and checkpoints the corpus at version. The swap revalidates that staging is a
// complete embedded corpus inside its own transaction, so it never swaps a
// partial or unembedded corpus and a concurrent reader sees the old corpus until
// commit.
func (s *Store) FinalizeStaging(ctx context.Context, corpus, version string, lastChangeTS time.Time, maintenanceWorkMem string, maxParallelWorkers int) error {
	if err := s.buildStagingIndex(ctx, maintenanceWorkMem, maxParallelWorkers); err != nil {
		return err
	}
	return s.swapStaging(ctx, corpus, version, lastChangeTS)
}

// validateStagingTx refuses the swap unless staging is a non-empty, fully
// embedded corpus. Staging is built solely from the current dump, so it is the
// authority for the new corpus; there is no count comparison against live, whose
// row set is intentionally allowed to differ (it may hold orphans the rebuild
// drops). Running inside the swap transaction makes the check atomic with the
// rename.
func (s *Store) validateStagingTx(ctx context.Context, tx pgx.Tx) error {
	var staging, nullEmbeddings int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+wikiStagingTable).Scan(&staging); err != nil {
		return fmt.Errorf("count staging chunks: %w", err)
	}
	if staging == 0 {
		return errors.New("staging corpus is empty, refusing to swap")
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
// in the same transaction, recording the dump version now live so a later run
// can tell the corpus is current.
func (s *Store) swapStaging(ctx context.Context, corpus, version string, lastChangeTS time.Time) error {
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

	checkpoint := db.UpsertWikiSyncStateParams{
		Corpus:       corpus,
		DumpVersion:  pgtype.Text{String: version, Valid: version != ""},
		LastChangeTs: pgtype.Timestamptz{Time: lastChangeTS, Valid: !lastChangeTS.IsZero()},
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
		if err := s.queries.WithTx(tx).UpsertWikiSyncState(ctx, checkpoint); err != nil {
			return fmt.Errorf("checkpoint corpus: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: swap staging into place: %w", err)
	}
	return nil
}

// EmbedInProgress reports whether a bulk rebuild is mid-flight, so the delta
// sync refuses to mutate live while one is. The staging table exists from the
// start of ingest (ResetStaging) until a completed swap drops it, covering both
// the ingest and embed phases: delta must not write to live during either,
// because the swap replaces live wholesale and would discard delta's work. A
// staging table left by a crashed bulk therefore also blocks delta until the
// next bulk run (which ResetStaging clears) finishes - the conservative choice,
// since a half-built corpus is exactly when delta's in-place writes are unsafe.
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
