package postgres

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// testDumpVersion is the dump version the bulk-embed integration tests run
// under.
const testDumpVersion = "Mon, 01 Jun 2026 00:00:00 GMT"

func bulkConfig() wiki.Config {
	return wiki.Config{Corpus: "simplewiki", DumpVersion: testDumpVersion, BatchSize: 2, Concurrency: 2, MaintenanceWorkMem: "64MB", MaxParallelWorkers: 0}
}

// seedChunks claims the corpus and stores chunks in the live table; chunks
// carrying embeddings also have those written, so the live corpus is searchable,
// mirroring the state after a completed swap.
func seedChunks(t *testing.T, store *Store, chunks []domain.WikiChunk) {
	t.Helper()
	ctx := t.Context()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	var embedded []domain.WikiChunk
	for _, c := range chunks {
		if c.Embedding != nil {
			embedded = append(embedded, c)
		}
	}
	if len(embedded) > 0 {
		if err := store.SetChunkEmbeddings(ctx, embedded); err != nil {
			t.Fatalf("SetChunkEmbeddings: %v", err)
		}
	}
}

// stageChunks resets staging for version and loads chunks with NULL embeddings,
// the state ingest leaves for the embed run.
func stageChunks(t *testing.T, store *Store, version string, chunks []domain.WikiChunk) {
	t.Helper()
	ctx := t.Context()
	if err := store.ResetStaging(ctx, version); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	if err := store.UpsertStagingChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertStagingChunks: %v", err)
	}
}

func TestStagingPlan(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const v1, v2 = "v1", "v2"

	// No staging, no live corpus -> build.
	if p, err := store.StagingPlan(ctx, v1); err != nil || p != wiki.PlanBuild {
		t.Fatalf("empty: got %v, %v; want PlanBuild", p, err)
	}

	// ready:v1 staging -> resume embed for v1, rebuild for v2.
	if err := store.ResetStaging(ctx, v1); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	if err := store.MarkStagingReady(ctx, v1); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}
	if p, err := store.StagingPlan(ctx, v1); err != nil || p != wiki.PlanResumeEmbed {
		t.Fatalf("ready:v1 @ v1: got %v, %v; want PlanResumeEmbed", p, err)
	}
	if p, err := store.StagingPlan(ctx, v2); err != nil || p != wiki.PlanBuild {
		t.Fatalf("ready:v1 @ v2: got %v, %v; want PlanBuild", p, err)
	}

	// building:v1 (interrupted build) -> rebuild even for v1.
	if err := store.ResetStaging(ctx, v1); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	if p, err := store.StagingPlan(ctx, v1); err != nil || p != wiki.PlanBuild {
		t.Fatalf("building:v1 @ v1: got %v, %v; want PlanBuild", p, err)
	}
}

func TestStagingPlanAlreadyCurrent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const v1 = "v1"
	// Live fully embedded and checkpointed at v1, no staging -> already current.
	seedChunks(t, store, []domain.WikiChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	if err := store.SetSyncState(ctx, domain.WikiSyncState{Corpus: "simplewiki", DumpVersion: v1}); err != nil {
		t.Fatalf("SetSyncState: %v", err)
	}
	if p, err := store.StagingPlan(ctx, v1); err != nil || p != wiki.PlanAlreadyCurrent {
		t.Fatalf("got %v, %v; want PlanAlreadyCurrent", p, err)
	}
	// A different version is not current.
	if p, err := store.StagingPlan(ctx, "v2"); err != nil || p != wiki.PlanBuild {
		t.Fatalf("got %v, %v; want PlanBuild", p, err)
	}
}

