package postgres

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// vecEmbedder embeds each chunk to the orthogonal unit vector its content
// names ("v<n>" -> unitVec(n)), so nearest-neighbor assertions are exact, and
// counts how many texts it embedded so resume tests can prove work was skipped.
type vecEmbedder struct {
	mu       sync.Mutex
	embedded int
}

func (e *vecEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.embedded += len(texts)
	e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i, t := range texts {
		n, _ := strconv.Atoi(strings.TrimPrefix(t, "v"))
		out[i] = unitVec(n)
	}
	return out, nil
}

func (e *vecEmbedder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.embedded
}

func withEmbedding(c domain.WikiChunk, v []float32) domain.WikiChunk {
	c.Embedding = v
	return c
}

// testDumpVersion is the dump stamp the bulk-embed integration tests run under;
// the manual CreateStaging calls below use it so the discard guard sees a
// matching stamp and resumes instead of dropping the staging.
const testDumpVersion = "Mon, 01 Jun 2026 00:00:00 GMT"

func bulkConfig() wiki.Config {
	return wiki.Config{Corpus: "simplewiki", DumpVersion: testDumpVersion, BatchSize: 2, Concurrency: 2, MaintenanceWorkMem: "64MB", MaxParallelWorkers: 0}
}

// seedChunks claims the corpus and stores chunks with null embeddings, the
// state the bulk-embedding pipeline starts from.
func seedChunks(t *testing.T, store *Store, chunks []domain.WikiChunk) {
	t.Helper()
	ctx := t.Context()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
}

func TestUnembeddedChunksKeyset(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "a"),
		wikiChunk(1, 1, "b"),
		wikiChunk(2, 0, "c"),
	})

	all, err := store.UnembeddedChunks(ctx, domain.WikiCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedChunks: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d chunks, want 3", len(all))
	}
	if all[0].PageID != 1 || all[0].ChunkIndex != 0 || all[2].PageID != 2 {
		t.Errorf("not in keyset order: %+v", all)
	}

	rest, err := store.UnembeddedChunks(ctx, domain.WikiCursor{PageID: 1, ChunkIndex: 0}, 10)
	if err != nil {
		t.Fatalf("UnembeddedChunks after cursor: %v", err)
	}
	if len(rest) != 2 || rest[0].ChunkIndex != 1 {
		t.Errorf("after (1,0) got %+v, want chunks (1,1) and (2,0)", rest)
	}

	limited, err := store.UnembeddedChunks(ctx, domain.WikiCursor{}, 2)
	if err != nil {
		t.Fatalf("UnembeddedChunks limited: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("limit 2 returned %d", len(limited))
	}
}

func TestEstimateRemaining(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "abc"),
		wikiChunk(1, 1, "de"),
		wikiChunk(2, 0, "f"),
	})

	rem, err := store.EstimateRemaining(ctx, domain.WikiCursor{})
	if err != nil {
		t.Fatalf("EstimateRemaining: %v", err)
	}
	if rem.Pages != 2 || rem.Chunks != 3 || rem.Chars != 6 {
		t.Errorf("estimate = %+v, want pages 2 chunks 3 chars 6", rem)
	}

	after, err := store.EstimateRemaining(ctx, domain.WikiCursor{PageID: 1, ChunkIndex: 1})
	if err != nil {
		t.Fatalf("EstimateRemaining after cursor: %v", err)
	}
	if after.Pages != 1 || after.Chunks != 1 || after.Chars != 1 {
		t.Errorf("estimate after (1,1) = %+v, want pages 1 chunks 1 chars 1", after)
	}
}

func TestEmbedWatermarkTracksStaging(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	cur, err := store.EmbedWatermark(ctx)
	if err != nil {
		t.Fatalf("EmbedWatermark (no staging): %v", err)
	}
	if cur != (domain.WikiCursor{}) {
		t.Errorf("watermark = %+v, want zero before staging exists", cur)
	}

	if err := store.CreateStaging(ctx, testDumpVersion); err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}
	cur, err = store.EmbedWatermark(ctx)
	if err != nil {
		t.Fatalf("EmbedWatermark (empty staging): %v", err)
	}
	if cur != (domain.WikiCursor{}) {
		t.Errorf("watermark = %+v, want zero for empty staging", cur)
	}

	if err := store.CopyStagingChunks(ctx, []domain.WikiChunk{
		withEmbedding(wikiChunk(2, 1, "v0"), unitVec(0)),
		withEmbedding(wikiChunk(2, 3, "v1"), unitVec(1)),
	}); err != nil {
		t.Fatalf("CopyStagingChunks: %v", err)
	}
	cur, err = store.EmbedWatermark(ctx)
	if err != nil {
		t.Fatalf("EmbedWatermark (loaded staging): %v", err)
	}
	if cur.PageID != 2 || cur.ChunkIndex != 3 {
		t.Errorf("watermark = %+v, want (2,3)", cur)
	}
}

