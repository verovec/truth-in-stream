package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// evidenceSourceLockKey is the advisory lock key serializing source claims
// ("wikicorp" in hex, kept stable so an in-flight claim survives the rename).
const evidenceSourceLockKey = int64(0x77696b69636f7270)

// marshalMetadata renders a chunk's metadata map as the jsonb payload. The
// column is NOT NULL, so a nil map becomes "{}" rather than SQL NULL.
func marshalMetadata(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	return b, nil
}

// unmarshalMetadata decodes a jsonb metadata payload back into a map. An empty
// or absent payload is the empty map, never nil, so callers never nil-check.
func unmarshalMetadata(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// EnsureSource claims the store for one encyclopedic source before ingestion
// starts. The wiki delta sync assumes a single encyclopedic corpus per database
// (its change-fraction denominator and bulk plan depend on it), so finding a
// foreign source already checkpointed in evidence_sync_state is fatal. Check and
// claim run in one transaction under an advisory lock, so two concurrent syncs
// cannot both claim different sources. Statistical and crawl ingestion write
// evidence_chunks directly and never checkpoint, so they bypass this guard.
func (s *Store) EnsureSource(ctx context.Context, source string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if err := q.AcquireEvidenceSourceLock(ctx, evidenceSourceLockKey); err != nil {
			return fmt.Errorf("acquire source lock: %w", err)
		}
		other, err := q.GetOtherEvidenceSource(ctx, source)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("check source: %w", err)
		default:
			return fmt.Errorf("store already holds source %q; the encyclopedic corpus is single-source, use a fresh database for %q", other, source)
		}
		if err := q.ClaimEvidenceSource(ctx, source); err != nil {
			return fmt.Errorf("claim source: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: ensure evidence source %q: %w", source, err)
	}
	return nil
}

// SearchEvidence returns the topK embedded evidence chunks closest to query by
// cosine distance, nearest first, as supporting evidence. It mirrors
// Store.Search over the claims corpus; unembedded chunks are excluded by the
// query. A non-empty sources scopes the search to those sources and runs under
// iterative scan so the filter does not under-return; nil (or an empty slice)
// is the unchanged global search, which stays strictly ordered.
func (s *Store) SearchEvidence(ctx context.Context, query []float32, topK, efSearch int, sources []string) ([]domain.EvidenceHit, error) {
	if topK <= 0 || topK > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: search evidence: topK %d out of range", topK)
	}
	if len(query) != domain.EmbeddingDim {
		return nil, fmt.Errorf("postgres: search evidence: query has %d dims, want %d", len(query), domain.EmbeddingDim)
	}

	// An empty (non-nil) slice is coerced to nil so it means "all sources" like
	// nil does, rather than encoding to the empty SQL array `{}` that
	// `source = ANY('{}')` would match against nothing - a silent no-results
	// footgun for a dynamically built, possibly-empty filter.
	if len(sources) == 0 {
		sources = nil
	}
	scoped := sources != nil

	vec := pgvector.NewHalfVector(query)
	var rows []db.SearchEvidenceChunksRow
	var err error
	if s.bqMultiplier > 0 {
		// Two-stage BQ. The coarse bit-index scan must gather coarse_limit
		// candidates before the halfvec rerank, so it runs under iterative_scan
		// (relaxed order is fine - the rerank re-sorts by exact cosine) with
		// hnsw.ef_search raised to the pool size; a bare HNSW scan returns at most
		// ef_search rows, which would silently cap the pool and make a larger
		// multiplier a no-op. coarse_limit floors at the caller's efSearch so a
		// full-recall probe (coverage: topK=1, efSearch=200) keeps its candidate
		// budget through the lossy coarse stage instead of collapsing to
		// multiplier*1 and risking a false not_covered verdict.
		coarse := bqCoarseLimit(s.bqMultiplier, topK, efSearch)
		err = s.searchTuned(ctx, bqEfSearch(coarse), true, func(q *db.Queries) error {
			var e error
			rows, e = s.searchEvidenceBQ(ctx, q, &vec, coarse, topK, sources)
			return e
		})
	} else {
		err = s.searchTuned(ctx, efSearch, scoped, func(q *db.Queries) error {
			var e error
			rows, e = q.SearchEvidenceChunks(ctx, db.SearchEvidenceChunksParams{
				QueryEmbedding: &vec,
				Sources:        sources,
				ResultLimit:    int32(topK),
			})
			return e
		})
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: search evidence: %w", err)
	}

	hits := make([]domain.EvidenceHit, 0, len(rows))
	for _, r := range rows {
		meta, err := unmarshalMetadata(r.Metadata)
		if err != nil {
			return nil, fmt.Errorf("postgres: search evidence: chunk %s/%s#%d: %w", r.Source, r.ExternalID, r.ChunkIndex, err)
		}
		// Only the section is surfaced on a hit, so extract it leniently rather
		// than full-parsing wiki metadata: a source-extensible corpus can carry a
		// source whose metadata is not wiki-shaped, and one such row must not fail
		// the whole search. A missing or non-string section is simply empty.
		section, _ := meta["section"].(string)
		hits = append(hits, domain.EvidenceHit{
			Source:     r.Source,
			ExternalID: r.ExternalID,
			ChunkIndex: int(r.ChunkIndex),
			Title:      r.Title,
			URL:        r.Url,
			Content:    r.Content,
			Kind:       domain.EvidenceChunkKind(r.Kind),
			Section:    section,
			// Cosine distance is in [0,2]; the float32 narrowing matches
			// domain.EvidenceHit and is plenty precise for ranking.
			Distance: float32(r.Distance),
		})
	}
	return hits, nil
}

