package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

// wikiCorpusLockKey is the advisory lock key serializing corpus claims
// ("wikicorp" in hex).
const wikiCorpusLockKey = int64(0x77696b69636f7270)

// EnsureCorpus claims the store for one corpus before ingestion starts. The
// wiki_chunks primary key carries no corpus (one corpus per environment), so
// mixing two would silently interleave colliding page ids; finding a foreign
// corpus in wiki_sync_state is therefore fatal. Check and claim run in one
// transaction under an advisory lock, so two concurrent syncs cannot both
// claim different corpora.
func (s *Store) EnsureCorpus(ctx context.Context, corpus string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := s.queries.WithTx(tx)
		if err := q.AcquireWikiCorpusLock(ctx, wikiCorpusLockKey); err != nil {
			return fmt.Errorf("acquire corpus lock: %w", err)
		}
		other, err := q.GetOtherWikiCorpus(ctx, corpus)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("check corpus: %w", err)
		default:
			return fmt.Errorf("store already holds corpus %q; wiki_chunks is single-corpus, use a fresh database for %q", other, corpus)
		}
		if err := q.ClaimWikiCorpus(ctx, corpus); err != nil {
			return fmt.Errorf("claim corpus: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres: ensure wiki corpus %q: %w", corpus, err)
	}
	return nil
}

// SearchWiki returns the topK embedded wiki chunks closest to query by cosine
// distance, nearest first, as supporting evidence. It mirrors Store.Search over
// the claims corpus; unembedded chunks are excluded by the query.
func (s *Store) SearchWiki(ctx context.Context, query []float32, topK int) ([]domain.WikiEvidence, error) {
	if topK <= 0 || topK > math.MaxInt32 {
		return nil, fmt.Errorf("postgres: search wiki: topK %d out of range", topK)
	}
	if len(query) != domain.EmbeddingDim {
		return nil, fmt.Errorf("postgres: search wiki: query has %d dims, want %d", len(query), domain.EmbeddingDim)
	}

	vec := pgvector.NewHalfVector(query)
	rows, err := s.queries.SearchWikiChunks(ctx, db.SearchWikiChunksParams{
		QueryEmbedding: &vec,
		ResultLimit:    int32(topK),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: search wiki: %w", err)
	}

	evidence := make([]domain.WikiEvidence, 0, len(rows))
	for _, r := range rows {
		evidence = append(evidence, domain.WikiEvidence{
			PageID:     r.PageID,
			ChunkIndex: int(r.ChunkIndex),
			Title:      r.Title,
			URL:        r.Url,
			Content:    r.Content,
			Section:    r.Section,
			Kind:       domain.WikiChunkKind(r.Kind),
			// Cosine distance is in [0,2]; the float32 narrowing matches
			// domain.WikiEvidence and is plenty precise for ranking.
			Distance: float32(r.Distance),
		})
	}
	return evidence, nil
}

// UpsertChunks inserts or replaces wiki chunks by (page_id, chunk_index) in a
// single batch. Embeddings are never written here; the generated upsert keeps
// an existing embedding only when the chunk content is unchanged.
func (s *Store) UpsertChunks(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}

	params := make([]db.UpsertWikiChunkParams, len(chunks))
	for i, c := range chunks {
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: upsert wiki chunk page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		if !c.Kind.Valid() {
			return fmt.Errorf("postgres: upsert wiki chunk page %d: invalid kind %q", c.PageID, c.Kind)
		}
		params[i] = db.UpsertWikiChunkParams{
			PageID:     c.PageID,
			ChunkIndex: int32(c.ChunkIndex),
			Title:      c.Title,
			Url:        c.URL,
			RevisionID: c.RevisionID,
			Corpus:     c.Corpus,
			Content:    c.Content,
			Section:    c.Section,
			Kind:       string(c.Kind),
		}
	}

	if err := firstBatchError(s.queries.UpsertWikiChunk(ctx, params)); err != nil {
		return fmt.Errorf("postgres: upsert wiki chunks: %w", err)
	}
	return nil
}

// TrimPages removes the stale tail of each page's chunks in a single batch.
func (s *Store) TrimPages(ctx context.Context, trims []domain.WikiTrim) error {
	if len(trims) == 0 {
		return nil
	}

	params := make([]db.TrimWikiPageChunksParams, len(trims))
	for i, tr := range trims {
		if tr.FromIndex < 0 || tr.FromIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: trim wiki page %d: from index %d out of range", tr.PageID, tr.FromIndex)
		}
		params[i] = db.TrimWikiPageChunksParams{
			PageID:     tr.PageID,
			ChunkIndex: int32(tr.FromIndex),
		}
	}

	if err := firstBatchError(s.queries.TrimWikiPageChunks(ctx, params)); err != nil {
		return fmt.Errorf("postgres: trim wiki chunks: %w", err)
	}
	return nil
}

// DeletePagesByTitle removes every chunk of each named page in one statement.
// The delta sync uses it for hard deletions, which RecentChanges reports by
// title (page id 0) rather than by page id.
func (s *Store) DeletePagesByTitle(ctx context.Context, titles []string) error {
	if len(titles) == 0 {
		return nil
	}
	if err := s.queries.DeleteWikiPagesByTitle(ctx, titles); err != nil {
		return fmt.Errorf("postgres: delete wiki pages by title: %w", err)
	}
	return nil
}

