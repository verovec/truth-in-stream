package postgres

import (
	"context"
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
// swaps it into evidence_chunks atomically: ingest fills staging from the dump,
// unchanged embeddings carry forward from live, the remaining chunks embed in
// place, and a rename swaps staging over live. The staging table, its index,
// and the rename are runtime DDL that sqlc cannot model (it compiles DML
// against the migration schema, which omits this transient table), so they run
// as the raw statements below; the live-corpus DML still goes through generated
// queries. The names are constants, never interpolated user input.
const (
	evidenceStagingTable       = "evidence_chunks_staging"
	evidenceStagingPK          = "evidence_chunks_staging_pkey"
	evidenceStagingHNSWIndex   = "evidence_chunks_staging_embedding_hnsw"
	evidenceChunksHNSWIndex    = "evidence_chunks_embedding_hnsw"
	evidenceChunksPK           = "evidence_chunks_pkey"
	evidenceChunksOldTable     = "evidence_chunks_old"
	evidenceChunksOldPK        = "evidence_chunks_old_pkey"
	evidenceChunksOldHNSWIndex = "evidence_chunks_old_embedding_hnsw"

	// The secondary indexes a rebuilt corpus must also carry so a staging swap
	// leaves the live table with the SAME index set the migrations define, never a
	// degraded one. Each is built on staging under its own name, then swapped to
	// the canonical name the migration schema expects (the live one is renamed
	// aside first and dropped with the old table). Without this a rebuild
	// (make reingest) would permanently drop the 0017 hybrid-FTS GIN index -
	// silently degrading lexical search to a sequential scan - and the VER-203
	// content-hash index.
	evidenceStagingGINIndex         = "evidence_chunks_staging_search_vector_gin"
	evidenceChunksGINIndex          = "evidence_chunks_search_vector_gin"
	evidenceChunksOldGINIndex       = "evidence_chunks_old_search_vector_gin"
	evidenceStagingContentHashIndex = "evidence_chunks_staging_source_content_hash"
	evidenceChunksContentHashIndex  = "evidence_chunks_source_content_hash"
	evidenceChunksOldContentHashIdx = "evidence_chunks_old_source_content_hash"

	hnswM              int = 16
	hnswEfConstruction int = 200
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

// notDuplicate excludes the near-duplicate chunks the volume-control gate
// withheld (VER-203, measure 2): they are stored with a NULL embedding and a
// duplicate metadata flag for provenance, so the live un-embedded scans must skip
// them - otherwise a bulk-into-live producer would enqueue them, the fleet would
// embed them, and the gate would be defeated. Staging never holds them (a rebuild
// loads fresh from the dump), so only the live scans carry this predicate.
const notDuplicate = ` AND NOT (metadata @> '{"duplicate": true}')`

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
		evidenceStagingTable,
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
		SELECT (SELECT dump_version FROM evidence_sync_state LIMIT 1),
		       (SELECT count(*) FROM evidence_chunks WHERE embedding IS NULL)`,
	).Scan(&stored, &nulls); err != nil {
		return false, fmt.Errorf("postgres: read live currency: %w", err)
	}
	return stored != nil && *stored == version && nulls == 0, nil
}

// ResetStaging drops any surviving staging table and creates a fresh unindexed
// one stamped building:version. LIKE copies the columns, NOT NULLs, defaults,
// and - INCLUDING GENERATED - the content_hash generation expression, but no
// primary key or HNSW index, keeping the bulk load fast; the index and key are
// built once at finalize. Copying the generated column matters because staging
// is renamed over the live table at swap: without it the swapped-in corpus would
// lose the generated content_hash the volume-control short-circuit reads. The
// drop, create, and stamp run in
// one transaction so a concurrent stagingExists/EmbedInProgress check never sees
// the table briefly absent mid-rebuild: it observes the old table until commit,
// then the new one, never a window where a delta sync could slip past the guard.
func (s *Store) ResetStaging(ctx context.Context, version string) error {
	stamp := stagingStamp(stampBuilding, version)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+evidenceStagingTable); err != nil {
			return fmt.Errorf("drop staging: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE TABLE %s (LIKE evidence_chunks INCLUDING DEFAULTS INCLUDING GENERATED)", evidenceStagingTable)); err != nil {
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
// one batch. ResetStaging leaves staging empty and multistream document ids are
// unique, so a plain INSERT never conflicts; the primary key is added at
// finalize. Staging becomes the live table at swap, so it carries every served
// column (title, url, kind, metadata), not just the embed inputs.
func (s *Store) UpsertStagingChunks(ctx context.Context, chunks []domain.EvidenceChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	stmt := fmt.Sprintf(
		"INSERT INTO %s (source, external_id, chunk_index, title, url, content, kind, metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)",
		evidenceStagingTable,
	)
	batch := &pgx.Batch{}
	for _, c := range chunks {
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: stage chunk %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
		}
		if !c.Kind.Valid() {
			return fmt.Errorf("postgres: stage chunk %s/%s: invalid kind %q", c.Source, c.ExternalID, c.Kind)
		}
		meta, err := marshalMetadata(c.Metadata)
		if err != nil {
			return fmt.Errorf("postgres: stage chunk %s/%s: %w", c.Source, c.ExternalID, err)
		}
		batch.Queue(stmt, c.Source, c.ExternalID, int32(c.ChunkIndex), c.Title, c.URL, c.Content, string(c.Kind), meta)
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
		FROM evidence_chunks l
		WHERE s.source = l.source
		  AND s.external_id = l.external_id
		  AND s.chunk_index = l.chunk_index
		  AND s.content = l.content
		  AND l.embedding IS NOT NULL`, evidenceStagingTable))
	if err != nil {
		return 0, fmt.Errorf("postgres: carry forward embeddings: %w", err)
	}
	return tag.RowsAffected(), nil
}