// hnswEfSearchMax is pgvector's upper bound for hnsw.ef_search (valid range
// 1..1000). A coarse pool larger than this cannot be requested through
// ef_search alone; iterative_scan carries the scan past the initial candidate
// list up to hnsw.max_scan_tuples (default 20000), the practical coarse ceiling.
const hnswEfSearchMax = 1000

// bqCoarseLimit is the size of the coarse candidate pool the BQ stage gathers:
// multiplier*topK, floored at efSearch so a caller that asked for a wider recall
// budget (a full-recall coverage probe passes efSearch=200, topK=1) keeps it
// through the lossy coarse stage rather than collapsing to multiplier*1. It is
// clamped to MaxInt32 for the int32 LIMIT.
func bqCoarseLimit(multiplier, topK, efSearch int) int {
	coarse := int64(multiplier) * int64(topK)
	if int64(efSearch) > coarse {
		coarse = int64(efSearch)
	}
	if coarse > math.MaxInt32 {
		coarse = math.MaxInt32
	}
	return int(coarse)
}

// bqEfSearch is the hnsw.ef_search the coarse scan runs at: the coarse pool
// size, capped at pgvector's ef_search maximum. When the pool exceeds the cap,
// iterative_scan (enabled by the caller) keeps scanning to fill the LIMIT.
func bqEfSearch(coarse int) int {
	if coarse > hnswEfSearchMax {
		return hnswEfSearchMax
	}
	return coarse
}

// searchEvidenceBQ runs the coarse+rerank query for the two-stage
// binary-quantization search: the coarse bit-index stage gathers `coarse`
// candidates by Hamming distance and the halfvec rerank restores exact cosine
// ordering. The caller raises hnsw.ef_search and enables iterative_scan so the
// coarse LIMIT is filled. It returns the shared SearchEvidenceChunksRow shape (a
// direct conversion from the field-identical generated row) so the caller maps
// the result exactly as the single-stage path does.
func (s *Store) searchEvidenceBQ(ctx context.Context, q *db.Queries, vec *pgvector.HalfVector, coarse, topK int, sources []string) ([]db.SearchEvidenceChunksRow, error) {
	bq, err := q.SearchEvidenceChunksBinaryQuantized(ctx, db.SearchEvidenceChunksBinaryQuantizedParams{
		QueryEmbedding: vec,
		Sources:        sources,
		CoarseLimit:    int32(coarse),
		ResultLimit:    int32(topK),
	})
	if err != nil {
		return nil, err
	}
	rows := make([]db.SearchEvidenceChunksRow, len(bq))
	for i, r := range bq {
		rows[i] = db.SearchEvidenceChunksRow(r)
	}
	return rows, nil
}