func TestStagingBuild(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	stageChunks(t, store, "v1", []domain.WikiChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(1, 1, "v2"), wikiChunk(2, 0, "v3"),
	})

	rem, err := store.StagingRemaining(ctx)
	if err != nil {
		t.Fatalf("StagingRemaining: %v", err)
	}
	if rem.Chunks != 3 || rem.Pages != 2 {
		t.Fatalf("remaining = %+v; want 3 chunks / 2 pages", rem)
	}
	if exists, phase, v, _ := store.readStaging(ctx); !exists || phase != stampBuilding || v != "v1" {
		t.Fatalf("stamp = %v %q %q; want building v1", exists, phase, v)
	}
	if err := store.MarkStagingReady(ctx, "v1"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}
	if _, phase, _, _ := store.readStaging(ctx); phase != stampReady {
		t.Fatalf("phase = %q; want ready", phase)
	}
	// Reset drops the prior table.
	if err := store.ResetStaging(ctx, "v2"); err != nil {
		t.Fatalf("ResetStaging v2: %v", err)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 0 {
		t.Fatalf("after reset remaining = %d; want 0", rem.Chunks)
	}
}

func TestCarryForwardEmbeddings(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Live: page1/0 "v1", page1/1 "v2", orphan page9/0 "v9" (all embedded).
	seedChunks(t, store, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(1, 1, "v2"), unitVec(2)),
		withEmbedding(wikiChunk(9, 0, "v9"), unitVec(9)),
	})
	// New dump in staging: page1/0 unchanged, page1/1 changed, page2/0 new.
	stageChunks(t, store, "v2", []domain.WikiChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(1, 1, "v2-changed"), wikiChunk(2, 0, "v7"),
	})

	carried, err := store.CarryForwardEmbeddings(ctx)
	if err != nil {
		t.Fatalf("CarryForwardEmbeddings: %v", err)
	}
	if carried != 1 {
		t.Fatalf("carried = %d; want 1 (only page1/0)", carried)
	}
	rem, _ := store.StagingRemaining(ctx)
	if rem.Chunks != 2 {
		t.Fatalf("remaining unembedded = %d; want 2 (changed + new)", rem.Chunks)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM "+wikiStagingTable+" WHERE page_id = 9"); n != 0 {
		t.Fatalf("orphan page9 entered staging: %d rows", n)
	}
}

func TestStagingEmbedInPlace(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	stageChunks(t, store, "v1", []domain.WikiChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2"), wikiChunk(3, 0, "v3"),
	})

	batch, err := store.UnembeddedStaging(ctx, 2)
	if err != nil || len(batch) != 2 || batch[0].PageID != 1 || batch[1].PageID != 2 {
		t.Fatalf("batch = %+v, %v; want pages 1,2 in keyset order", batch, err)
	}
	for i := range batch {
		batch[i] = withEmbedding(batch[i], unitVec(int(batch[i].PageID)))
	}
	if err := store.UpdateStagingEmbeddings(ctx, batch); err != nil {
		t.Fatalf("UpdateStagingEmbeddings: %v", err)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 1 {
		t.Fatalf("remaining = %d; want 1", rem.Chunks)
	}
}

func TestFinalizeStagingSwapAndHeal(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Live holds an orphan page (9) absent from the new dump.
	seedChunks(t, store, []domain.WikiChunk{withEmbedding(wikiChunk(9, 0, "v9"), unitVec(9))})
	// Build a fully embedded staging for two real pages.
	stageChunks(t, store, "v2", []domain.WikiChunk{wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2")})
	b, _ := store.UnembeddedStaging(ctx, 10)
	for i := range b {
		b[i] = withEmbedding(b[i], unitVec(int(b[i].PageID)))
	}
	if err := store.UpdateStagingEmbeddings(ctx, b); err != nil {
		t.Fatalf("UpdateStagingEmbeddings: %v", err)
	}
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}

	if err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks"); n != 2 {
		t.Fatalf("live count = %d; want 2", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks WHERE page_id = 9"); n != 0 {
		t.Fatalf("orphan survived the swap: %d rows", n)
	}
	if stagingExistsT(t, store) {
		t.Error("staging was not dropped after the swap")
	}
	// Checkpoint advanced to v2; the plan now short-circuits.
	if p, err := store.StagingPlan(ctx, "v2"); err != nil || p != wiki.PlanAlreadyCurrent {
		t.Fatalf("plan after swap = %v, %v; want PlanAlreadyCurrent", p, err)
	}
}

func TestStagingSwapPreservesSectionAndKind(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	// Stage two chunks carrying distinct metadata, embed them, and swap live.
	body := wikiChunk(1, 0, "v1")
	body.Section = "History"
	body.Kind = domain.WikiChunkKindBody
	lead := wikiChunk(2, 0, "v2") // helper default: section "", kind lead
	stageChunks(t, store, "v2", []domain.WikiChunk{body, lead})
	b, _ := store.UnembeddedStaging(ctx, 10)
	for i := range b {
		b[i] = withEmbedding(b[i], unitVec(int(b[i].PageID)))
	}
	if err := store.UpdateStagingEmbeddings(ctx, b); err != nil {
		t.Fatalf("UpdateStagingEmbeddings: %v", err)
	}
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}
	if err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}

	if s, k := wikiChunkMeta(t, store, 1, 0); s != "History" || k != "body" {
		t.Errorf("page1 after swap (section, kind) = (%q, %q), want (History, body)", s, k)
	}
	if s, k := wikiChunkMeta(t, store, 2, 0); s != "" || k != "lead" {
		t.Errorf("page2 after swap (section, kind) = (%q, %q), want (\"\", lead)", s, k)
	}
}