// StagingRemaining counts the staging chunks still to embed and their content
// characters, feeding the run's pending total and the dry-run cost estimate.
func (s *Store) StagingRemaining(ctx context.Context) (domain.EvidenceRemaining, error) {
	var r domain.EvidenceRemaining
	if err := s.pool.QueryRow(
		ctx, fmt.Sprintf(`
		SELECT count(*)::bigint, count(DISTINCT (source, external_id))::bigint,
		       COALESCE(sum(length(content)), 0)::bigint
		FROM %s WHERE embedding IS NULL`, evidenceStagingTable),
	).Scan(&r.Chunks, &r.Documents, &r.Chars); err != nil {
		return domain.EvidenceRemaining{}, fmt.Errorf("postgres: staging remaining: %w", err)
	}
	return r, nil
}

// CountUnembeddedStaging counts the staging chunks still lacking an embedding.
// The producer's drain wait polls it every few seconds, so unlike
// StagingRemaining it counts rows only - it never reads content or counts
// distinct documents - keeping each poll cheap even on a large staging table.
func (s *Store) CountUnembeddedStaging(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(
		ctx, fmt.Sprintf("SELECT count(*)::bigint FROM %s WHERE embedding IS NULL", evidenceStagingTable),
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count un-embedded staging: %w", err)
	}
	return n, nil
}