// UpsertChunks inserts or replaces evidence chunks by (source, external_id,
// chunk_index) in a single batch. Embeddings are never written here; the
// generated upsert keeps an existing embedding only when the chunk content is
// unchanged.
func (s *Store) UpsertChunks(ctx context.Context, chunks []domain.EvidenceChunk) error {
	if len(chunks) == 0 {
		return nil
	}

	params := make([]db.UpsertEvidenceChunkParams, len(chunks))
	for i, c := range chunks {
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: upsert evidence chunk %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
		}
		if !c.Kind.Valid() {
			return fmt.Errorf("postgres: upsert evidence chunk %s/%s: invalid kind %q", c.Source, c.ExternalID, c.Kind)
		}
		meta, err := marshalMetadata(c.Metadata)
		if err != nil {
			return fmt.Errorf("postgres: upsert evidence chunk %s/%s: %w", c.Source, c.ExternalID, err)
		}
		params[i] = db.UpsertEvidenceChunkParams{
			Source:     c.Source,
			ExternalID: c.ExternalID,
			ChunkIndex: int32(c.ChunkIndex),
			Title:      c.Title,
			Url:        c.URL,
			Content:    c.Content,
			Kind:       string(c.Kind),
			Metadata:   meta,
		}
	}

	if err := firstBatchError(s.queries.UpsertEvidenceChunk(ctx, params)); err != nil {
		return fmt.Errorf("postgres: upsert evidence chunks: %w", err)
	}
	return nil
}

// TrimDocuments removes the stale tail of each document's chunks in a single
// batch.
func (s *Store) TrimDocuments(ctx context.Context, trims []domain.EvidenceTrim) error {
	if len(trims) == 0 {
		return nil
	}

	params := make([]db.TrimEvidenceDocumentChunksParams, len(trims))
	for i, tr := range trims {
		if tr.FromIndex < 0 || tr.FromIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: trim evidence document %s/%s: from index %d out of range", tr.Source, tr.ExternalID, tr.FromIndex)
		}
		params[i] = db.TrimEvidenceDocumentChunksParams{
			Source:     tr.Source,
			ExternalID: tr.ExternalID,
			ChunkIndex: int32(tr.FromIndex),
		}
	}

	if err := firstBatchError(s.queries.TrimEvidenceDocumentChunks(ctx, params)); err != nil {
		return fmt.Errorf("postgres: trim evidence chunks: %w", err)
	}
	return nil
}

// DeleteByTitle removes every chunk of each named document within one source in
// one statement. The delta sync uses it for hard deletions, which RecentChanges
// reports by title (page id 0) rather than by page id.
func (s *Store) DeleteByTitle(ctx context.Context, source string, titles []string) error {
	if len(titles) == 0 {
		return nil
	}
	if err := s.queries.DeleteEvidenceByTitle(ctx, db.DeleteEvidenceByTitleParams{
		Source: source,
		Titles: titles,
	}); err != nil {
		return fmt.Errorf("postgres: delete evidence by title: %w", err)
	}
	return nil
}