func TestCarryForwardIgnoresMetadataOnlyChange(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Live: page1/0 content "v1", embedded, tagged History/body.
	live := withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))
	live.Section = "History"
	live.Kind = domain.WikiChunkKindBody
	seedChunks(t, store, []domain.WikiChunk{live})
	// New dump: identical content, metadata-only change (back to lead/"").
	stageChunks(t, store, "v2", []domain.WikiChunk{wikiChunk(1, 0, "v1")})

	carried, err := store.CarryForwardEmbeddings(ctx)
	if err != nil {
		t.Fatalf("CarryForwardEmbeddings: %v", err)
	}
	if carried != 1 {
		t.Fatalf("carried = %d; want 1 (content unchanged: a metadata-only change must not re-embed)", carried)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 0 {
		t.Fatalf("remaining unembedded = %d; want 0 (embedding carried despite the metadata change)", rem.Chunks)
	}
}

func TestFinalizeStagingRefusesNull(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	stageChunks(t, store, "v2", []domain.WikiChunk{wikiChunk(2, 0, "v2")})
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}

	err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0)
	if err == nil || !strings.Contains(err.Error(), "unembedded") {
		t.Fatalf("err = %v; want unembedded refusal", err)
	}
	// Live is untouched and staging survives the refused swap.
	if n := scalarInt(t, store, "SELECT count(*) FROM wiki_chunks"); n != 1 {
		t.Errorf("live corpus changed: %d chunks, want 1", n)
	}
	if !stagingExistsT(t, store) {
		t.Error("staging was dropped despite the refused swap")
	}
}

func TestBulkEmbedEndToEnd(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	// Staging built by ingest: three pages, no embeddings yet.
	stageChunks(t, store, testDumpVersion, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"), wikiChunk(2, 0, "v1"), wikiChunk(3, 0, "v2"),
	})
	if err := store.MarkStagingReady(ctx, testDumpVersion); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}

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

	// The swap checkpointed the dump version, so a re-run is a no-op.
	if p, err := store.StagingPlan(ctx, testDumpVersion); err != nil || p != wiki.PlanAlreadyCurrent {
		t.Errorf("plan after swap = %v, %v; want PlanAlreadyCurrent", p, err)
	}
}

func TestBulkEmbedResumeEmbedsOnlyRemaining(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	// A prior run already embedded page 1 into staging before dying.
	stageChunks(t, store, testDumpVersion, []domain.WikiChunk{
		wikiChunk(1, 0, "v0"), wikiChunk(2, 0, "v1"), wikiChunk(3, 0, "v2"),
	})
	if err := store.UpdateStagingEmbeddings(ctx, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v0"), unitVec(0)),
	}); err != nil {
		t.Fatalf("UpdateStagingEmbeddings: %v", err)
	}
	if err := store.MarkStagingReady(ctx, testDumpVersion); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
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