// UnembeddedStaging returns up to limit staging chunks still lacking an
// embedding, ordered after cur in keyset order and carrying the metadata the
// producer maps to a job priority. It LEFT JOINs the live table to carry forward
// each chunk's prior importance (the offline clustering job writes importance
// into the live metadata after a corpus is embedded), so the producer
// prioritizes a re-ingest by the last run's clustering and falls back to the
// kind heuristic for a new chunk with no prior importance. The join matches on
// content as well as identity - exactly as CarryForwardEmbeddings does - so a
// chunk whose text changed does not inherit the stale importance of the old
// text. The WHERE embedding IS NULL filter is the resume cursor: a re-run pages
// from the start and the fleet has already filled the embedded prefix.
func (s *Store) UnembeddedStaging(ctx context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded staging: limit %d out of range", limit)
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT s.source, s.external_id, s.chunk_index, s.content, s.kind, (l.metadata->>'importance')::float8
		FROM %s s
		LEFT JOIN evidence_chunks l
		  ON l.source = s.source AND l.external_id = s.external_id AND l.chunk_index = s.chunk_index AND l.content = s.content
		WHERE s.embedding IS NULL
		  AND (s.source, s.external_id, s.chunk_index) > ($1::text, $2::text, $3::integer)
		ORDER BY s.source, s.external_id, s.chunk_index
		LIMIT $4`, evidenceStagingTable), cur.Source, cur.ExternalID, cur.ChunkIndex, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded staging: %w", err)
	}
	defer rows.Close()
	out, err := scanEmbedQueue(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: read staging chunks: %w", err)
	}
	return out, nil
}

// CountUnembeddedLive counts the live chunks still lacking an embedding. The
// bulk-into-live producer reports it as the pending total before publishing; the
// corpus is already queryable, so this is progress, not a usability gate.
func (s *Store) CountUnembeddedLive(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(
		ctx, "SELECT count(*)::bigint FROM evidence_chunks WHERE embedding IS NULL"+notDuplicate,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count un-embedded live: %w", err)
	}
	return n, nil
}

// CountUnembeddedLiveSource counts the un-embedded live chunks of one source.
// The stats bulk-into-live producer reports it as the pending total before
// publishing; scoping to the source keeps a stats run from counting another
// source's pending chunks (the wiki, crawl, and stat sources share the table).
func (s *Store) CountUnembeddedLiveSource(ctx context.Context, source string) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(
		ctx, "SELECT count(*)::bigint FROM evidence_chunks WHERE embedding IS NULL AND source = $1"+notDuplicate, source,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("postgres: count un-embedded live for source %q: %w", source, err)
	}
	return n, nil
}

// UnembeddedLiveSource returns up to limit un-embedded live chunks of one source
// in keyset order after cur, the source-scoped twin of UnembeddedLive the stats
// producer uses so a run enqueues only its own source's pending passages. The
// statistical sources carry no clustering importance, so it reads none; the
// producer falls back to its static kind/length heuristic for priority. The
// embedding IS NULL filter is the resume cursor.
func (s *Store) UnembeddedLiveSource(ctx context.Context, source string, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded live source: limit %d out of range", limit)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source, external_id, chunk_index, content, kind, NULL::float8
		FROM evidence_chunks
		WHERE embedding IS NULL
		  AND source = $1`+notDuplicate+`
		  AND (source, external_id, chunk_index) > ($1::text, $2::text, $3::integer)
		ORDER BY source, external_id, chunk_index
		LIMIT $4`, source, cur.ExternalID, cur.ChunkIndex, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded live source %q: %w", source, err)
	}
	defer rows.Close()
	out, err := scanEmbedQueue(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: read live source chunks: %w", err)
	}
	return out, nil
}

// LiveRemaining counts the live chunks still to embed and their content
// characters, feeding the bulk-into-live dry-run cost estimate. It mirrors
// StagingRemaining for the live corpus.
func (s *Store) LiveRemaining(ctx context.Context) (domain.EvidenceRemaining, error) {
	var r domain.EvidenceRemaining
	if err := s.pool.QueryRow(
		ctx, `
		SELECT count(*)::bigint, count(DISTINCT (source, external_id))::bigint,
		       COALESCE(sum(length(content)), 0)::bigint
		FROM evidence_chunks WHERE embedding IS NULL`+notDuplicate,
	).Scan(&r.Chunks, &r.Documents, &r.Chars); err != nil {
		return domain.EvidenceRemaining{}, fmt.Errorf("postgres: live remaining: %w", err)
	}
	return r, nil
}