func TestBulkEmbedEndToEnd(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"),
		wikiChunk(2, 0, "v1"),
		wikiChunk(3, 0, "v2"),
	})

	emb := &vecEmbedder{}
	stats, err := wiki.RunBulkEmbed(ctx, slog.New(slog.DiscardHandler), store, emb, bulkConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	if stats.Embedded != 3 {
		t.Errorf("embedded = %d, want 3", stats.Embedded)
	}

	if stagingExistsT(t, store) {
		t.Error("staging table was not dropped after the swap")
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks WHERE embedding IS NULL"); n != 0 {
		t.Errorf("%d chunks left unembedded after a complete run", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks"); n != 3 {
		t.Errorf("live corpus has %d chunks after swap, want 3", n)
	}

	var nearest int64
	if err := store.pool.QueryRow(
		ctx,
		"SELECT page_id FROM wiki_chunks ORDER BY embedding <=> $1 LIMIT 1",
		pgvector.NewHalfVector(unitVec(1)),
	).Scan(&nearest); err != nil {
		t.Fatalf("similarity query: %v", err)
	}
	if nearest != 2 {
		t.Errorf("nearest to unitVec(1) = page %d, want 2", nearest)
	}

	if _, ok, err := store.GetSyncState(ctx, "simplewiki"); err != nil || !ok {
		t.Errorf("sync state after embed: ok=%v err=%v", ok, err)
	}
}

func TestBulkEmbedRerunAfterCompletionIsNoop(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"),
		wikiChunk(2, 0, "v1"),
	})

	first := &vecEmbedder{}
	if _, err := wiki.RunBulkEmbed(ctx, slog.New(slog.DiscardHandler), store, first, bulkConfig()); err != nil {
		t.Fatalf("RunBulkEmbed (first): %v", err)
	}
	if first.count() != 2 {
		t.Fatalf("first run embedded %d, want 2", first.count())
	}

	// Re-running on the already-embedded corpus must embed nothing (no double
	// billing) and leave no staging table behind.
	second := &vecEmbedder{}
	stats, err := wiki.RunBulkEmbed(ctx, slog.New(slog.DiscardHandler), store, second, bulkConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed (rerun): %v", err)
	}
	if second.count() != 0 {
		t.Errorf("rerun embedded %d texts, want 0", second.count())
	}
	if stats.Embedded != 0 {
		t.Errorf("rerun stats.Embedded = %d, want 0", stats.Embedded)
	}
	if stagingExistsT(t, store) {
		t.Error("rerun left a staging table behind")
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks"); n != 2 {
		t.Errorf("corpus has %d chunks after rerun, want 2", n)
	}
}

func TestBulkEmbedResumeEmbedsOnlyRemaining(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"),
		wikiChunk(2, 0, "v1"),
		wikiChunk(3, 0, "v2"),
	})

	// Simulate an interrupted prior run that already staged page 1.
	if err := store.CreateStaging(ctx, testDumpVersion); err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}
	if err := store.CopyStagingChunks(ctx, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v0"), unitVec(0)),
	}); err != nil {
		t.Fatalf("CopyStagingChunks: %v", err)
	}

	emb := &vecEmbedder{}
	stats, err := wiki.RunBulkEmbed(ctx, slog.New(slog.DiscardHandler), store, emb, bulkConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed (resume): %v", err)
	}
	if emb.count() != 2 {
		t.Errorf("embedded %d texts, want 2 (page 1 was already staged)", emb.count())
	}
	if stats.Embedded != 2 {
		t.Errorf("stats.Embedded = %d, want 2", stats.Embedded)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks WHERE embedding IS NULL"); n != 0 {
		t.Errorf("%d chunks unembedded after resume completed", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks"); n != 3 {
		t.Errorf("live corpus has %d chunks, want 3", n)
	}
}

func TestFinalizeStagingRefusesPartialCorpus(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"),
		wikiChunk(2, 0, "v1"),
		wikiChunk(3, 0, "v2"),
	})

	if err := store.CreateStaging(ctx, testDumpVersion); err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}
	// Only one of three chunks loaded: finalizing would swap a partial corpus.
	if err := store.CopyStagingChunks(ctx, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v0"), unitVec(0)),
	}); err != nil {
		t.Fatalf("CopyStagingChunks: %v", err)
	}

	if err := store.FinalizeStaging(ctx, "simplewiki", "64MB", 0); err == nil {
		t.Fatal("FinalizeStaging swapped a partial corpus, want error")
	}
	// The live corpus is untouched: still three unembedded chunks, no swap.
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks WHERE embedding IS NULL"); n != 3 {
		t.Errorf("live corpus changed: %d null embeddings, want 3", n)
	}
	if !stagingExistsT(t, store) {
		t.Error("staging was dropped despite the refused swap")
	}
}

