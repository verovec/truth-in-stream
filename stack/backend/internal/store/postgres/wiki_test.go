package postgres

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

func wikiChunk(pageID int64, idx int, content string) domain.WikiChunk {
	return domain.WikiChunk{
		PageID:     pageID,
		ChunkIndex: idx,
		Title:      "Paris",
		URL:        "https://simple.wikipedia.org/wiki/Paris",
		RevisionID: 100,
		Corpus:     "simplewiki",
		Content:    content,
	}
}

func TestUpsertChunksRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		wikiChunk(1, 0, "Paris\n\nParis is the capital of France."),
		wikiChunk(1, 1, "Paris\n\nIt sits on the Seine."),
		wikiChunk(2, 0, "Lyon\n\nLyon is a city in France."),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	got, err := store.queries.GetWikiChunk(ctx, db.GetWikiChunkParams{PageID: 1, ChunkIndex: 1})
	if err != nil {
		t.Fatalf("GetWikiChunk: %v", err)
	}
	want := db.GetWikiChunkRow{
		PageID:          1,
		ChunkIndex:      1,
		Title:           "Paris",
		Url:             "https://simple.wikipedia.org/wiki/Paris",
		RevisionID:      100,
		Corpus:          "simplewiki",
		Content:         "Paris\n\nIt sits on the Seine.",
		EmbeddingIsNull: true,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("stored chunk mismatch (-want +got):\n%s", diff)
	}
}

func TestUpsertChunksIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		wikiChunk(1, 0, "Paris\n\nFirst."),
		wikiChunk(1, 1, "Paris\n\nSecond."),
	}
	for range 2 {
		if err := store.UpsertChunks(ctx, chunks); err != nil {
			t.Fatalf("UpsertChunks: %v", err)
		}
	}

	n, err := store.queries.CountWikiChunksForPage(ctx, 1)
	if err != nil {
		t.Fatalf("CountWikiChunksForPage: %v", err)
	}
	if n != 2 {
		t.Errorf("page 1 has %d chunks after re-run, want 2", n)
	}
}

func TestUpsertChunksEmbeddingInvalidation(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.UpsertChunks(ctx, []domain.WikiChunk{wikiChunk(1, 0, "Paris\n\nOriginal.")}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	emb := pgvector.NewHalfVector(unitVec(0))
	if _, err := store.pool.Exec(ctx, "UPDATE wiki_chunks SET embedding = $1 WHERE page_id = 1 AND chunk_index = 0", emb); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}

	// Same content: the embedding must survive the upsert.
	if err := store.UpsertChunks(ctx, []domain.WikiChunk{wikiChunk(1, 0, "Paris\n\nOriginal.")}); err != nil {
		t.Fatalf("UpsertChunks (same content): %v", err)
	}
	row, err := store.queries.GetWikiChunk(ctx, db.GetWikiChunkParams{PageID: 1, ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetWikiChunk: %v", err)
	}
	if row.EmbeddingIsNull {
		t.Error("unchanged content dropped the embedding")
	}

	// Changed content: the stale embedding must be invalidated.
	if err := store.UpsertChunks(ctx, []domain.WikiChunk{wikiChunk(1, 0, "Paris\n\nRewritten.")}); err != nil {
		t.Fatalf("UpsertChunks (changed content): %v", err)
	}
	row, err = store.queries.GetWikiChunk(ctx, db.GetWikiChunkParams{PageID: 1, ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetWikiChunk: %v", err)
	}
	if !row.EmbeddingIsNull {
		t.Error("changed content kept a stale embedding")
	}
}

func TestDeletePage(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		wikiChunk(1, 0, "Paris\n\nOne."),
		wikiChunk(1, 1, "Paris\n\nTwo."),
		wikiChunk(2, 0, "Lyon\n\nOther page."),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	if err := store.DeletePage(ctx, 1); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	n, err := store.queries.CountWikiChunksForPage(ctx, 1)
	if err != nil {
		t.Fatalf("CountWikiChunksForPage(1): %v", err)
	}
	if n != 0 {
		t.Errorf("page 1 has %d chunks after delete, want 0", n)
	}
	n, err = store.queries.CountWikiChunksForPage(ctx, 2)
	if err != nil {
		t.Fatalf("CountWikiChunksForPage(2): %v", err)
	}
	if n != 1 {
		t.Errorf("page 2 has %d chunks after deleting page 1, want 1", n)
	}
}

func TestTrimPages(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		wikiChunk(1, 0, "Paris\n\nOne."),
		wikiChunk(1, 1, "Paris\n\nTwo."),
		wikiChunk(1, 2, "Paris\n\nThree."),
		wikiChunk(2, 0, "Lyon\n\nOther page."),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	trims := []domain.WikiTrim{
		{PageID: 1, FromIndex: 1},
		{PageID: 2, FromIndex: 0},
	}
	if err := store.TrimPages(ctx, trims); err != nil {
		t.Fatalf("TrimPages: %v", err)
	}

	n, err := store.queries.CountWikiChunksForPage(ctx, 1)
	if err != nil {
		t.Fatalf("CountWikiChunksForPage(1): %v", err)
	}
	if n != 1 {
		t.Errorf("page 1 has %d chunks after trim from 1, want 1", n)
	}
	n, err = store.queries.CountWikiChunksForPage(ctx, 2)
	if err != nil {
		t.Fatalf("CountWikiChunksForPage(2): %v", err)
	}
	if n != 0 {
		t.Errorf("page 2 has %d chunks after trim from 0, want 0", n)
	}
}

func TestEnsureCorpus(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus (fresh store): %v", err)
	}
	// Idempotent for the same corpus, even before any checkpoint exists.
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus (same corpus): %v", err)
	}
	// A different corpus is refused: page ids would collide.
	if err := store.EnsureCorpus(ctx, "enwiki"); err == nil {
		t.Fatal("EnsureCorpus accepted a second corpus, want error")
	}

	// The claim must not have created a fake checkpoint.
	st, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !ok {
		t.Fatal("corpus claim row missing")
	}
	if !st.LastChangeTS.IsZero() || st.DumpVersion != "" {
		t.Errorf("corpus claim invented a checkpoint: %+v", st)
	}
}

func TestSyncStateRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	_, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil {
		t.Fatalf("GetSyncState (empty): %v", err)
	}
	if ok {
		t.Fatal("GetSyncState reported a checkpoint before any sync")
	}

	ts := time.Date(2026, 6, 1, 3, 14, 0, 0, time.UTC)
	st := domain.WikiSyncState{Corpus: "simplewiki", LastChangeTS: ts, DumpVersion: "Mon, 01 Jun 2026 03:14:00 GMT"}
	if err := store.SetSyncState(ctx, st); err != nil {
		t.Fatalf("SetSyncState: %v", err)
	}

	got, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !ok {
		t.Fatal("GetSyncState found nothing after SetSyncState")
	}
	if diff := cmp.Diff(st, got); diff != "" {
		t.Errorf("sync state mismatch (-want +got):\n%s", diff)
	}

	// Re-checkpoint replaces the row, never duplicates it.
	st.DumpVersion = "Mon, 08 Jun 2026 03:14:00 GMT"
	st.LastChangeTS = ts.AddDate(0, 0, 7)
	if err := store.SetSyncState(ctx, st); err != nil {
		t.Fatalf("SetSyncState (update): %v", err)
	}
	got, ok, err = store.GetSyncState(ctx, "simplewiki")
	if err != nil || !ok {
		t.Fatalf("GetSyncState (after update): ok=%v err=%v", ok, err)
	}
	if diff := cmp.Diff(st, got); diff != "" {
		t.Errorf("updated sync state mismatch (-want +got):\n%s", diff)
	}
}

func TestSyncStateZeroTimeStoresNull(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	st := domain.WikiSyncState{Corpus: "simplewiki", DumpVersion: "unknown"}
	if err := store.SetSyncState(ctx, st); err != nil {
		t.Fatalf("SetSyncState: %v", err)
	}

	got, ok, err := store.GetSyncState(ctx, "simplewiki")
	if err != nil || !ok {
		t.Fatalf("GetSyncState: ok=%v err=%v", ok, err)
	}
	if !got.LastChangeTS.IsZero() {
		t.Errorf("LastChangeTS = %v, want zero for a NULL checkpoint", got.LastChangeTS)
	}
}

func TestEnsureCorpusConcurrentClaims(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// Two concurrent syncs claiming different corpora on a fresh store: the
	// advisory lock serializes them, so exactly one wins.
	errs := make(chan error, 2)
	for _, corpus := range []string{"simplewiki", "enwiki"} {
		go func() { errs <- store.EnsureCorpus(ctx, corpus) }()
	}

	failures := 0
	for range 2 {
		if err := <-errs; err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Errorf("%d of 2 concurrent foreign-corpus claims failed, want exactly 1", failures)
	}
}