// UnembeddedLive returns up to limit live chunks still lacking an embedding,
// ordered after cur in keyset order and carrying the metadata (kind, importance)
// the bulk-into-live producer maps to a job priority. It reads importance from
// the live metadata jsonb - the clustering job writes it after a corpus is
// embedded - so a re-ingest prioritizes by the last run's clustering and a first
// build falls back to the producer's static heuristic. The embedding IS NULL
// filter is the resume cursor. It is distinct from UnembeddedChunks, which the
// delta sync uses without the publish metadata.
func (s *Store) UnembeddedLive(ctx context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded live: limit %d out of range", limit)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source, external_id, chunk_index, content, kind, (metadata->>'importance')::float8
		FROM evidence_chunks
		WHERE embedding IS NULL`+notDuplicate+`
		  AND (source, external_id, chunk_index) > ($1::text, $2::text, $3::integer)
		ORDER BY source, external_id, chunk_index
		LIMIT $4`, cur.Source, cur.ExternalID, cur.ChunkIndex, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded live: %w", err)
	}
	defer rows.Close()
	out, err := scanEmbedQueue(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: read live chunks: %w", err)
	}
	return out, nil
}

// scanEmbedQueue reads the embed-order projection (key, content, kind, and an
// optional importance) shared by the staging and live pending scans, carrying
// importance into the chunk metadata so the producer prioritizes by it.
func scanEmbedQueue(rows pgx.Rows) ([]domain.EvidenceChunk, error) {
	out := []domain.EvidenceChunk{}
	for rows.Next() {
		var (
			c          domain.EvidenceChunk
			idx        int32
			kind       string
			importance *float64
		)
		if err := rows.Scan(&c.Source, &c.ExternalID, &idx, &c.Content, &kind, &importance); err != nil {
			return nil, fmt.Errorf("scan embed-queue chunk: %w", err)
		}
		c.ChunkIndex = int(idx)
		c.Kind = domain.EvidenceChunkKind(kind)
		if importance != nil {
			c.Metadata = domain.WikiMetadata{Importance: importance}.Map()
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UnembeddedChunks returns up to limit live chunks ordered after cur. The delta
// sync embeds changed chunks in the live table, reading the ones it just
// upserted back through this keyset scan.
func (s *Store) UnembeddedChunks(ctx context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	if limit < 1 || limit > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: unembedded chunks: limit %d out of range", limit)
	}
	rows, err := s.queries.UnembeddedEvidenceChunks(ctx, db.UnembeddedEvidenceChunksParams{
		AfterSource:     cur.Source,
		AfterExternalID: cur.ExternalID,
		AfterChunkIndex: cur.ChunkIndex,
		RowLimit:        int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: unembedded chunks: %w", err)
	}
	out := make([]domain.EvidenceChunk, len(rows))
	for i, r := range rows {
		meta, err := unmarshalMetadata(r.Metadata)
		if err != nil {
			return nil, fmt.Errorf("postgres: unembedded chunks: %s/%s#%d: %w", r.Source, r.ExternalID, r.ChunkIndex, err)
		}
		out[i] = domain.EvidenceChunk{
			Source:     r.Source,
			ExternalID: r.ExternalID,
			ChunkIndex: int(r.ChunkIndex),
			Title:      r.Title,
			URL:        r.Url,
			Content:    r.Content,
			Kind:       domain.EvidenceChunkKind(r.Kind),
			Metadata:   meta,
		}
	}
	return out, nil
}

// undefinedTableCode is PostgreSQL's SQLSTATE for "relation does not exist". A
// single-chunk embedding write hits it when the staging table is gone because
// the corpus was already swapped live, which the worker treats as an obsolete
// job rather than an error.
const undefinedTableCode = "42P01"

// SetStagingChunkEmbedding writes one embedding onto the staging row identified
// by (source, externalID, chunkIndex). The vector is sent in pgvector's text
// form and cast server-side - the same text-format path setChunkEmbeddingsInto
// uses, because the runtime staging table is outside the sqlc-modeled schema and
// pgvector-go has no binary halfvec encoder. The write is idempotent. updated is
// false, with no error, when nothing matched - either no staging row carries
// that identity, or the staging table is gone because the corpus was already
// swapped live - so the embedding worker drops a late or duplicate job.
func (s *Store) SetStagingChunkEmbedding(ctx context.Context, source, externalID string, chunkIndex int, embedding []float32) (bool, error) {
	if len(embedding) != domain.EmbeddingDim {
		return false, fmt.Errorf("postgres: set staging embedding %s/%s: embedding has %d dims, want %d", source, externalID, len(embedding), domain.EmbeddingDim)
	}
	if chunkIndex < 0 || chunkIndex > math.MaxInt32 {
		return false, fmt.Errorf("postgres: set staging embedding %s/%s: chunk index %d out of range", source, externalID, chunkIndex)
	}
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET embedding = $1::halfvec WHERE source = $2 AND external_id = $3 AND chunk_index = $4", evidenceStagingTable,
	),
		formatHalfVec(embedding), source, externalID, int32(chunkIndex))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == undefinedTableCode {
			return false, nil
		}
		return false, fmt.Errorf("postgres: set staging embedding %s/%s#%d: %w", source, externalID, chunkIndex, err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetLiveChunkEmbeddings writes a batch of embeddings straight into the live
// evidence_chunks table in one statement, so each NULL->vector write makes its
// chunk searchable immediately and the corpus grows monotonically while the
// fleet embeds. It is the bulk-into-live worker's write path. matched is how
// many rows the batch updated; the rest reference a chunk that left the corpus
// or whose content changed under a re-ingest (the content guard refuses to
// attach a vector to text it was not computed from), which the worker treats as
// obsolete and drops. The write is idempotent.
func (s *Store) SetLiveChunkEmbeddings(ctx context.Context, chunks []domain.EvidenceChunk) (int, error) {
	return s.setChunkEmbeddingsInto(ctx, "evidence_chunks", chunks)
}

// SetStagingChunkEmbeddings writes a batch of embeddings into the staging table
// in one statement, the atomic-rebuild worker's write path. matched is zero with
// no error when the staging table is gone because the corpus was already swapped
// live, so the worker drops a late job instead of retrying it forever.
func (s *Store) SetStagingChunkEmbeddings(ctx context.Context, chunks []domain.EvidenceChunk) (int, error) {
	return s.setChunkEmbeddingsInto(ctx, evidenceStagingTable, chunks)
}

// setChunkEmbeddingsInto writes the batch's embeddings into table in a single
// text-form UPDATE ... FROM unnest, the only halfvec-safe bulk write: the
// vectors travel as a text[] of pgvector literals and are cast to halfvec
// server-side, since pgx's binary halfvec path corrupts the column. The join
// matches on content as well as identity so a chunk whose text changed since the
// job was published is not given a vector computed from the old text - it simply
// does not match and is left for the fresh job. table is a trusted constant,
// never user input. A missing table (staging dropped by a completed swap) yields
// matched zero with no error.
func (s *Store) setChunkEmbeddingsInto(ctx context.Context, table string, chunks []domain.EvidenceChunk) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}
	sources := make([]string, len(chunks))
	externalIDs := make([]string, len(chunks))
	indexes := make([]int32, len(chunks))
	contents := make([]string, len(chunks))
	vectors := make([]string, len(chunks))
	for i, c := range chunks {
		if len(c.Embedding) != domain.EmbeddingDim {
			return 0, fmt.Errorf("postgres: set embedding %s/%s#%d: embedding has %d dims, want %d", c.Source, c.ExternalID, c.ChunkIndex, len(c.Embedding), domain.EmbeddingDim)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return 0, fmt.Errorf("postgres: set embedding %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
		}
		sources[i] = c.Source
		externalIDs[i] = c.ExternalID
		indexes[i] = int32(c.ChunkIndex)
		contents[i] = c.Content
		vectors[i] = formatHalfVec(c.Embedding)
	}
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s AS w
		SET embedding = v.embedding::halfvec, synced_at = now()
		FROM unnest($1::text[], $2::text[], $3::integer[], $4::text[], $5::text[]) AS v(source, external_id, chunk_index, content, embedding)
		WHERE w.source = v.source
		  AND w.external_id = v.external_id
		  AND w.chunk_index = v.chunk_index
		  AND w.content = v.content`, table),
		sources, externalIDs, indexes, contents, vectors)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == undefinedTableCode {
			return 0, nil
		}
		return 0, fmt.Errorf("postgres: set chunk embeddings into %s: %w", table, err)
	}
	return int(tag.RowsAffected()), nil
}