func TestDiscardStagingIfStaleDropsOnDumpChange(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.CreateStaging(ctx, "dump-v1"); err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}

	// Same dump: staging is a valid resume target and is kept.
	dropped, err := store.DiscardStagingIfStale(ctx, "dump-v1")
	if err != nil {
		t.Fatalf("DiscardStagingIfStale (same dump): %v", err)
	}
	if dropped {
		t.Error("staging from the same dump was dropped, want kept")
	}
	if !stagingExistsT(t, store) {
		t.Error("staging table missing after a same-dump check")
	}

	// Newer dump: the stamp no longer matches, so the stale staging is dropped.
	dropped, err = store.DiscardStagingIfStale(ctx, "dump-v2")
	if err != nil {
		t.Fatalf("DiscardStagingIfStale (new dump): %v", err)
	}
	if !dropped {
		t.Error("staging from an older dump was kept, want dropped")
	}
	if stagingExistsT(t, store) {
		t.Error("stale staging table survived the discard")
	}

	// Absent staging is a no-op.
	dropped, err = store.DiscardStagingIfStale(ctx, "dump-v2")
	if err != nil {
		t.Fatalf("DiscardStagingIfStale (absent): %v", err)
	}
	if dropped {
		t.Error("discard reported a drop with no staging table")
	}
}

func TestDiscardStagingIfStaleHandlesUnversionedDump(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// A mirror that reports no Last-Modified yields an empty dump version, which
	// Postgres stores as a NULL comment. The empty stamp must still compare equal
	// to a later empty version so the staging is kept (the pre-stamp behavior),
	// and must be dropped once a real version appears.
	if err := store.CreateStaging(ctx, ""); err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}
	dropped, err := store.DiscardStagingIfStale(ctx, "")
	if err != nil {
		t.Fatalf("DiscardStagingIfStale (empty vs empty): %v", err)
	}
	if dropped {
		t.Error("unversioned staging dropped against an unversioned dump, want kept")
	}
	if !stagingExistsT(t, store) {
		t.Error("staging table missing after an unversioned same-dump check")
	}

	dropped, err = store.DiscardStagingIfStale(ctx, "Mon, 01 Jun 2026 00:00:00 GMT")
	if err != nil {
		t.Fatalf("DiscardStagingIfStale (empty vs versioned): %v", err)
	}
	if !dropped {
		t.Error("unversioned staging kept when a versioned dump arrived, want dropped")
	}
	if stagingExistsT(t, store) {
		t.Error("stale unversioned staging survived the discard")
	}
}

func TestBulkEmbedReembedsAfterDumpChange(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// The live state after a newer dump (V2) re-ingested the corpus: page 1
	// sorts below the watermark a prior V1 run left, so a keyset resume would
	// never embed it. Without the discard guard this is the production failure -
	// staging stays one chunk short of live and the swap is refused forever.
	seedChunks(t, store, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"),
		wikiChunk(2, 0, "v1"),
		wikiChunk(3, 0, "v2"),
	})

	// A surviving V1 staging table that embedded only page 2 (watermark (2,0)).
	if err := store.CreateStaging(ctx, "Sun, 25 May 2026 00:00:00 GMT"); err != nil {
		t.Fatalf("CreateStaging: %v", err)
	}
	if err := store.CopyStagingChunks(ctx, []domain.WikiChunk{
		withEmbedding(wikiChunk(2, 0, "v1"), unitVec(1)),
	}); err != nil {
		t.Fatalf("CopyStagingChunks: %v", err)
	}

	// Running under the new dump version discards the stale staging and re-embeds
	// the whole corpus, so the swap completes instead of refusing a partial corpus.
	emb := &vecEmbedder{}
	stats, err := wiki.RunBulkEmbed(ctx, slog.New(slog.DiscardHandler), store, emb, bulkConfig())
	if err != nil {
		t.Fatalf("RunBulkEmbed: %v", err)
	}
	if stats.Embedded != 3 {
		t.Errorf("embedded = %d, want 3 (full corpus re-embedded after discard)", stats.Embedded)
	}
	if emb.count() != 3 {
		t.Errorf("embedder saw %d texts, want 3", emb.count())
	}
	if stagingExistsT(t, store) {
		t.Error("staging table was not dropped after the swap")
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks WHERE embedding IS NULL"); n != 0 {
		t.Errorf("%d chunks left unembedded after re-embed", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks"); n != 3 {
		t.Errorf("live corpus has %d chunks after swap, want 3", n)
	}
}

func stagingExistsT(t *testing.T, store *Store) bool {
	t.Helper()
	var exists bool
	if err := store.pool.QueryRow(t.Context(),
		"SELECT to_regclass('wiki_chunks_staging') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("check staging existence: %v", err)
	}
	return exists
}

func scalarInt(t *testing.T, store *Store, query string) int {
	t.Helper()
	var n int
	if err := store.pool.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