func TestSetStagingChunkEmbedding(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	stageChunks(t, store, "v1", []domain.WikiChunk{wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2")})

	updated, err := store.SetStagingChunkEmbedding(ctx, 1, 0, unitVec(1))
	if err != nil || !updated {
		t.Fatalf("first write: updated=%v, err=%v; want true, nil", updated, err)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 1 {
		t.Fatalf("remaining after one write = %d; want 1", rem.Chunks)
	}

	// A redelivery rewrites the same row safely and leaves the corpus unchanged.
	updated, err = store.SetStagingChunkEmbedding(ctx, 1, 0, unitVec(1))
	if err != nil || !updated {
		t.Fatalf("idempotent rewrite: updated=%v, err=%v; want true, nil", updated, err)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 1 {
		t.Fatalf("remaining after rewrite = %d; want 1 (idempotent)", rem.Chunks)
	}

	// A job for a chunk not in staging matches nothing: dropped, not an error.
	updated, err = store.SetStagingChunkEmbedding(ctx, 999, 0, unitVec(1))
	if err != nil {
		t.Fatalf("absent row: err=%v; want nil", err)
	}
	if updated {
		t.Fatal("absent row: updated=true; want false")
	}
}

func TestSetStagingChunkEmbeddingRejectsWrongDimension(t *testing.T) {
	store := setupStore(t)
	stageChunks(t, store, "v1", []domain.WikiChunk{wikiChunk(1, 0, "v1")})
	if _, err := store.SetStagingChunkEmbedding(t.Context(), 1, 0, []float32{1, 2, 3}); err == nil {
		t.Fatal("want error for wrong embedding dimension")
	}
}

func TestSetStagingChunkEmbeddingNoStagingTable(t *testing.T) {
	store := setupStore(t)
	// No staging table exists (none reset): a late job after a completed swap
	// must drop cleanly rather than error and loop.
	updated, err := store.SetStagingChunkEmbedding(t.Context(), 1, 0, unitVec(1))
	if err != nil {
		t.Fatalf("absent staging table: err=%v; want nil", err)
	}
	if updated {
		t.Fatal("absent staging table: updated=true; want false")
	}
}

func TestSetStagingChunkEmbeddingSearchableAfterSwap(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	stageChunks(t, store, "v2", []domain.WikiChunk{wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2")})

	// Drive the worker's per-chunk write for every staged chunk, then swap live.
	batch, err := store.UnembeddedStaging(ctx, 10)
	if err != nil {
		t.Fatalf("UnembeddedStaging: %v", err)
	}
	for _, c := range batch {
		if _, err := store.SetStagingChunkEmbedding(ctx, c.PageID, c.ChunkIndex, unitVec(int(c.PageID))); err != nil {
			t.Fatalf("SetStagingChunkEmbedding(%d): %v", c.PageID, err)
		}
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 0 {
		t.Fatalf("remaining after writing every chunk = %d; want 0", rem.Chunks)
	}
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}
	if err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}

	got, err := store.SearchWiki(ctx, unitVec(2), 1)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(got) != 1 || got[0].Content != "v2" {
		t.Fatalf("nearest to unitVec(2) = %+v; want the chunk written with content v2", got)
	}
	if got[0].Distance > 1e-4 {
		t.Errorf("nearest distance = %v; want ~0 (the worker's vector is searchable)", got[0].Distance)
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

func wikiChunkMeta(t *testing.T, store *Store, pageID int64, idx int) (section, kind string) {
	t.Helper()
	if err := store.pool.QueryRow(
		t.Context(),
		"SELECT section, kind FROM wiki_chunks WHERE page_id = $1 AND chunk_index = $2",
		pageID, idx,
	).Scan(&section, &kind); err != nil {
		t.Fatalf("read metadata for (%d, %d): %v", pageID, idx, err)
	}
	return section, kind
}

func scalarInt(t *testing.T, store *Store, query string) int {
	t.Helper()
	var n int
	if err := store.pool.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