// formatHalfVec renders an embedding as pgvector's text form, "[1,2,3]", for a
// text-format cast.
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
		fmt.Sprintf("SELECT format('COMMENT ON TABLE %s IS %%L', $1::text)", evidenceStagingTable),
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
// and checkpoints the source at version. The swap revalidates that staging is a
// complete embedded corpus inside its own transaction, so it never swaps a
// partial or unembedded corpus and a concurrent reader sees the old corpus until
// commit.
func (s *Store) FinalizeStaging(ctx context.Context, source, version string, lastChangeTS time.Time, maintenanceWorkMem string, maxParallelWorkers int) error {
	if err := s.buildStagingIndex(ctx, maintenanceWorkMem, maxParallelWorkers); err != nil {
		return err
	}
	return s.swapStaging(ctx, source, version, lastChangeTS)
}

// validateStagingTx refuses the swap unless staging is a non-empty, fully
// embedded corpus. Staging is built solely from the current dump, so it is the
// authority for the new corpus; there is no count comparison against live, whose
// row set is intentionally allowed to differ (it may hold orphans the rebuild
// drops). Running inside the swap transaction makes the check atomic with the
// rename.
func (s *Store) validateStagingTx(ctx context.Context, tx pgx.Tx) error {
	var staging, nullEmbeddings int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+evidenceStagingTable).Scan(&staging); err != nil {
		return fmt.Errorf("count staging chunks: %w", err)
	}
	if staging == 0 {
		return errors.New("staging corpus is empty, refusing to swap")
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+evidenceStagingTable+" WHERE embedding IS NULL").Scan(&nullEmbeddings); err != nil {
		return fmt.Errorf("count staging null embeddings: %w", err)
	}
	if nullEmbeddings != 0 {
		return fmt.Errorf("%d staged chunks are unembedded", nullEmbeddings)
	}
	return nil
}