// StoredRevisions returns the stored revision id of each requested page the
// corpus has chunks for; pages with no stored chunks are absent from the map.
// The delta sync diffs these against the revisions RecentChanges reports to skip
// pages already current.
func (s *Store) StoredRevisions(ctx context.Context, pageIDs []int64) (map[int64]int64, error) {
	if len(pageIDs) == 0 {
		return map[int64]int64{}, nil
	}
	rows, err := s.queries.StoredWikiRevisions(ctx, pageIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: stored wiki revisions: %w", err)
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.PageID] = r.RevisionID
	}
	return out, nil
}

// CountPages returns the number of distinct pages in the live corpus, the
// denominator for the delta sync's bulk-recommendation guard.
func (s *Store) CountPages(ctx context.Context) (int64, error) {
	n, err := s.queries.CountWikiPages(ctx)
	if err != nil {
		return 0, fmt.Errorf("postgres: count wiki pages: %w", err)
	}
	return n, nil
}

// SetChunkEmbeddings writes each chunk's embedding into the live table in one
// batch. The delta sync embeds changed chunks in place; the HNSW index absorbs
// the updates incrementally, so no staging swap is needed at delta volume.
// Every chunk must carry a full-dimension embedding.
func (s *Store) SetChunkEmbeddings(ctx context.Context, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	params := make([]db.SetWikiChunkEmbeddingParams, len(chunks))
	for i, c := range chunks {
		if len(c.Embedding) != domain.EmbeddingDim {
			return fmt.Errorf("postgres: set embedding page %d chunk %d: embedding has %d dims, want %d", c.PageID, c.ChunkIndex, len(c.Embedding), domain.EmbeddingDim)
		}
		if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
			return fmt.Errorf("postgres: set embedding page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
		}
		hv := pgvector.NewHalfVector(c.Embedding)
		params[i] = db.SetWikiChunkEmbeddingParams{
			Embedding:  &hv,
			PageID:     c.PageID,
			ChunkIndex: int32(c.ChunkIndex),
		}
	}
	if err := firstBatchError(s.queries.SetWikiChunkEmbedding(ctx, params)); err != nil {
		return fmt.Errorf("postgres: set wiki chunk embeddings: %w", err)
	}
	return nil
}

// UpsertEmbeddedChunk inserts or replaces one wiki chunk together with its
// embedding in a single statement, so the row is never visible to search without
// a matching vector. The crawl worker calls it after embedding a self-contained
// message; the embedding is written as text-form ::halfvec by the generated
// query (a parametrized INSERT, never binary COPY). The embedding must be
// full-dimension and the kind valid.
func (s *Store) UpsertEmbeddedChunk(ctx context.Context, c domain.WikiChunk) error {
	if !c.Kind.Valid() {
		return fmt.Errorf("postgres: upsert embedded chunk page %d: invalid kind %q", c.PageID, c.Kind)
	}
	if c.ChunkIndex < 0 || c.ChunkIndex > math.MaxInt32 {
		return fmt.Errorf("postgres: upsert embedded chunk page %d: chunk index %d out of range", c.PageID, c.ChunkIndex)
	}
	if len(c.Embedding) != domain.EmbeddingDim {
		return fmt.Errorf("postgres: upsert embedded chunk page %d chunk %d: embedding has %d dims, want %d", c.PageID, c.ChunkIndex, len(c.Embedding), domain.EmbeddingDim)
	}
	hv := pgvector.NewHalfVector(c.Embedding)
	if err := s.queries.UpsertEmbeddedChunk(ctx, db.UpsertEmbeddedChunkParams{
		PageID:     c.PageID,
		ChunkIndex: int32(c.ChunkIndex),
		Title:      c.Title,
		Url:        c.URL,
		RevisionID: c.RevisionID,
		Corpus:     c.Corpus,
		Content:    c.Content,
		Section:    c.Section,
		Kind:       string(c.Kind),
		Embedding:  &hv,
	}); err != nil {
		return fmt.Errorf("postgres: upsert embedded chunk page %d chunk %d: %w", c.PageID, c.ChunkIndex, err)
	}
	return nil
}

// SetSyncState upserts the per-corpus ingestion checkpoint. A zero
// LastChangeTS stores NULL - no checkpoint to resume from yet.
func (s *Store) SetSyncState(ctx context.Context, st domain.WikiSyncState) error {
	params := db.UpsertWikiSyncStateParams{
		Corpus:      st.Corpus,
		DumpVersion: pgtype.Text{String: st.DumpVersion, Valid: st.DumpVersion != ""},
		LastChangeTs: pgtype.Timestamptz{
			Time:  st.LastChangeTS,
			Valid: !st.LastChangeTS.IsZero(),
		},
	}
	if err := s.queries.UpsertWikiSyncState(ctx, params); err != nil {
		return fmt.Errorf("postgres: set wiki sync state %q: %w", st.Corpus, err)
	}
	return nil
}

// GetSyncState loads the corpus checkpoint; ok is false when the corpus has
// never been synced.
func (s *Store) GetSyncState(ctx context.Context, corpus string) (domain.WikiSyncState, bool, error) {
	row, err := s.queries.GetWikiSyncState(ctx, corpus)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WikiSyncState{}, false, nil
	}
	if err != nil {
		return domain.WikiSyncState{}, false, fmt.Errorf("postgres: get wiki sync state %q: %w", corpus, err)
	}

	st := domain.WikiSyncState{Corpus: row.Corpus, DumpVersion: row.DumpVersion.String}
	if row.LastChangeTs.Valid {
		st.LastChangeTS = row.LastChangeTs.Time
	}
	return st, true, nil
}