// StoredRevisions returns the stored revision id of each requested page the
// source has chunks for; pages with no stored chunks are absent from the map.
// The delta sync diffs these against the revisions RecentChanges reports to skip
// pages already current. Revision id lives in the metadata jsonb. The delta sync
// is Wikipedia-specific and works in numeric page ids, so this maps them to the
// generic text external_id at the store boundary rather than leaking text ids
// into the delta logic.
func (s *Store) StoredRevisions(ctx context.Context, source string, pageIDs []int64) (map[int64]int64, error) {
	if len(pageIDs) == 0 {
		return map[int64]int64{}, nil
	}
	externalIDs := make([]string, len(pageIDs))
	for i, id := range pageIDs {
		externalIDs[i] = strconv.FormatInt(id, 10)
	}
	rows, err := s.queries.StoredEvidenceRevisions(ctx, db.StoredEvidenceRevisionsParams{
		Source:      source,
		ExternalIds: externalIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: stored evidence revisions: %w", err)
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		pageID, err := strconv.ParseInt(r.ExternalID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("postgres: stored evidence revisions: external id %q: %w", r.ExternalID, err)
		}
		out[pageID] = r.RevisionID
	}
	return out, nil
}

// CountDocuments returns the number of distinct documents in the live
// encyclopedic corpora, the denominator for the delta sync's
// bulk-recommendation guard. The statistical sources share this table but are
// excluded so their rows never skew the change-fraction guard.
func (s *Store) CountDocuments(ctx context.Context) (int64, error) {
	n, err := s.queries.CountEvidenceDocuments(ctx, domain.StatCorpora())
	if err != nil {
		return 0, fmt.Errorf("postgres: count evidence documents: %w", err)
	}
	return n, nil
}

// SetChunkEmbeddings writes each chunk's embedding into the live table in one
// batch. The delta sync embeds changed chunks in place; the HNSW index absorbs
// the updates incrementally, so no staging swap is needed at delta volume.
// Every chunk must carry a full-dimension embedding.
func (s *Store) SetChunkEmbeddings(ctx context.Context, chunks []domain.EvidenceChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	params := make([]db.SetEvidenceChunkEmbeddingParams, len(chunks))
	for i, c := range chunks {
		if len(c.Embedding) != domain.EmbeddingDim {
			return fmt.Errorf("postgres: set embedding %s/%s#%d: embedding has %d dims, want %d", c.Source, c.ExternalID, c.ChunkIndex, len(c.Embedding), domain.EmbeddingDim)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: set embedding %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
		}
		hv := pgvector.NewHalfVector(c.Embedding)
		params[i] = db.SetEvidenceChunkEmbeddingParams{
			Embedding:  &hv,
			Source:     c.Source,
			ExternalID: c.ExternalID,
			ChunkIndex: int32(c.ChunkIndex),
		}
	}
	if err := firstBatchError(s.queries.SetEvidenceChunkEmbedding(ctx, params)); err != nil {
		return fmt.Errorf("postgres: set evidence chunk embeddings: %w", err)
	}
	return nil
}

// UpsertEmbeddedChunk inserts or replaces one evidence chunk together with its
// embedding in a single statement, so the row is never visible to search without
// a matching vector. The crawl worker calls it after embedding a self-contained
// message; the embedding is written as text-form ::halfvec by the generated
// query (a parametrized INSERT, never binary COPY). The embedding must be
// full-dimension and the kind valid.
func (s *Store) UpsertEmbeddedChunk(ctx context.Context, c domain.EvidenceChunk) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("postgres: upsert embedded chunk %s/%s: invalid kind %q", c.Source, c.ExternalID, c.Kind)
	}
	if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
		return fmt.Errorf("postgres: upsert embedded chunk %s/%s: chunk index %d out of range", c.Source, c.ExternalID, c.ChunkIndex)
	}
	if len(c.Embedding) != domain.EmbeddingDim {
		return fmt.Errorf("postgres: upsert embedded chunk %s/%s#%d: embedding has %d dims, want %d", c.Source, c.ExternalID, c.ChunkIndex, len(c.Embedding), domain.EmbeddingDim)
	}
	meta, err := marshalMetadata(c.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: upsert embedded chunk %s/%s: %w", c.Source, c.ExternalID, err)
	}
	hv := pgvector.NewHalfVector(c.Embedding)
	if err := s.queries.UpsertEmbeddedEvidenceChunk(ctx, db.UpsertEmbeddedEvidenceChunkParams{
		Source:     c.Source,
		ExternalID: c.ExternalID,
		ChunkIndex: int32(c.ChunkIndex),
		Title:      c.Title,
		Url:        c.URL,
		Content:    c.Content,
		Kind:       string(c.Kind),
		Metadata:   meta,
		Embedding:  &hv,
	}); err != nil {
		return fmt.Errorf("postgres: upsert embedded chunk %s/%s#%d: %w", c.Source, c.ExternalID, c.ChunkIndex, err)
	}
	return nil
}

// SetSyncState upserts the per-source ingestion checkpoint. A zero LastChangeTS
// stores NULL - no checkpoint to resume from yet.
func (s *Store) SetSyncState(ctx context.Context, st domain.EvidenceSyncState) error {
	params := db.UpsertEvidenceSyncStateParams{
		Source:      st.Source,
		DumpVersion: pgtype.Text{String: st.DumpVersion, Valid: st.DumpVersion != ""},
		LastChangeTs: pgtype.Timestamptz{
			Time:  st.LastChangeTS,
			Valid: !st.LastChangeTS.IsZero(),
		},
	}
	if err := s.queries.UpsertEvidenceSyncState(ctx, params); err != nil {
		return fmt.Errorf("postgres: set evidence sync state %q: %w", st.Source, err)
	}
	return nil
}

// GetSyncState loads the source checkpoint; ok is false when the source has
// never been synced.
func (s *Store) GetSyncState(ctx context.Context, source string) (domain.EvidenceSyncState, bool, error) {
	row, err := s.queries.GetEvidenceSyncState(ctx, source)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EvidenceSyncState{}, false, nil
	}
	if err != nil {
		return domain.EvidenceSyncState{}, false, fmt.Errorf("postgres: get evidence sync state %q: %w", source, err)
	}

	st := domain.EvidenceSyncState{Source: row.Source, DumpVersion: row.DumpVersion.String}
	if row.LastChangeTs.Valid {
		st.LastChangeTS = row.LastChangeTs.Time
	}
	return st, true, nil
}
