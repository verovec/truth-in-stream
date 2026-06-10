package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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
		params[i] = db.UpsertWikiChunkParams{
			PageID:     c.PageID,
			ChunkIndex: int32(c.ChunkIndex),
			Title:      c.Title,
			Url:        c.URL,
			RevisionID: c.RevisionID,
			Corpus:     c.Corpus,
			Content:    c.Content,
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

// DeletePage removes every chunk of one article; the delta sync uses it when
// a page is deleted or turns into a redirect.
func (s *Store) DeletePage(ctx context.Context, pageID int64) error {
	if err := s.queries.DeleteWikiPage(ctx, pageID); err != nil {
		return fmt.Errorf("postgres: delete wiki page %d: %w", pageID, err)
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