// buildStagingIndex adds the primary key and builds every secondary index the
// live table carries onto the loaded staging table - the HNSW halfvec index, the
// 0017 hybrid-FTS GIN index, and the VER-203 (source, content_hash) btree - with
// the index-build memory and parallelism raised for the transaction only. It
// builds the full index set (not just the HNSW) so the swap leaves the live table
// with the same indexes the migrations define; omitting the GIN would silently
// degrade lexical search to a sequential scan after every rebuild. Every step is
// idempotent so a resumed finalize is safe.
func (s *Store) buildStagingIndex(ctx context.Context, maintenanceWorkMem string, maxParallelWorkers int) error {
	addPK := fmt.Sprintf(`DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '%s') THEN
        ALTER TABLE %s ADD CONSTRAINT %s PRIMARY KEY (source, external_id, chunk_index);
    END IF;
END $$;`, evidenceStagingPK, evidenceStagingTable, evidenceStagingPK)

	createHNSW := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding halfvec_cosine_ops) WITH (m = %d, ef_construction = %d)",
		evidenceStagingHNSWIndex, evidenceStagingTable, hnswM, hnswEfConstruction,
	)
	createGIN := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s USING gin (search_vector)",
		evidenceStagingGINIndex, evidenceStagingTable,
	)
	createContentHash := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON %s (source, content_hash)",
		evidenceStagingContentHashIndex, evidenceStagingTable,
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
		if _, err := tx.Exec(ctx, createHNSW); err != nil {
			return fmt.Errorf("build staging hnsw index: %w", err)
		}
		if _, err := tx.Exec(ctx, createGIN); err != nil {
			return fmt.Errorf("build staging fts gin index: %w", err)
		}
		if _, err := tx.Exec(ctx, createContentHash); err != nil {
			return fmt.Errorf("build staging content-hash index: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: build staging index: %w", err)
	}
	return nil
}

// swapStaging replaces the live evidence_chunks with the freshly indexed staging
// table in one transaction: readers see the old corpus until commit and the new
// one after, never a partial one. The old table's index and constraint are
// renamed aside first so the staging objects can take the canonical names that
// the migration schema and later ingests expect. The source checkpoint advances
// in the same transaction, recording the dump version now live so a later run
// can tell the corpus is current.
func (s *Store) swapStaging(ctx context.Context, source, version string, lastChangeTS time.Time) error {
	stmts := []string{
		"DROP TABLE IF EXISTS " + evidenceChunksOldTable,
		// IF EXISTS on every live-index rename so a corpus whose index was dropped
		// (an ops rebuild) still swaps; the staging indexes take the canonical names
		// next. Each secondary index (GIN, content_hash) is renamed aside just like
		// the HNSW so its canonical name is free before staging's takes it, and it
		// is dropped with the old table at the end.
		fmt.Sprintf("ALTER INDEX IF EXISTS %s RENAME TO %s", evidenceChunksHNSWIndex, evidenceChunksOldHNSWIndex),
		fmt.Sprintf("ALTER INDEX IF EXISTS %s RENAME TO %s", evidenceChunksGINIndex, evidenceChunksOldGINIndex),
		fmt.Sprintf("ALTER INDEX IF EXISTS %s RENAME TO %s", evidenceChunksContentHashIndex, evidenceChunksOldContentHashIdx),
		fmt.Sprintf("ALTER TABLE evidence_chunks RENAME CONSTRAINT %s TO %s", evidenceChunksPK, evidenceChunksOldPK),
		"ALTER TABLE evidence_chunks RENAME TO " + evidenceChunksOldTable,
		fmt.Sprintf("ALTER INDEX %s RENAME TO %s", evidenceStagingHNSWIndex, evidenceChunksHNSWIndex),
		fmt.Sprintf("ALTER INDEX %s RENAME TO %s", evidenceStagingGINIndex, evidenceChunksGINIndex),
		fmt.Sprintf("ALTER INDEX %s RENAME TO %s", evidenceStagingContentHashIndex, evidenceChunksContentHashIndex),
		fmt.Sprintf("ALTER TABLE %s RENAME CONSTRAINT %s TO %s", evidenceStagingTable, evidenceStagingPK, evidenceChunksPK),
		fmt.Sprintf("ALTER TABLE %s RENAME TO evidence_chunks", evidenceStagingTable),
		"DROP TABLE " + evidenceChunksOldTable,
	}

	checkpoint := db.UpsertEvidenceSyncStateParams{
		Source:       source,
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
		if err := s.queries.WithTx(tx).UpsertEvidenceSyncState(ctx, checkpoint); err != nil {
			return fmt.Errorf("checkpoint source: %w", err)
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
	if err := s.pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", evidenceStagingTable).Scan(&exists); err != nil {
		return false, fmt.Errorf("postgres: check staging table: %w", err)
	}
	return exists, nil
}
