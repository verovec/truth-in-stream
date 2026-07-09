package postgres

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

func withEmbedding(c domain.EvidenceChunk, v []float32) domain.EvidenceChunk {
	c.Embedding = v
	return c
}

// pageSeed parses a chunk's numeric external id back to the int the unitVec
// helper seeds on, so the embedding written for a staged chunk matches the
// vector a later similarity query is built from.
func pageSeed(t *testing.T, externalID string) int {
	t.Helper()
	n, err := strconv.Atoi(externalID)
	if err != nil {
		t.Fatalf("external id %q is not numeric: %v", externalID, err)
	}
	return n
}

// embedStagingChunk drives the embedding worker's per-chunk write, the path that
// now fills a staged chunk's vector (the producer publishes a job, the fleet
// embeds and calls this). Tests use it to stand in for a worker that has drained
// the queue.
func embedStagingChunk(t *testing.T, store *Store, source, externalID string, chunkIndex int, v []float32) {
	t.Helper()
	updated, err := store.SetStagingChunkEmbedding(t.Context(), source, externalID, chunkIndex, v)
	if err != nil {
		t.Fatalf("SetStagingChunkEmbedding(%s/%s,%d): %v", source, externalID, chunkIndex, err)
	}
	if !updated {
		t.Fatalf("SetStagingChunkEmbedding(%s/%s,%d): no staging row matched", source, externalID, chunkIndex)
	}
}

// testDumpVersion is the dump version the staging integration tests run under.
const testDumpVersion = "Mon, 01 Jun 2026 00:00:00 GMT"

// seedChunks claims the corpus and stores chunks in the live table; chunks
// carrying embeddings also have those written, so the live corpus is searchable,
// mirroring the state after a completed swap.
func seedChunks(t *testing.T, store *Store, chunks []domain.EvidenceChunk) {
	t.Helper()
	ctx := t.Context()
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	var embedded []domain.EvidenceChunk
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
func stageChunks(t *testing.T, store *Store, version string, chunks []domain.EvidenceChunk) {
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
	seedChunks(t, store, []domain.EvidenceChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	if err := store.SetSyncState(ctx, domain.EvidenceSyncState{Source: "simplewiki", DumpVersion: v1}); err != nil {
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
	stageChunks(t, store, "v1", []domain.EvidenceChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(1, 1, "v2"), wikiChunk(2, 0, "v3"),
	})

	rem, err := store.StagingRemaining(ctx)
	if err != nil {
		t.Fatalf("StagingRemaining: %v", err)
	}
	if rem.Chunks != 3 || rem.Documents != 2 {
		t.Fatalf("remaining = %+v; want 3 chunks / 2 documents", rem)
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
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(1, 1, "v2"), unitVec(2)),
		withEmbedding(wikiChunk(9, 0, "v9"), unitVec(9)),
	})
	// New dump in staging: page1/0 unchanged, page1/1 changed, page2/0 new.
	stageChunks(t, store, "v2", []domain.EvidenceChunk{
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
	if n := scalarInt(t, store, "SELECT count(*) FROM "+evidenceStagingTable+" WHERE external_id = '9'"); n != 0 {
		t.Fatalf("orphan page9 entered staging: %d rows", n)
	}
}

func TestStagingEmbedInPlace(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	stageChunks(t, store, "v1", []domain.EvidenceChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2"), wikiChunk(3, 0, "v3"),
	})

	batch, err := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 2)
	if err != nil || len(batch) != 2 || batch[0].ExternalID != "1" || batch[1].ExternalID != "2" {
		t.Fatalf("batch = %+v, %v; want pages 1,2 in keyset order", batch, err)
	}
	for _, c := range batch {
		embedStagingChunk(t, store, c.Source, c.ExternalID, c.ChunkIndex, unitVec(pageSeed(t, c.ExternalID)))
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 1 {
		t.Fatalf("remaining = %d; want 1", rem.Chunks)
	}

	// The keyset cursor pages past the embedded prefix: a read after page 2 yields
	// only the still-pending page 3.
	rest, err := store.UnembeddedStaging(ctx, domain.EvidenceCursor{Source: "simplewiki", ExternalID: "2", ChunkIndex: 0}, 10)
	if err != nil || len(rest) != 1 || rest[0].ExternalID != "3" {
		t.Fatalf("rest = %+v, %v; want only page 3", rest, err)
	}
}

func TestFinalizeStagingSwapAndHeal(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Live holds an orphan page (9) absent from the new dump.
	seedChunks(t, store, []domain.EvidenceChunk{withEmbedding(wikiChunk(9, 0, "v9"), unitVec(9))})
	// Build a fully embedded staging for two real pages.
	stageChunks(t, store, "v2", []domain.EvidenceChunk{wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2")})
	b, _ := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 10)
	for _, c := range b {
		embedStagingChunk(t, store, c.Source, c.ExternalID, c.ChunkIndex, unitVec(pageSeed(t, c.ExternalID)))
	}
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}

	if err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks"); n != 2 {
		t.Fatalf("live count = %d; want 2", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks WHERE external_id = '9'"); n != 0 {
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
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	// Stage two chunks carrying distinct metadata, embed them, and swap live.
	body := wikiChunk(1, 0, "v1")
	body.Metadata = domain.WikiMetadata{RevisionID: 100, Section: "History"}.Map()
	body.Kind = domain.EvidenceKindBody
	lead := wikiChunk(2, 0, "v2") // helper default: section "", kind lead
	stageChunks(t, store, "v2", []domain.EvidenceChunk{body, lead})
	b, _ := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 10)
	for _, c := range b {
		embedStagingChunk(t, store, c.Source, c.ExternalID, c.ChunkIndex, unitVec(pageSeed(t, c.ExternalID)))
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
	live.Metadata = domain.WikiMetadata{RevisionID: 100, Section: "History"}.Map()
	live.Kind = domain.EvidenceKindBody
	seedChunks(t, store, []domain.EvidenceChunk{live})
	// New dump: identical content, metadata-only change (back to lead/"").
	stageChunks(t, store, "v2", []domain.EvidenceChunk{wikiChunk(1, 0, "v1")})

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
	seedChunks(t, store, []domain.EvidenceChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	stageChunks(t, store, "v2", []domain.EvidenceChunk{wikiChunk(2, 0, "v2")})
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}

	err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0)
	if err == nil || !strings.Contains(err.Error(), "unembedded") {
		t.Fatalf("err = %v; want unembedded refusal", err)
	}
	// Live is untouched and staging survives the refused swap.
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks"); n != 1 {
		t.Errorf("live corpus changed: %d chunks, want 1", n)
	}
	if !stagingExistsT(t, store) {
		t.Error("staging was dropped despite the refused swap")
	}
}

func TestStagingEmbedAndSwapEndToEnd(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	// Staging built by ingest: three pages, no embeddings yet. The fleet drains
	// the queue by writing each chunk's vector through the worker's per-chunk path.
	stageChunks(t, store, testDumpVersion, []domain.EvidenceChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2"), wikiChunk(3, 0, "v3"),
	})
	if err := store.MarkStagingReady(ctx, testDumpVersion); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}

	batch, err := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedStaging: %v", err)
	}
	for _, c := range batch {
		embedStagingChunk(t, store, c.Source, c.ExternalID, c.ChunkIndex, unitVec(pageSeed(t, c.ExternalID)))
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 0 {
		t.Fatalf("remaining after the fleet drained = %d; want 0", rem.Chunks)
	}
	if err := store.FinalizeStaging(ctx, "simplewiki", testDumpVersion, time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}

	if stagingExistsT(t, store) {
		t.Error("staging table was not dropped after the swap")
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks WHERE embedding IS NULL"); n != 0 {
		t.Errorf("%d chunks left unembedded after a complete run", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks"); n != 3 {
		t.Errorf("live corpus has %d chunks after swap, want 3", n)
	}

	var nearest string
	if err := store.pool.QueryRow(
		ctx,
		"SELECT external_id FROM evidence_chunks ORDER BY embedding <=> $1 LIMIT 1",
		pgvector.NewHalfVector(unitVec(2)),
	).Scan(&nearest); err != nil {
		t.Fatalf("similarity query: %v", err)
	}
	if nearest != "2" {
		t.Errorf("nearest to unitVec(2) = page %s, want 2", nearest)
	}

	// The swap checkpointed the dump version, so a re-run is a no-op.
	if p, err := store.StagingPlan(ctx, testDumpVersion); err != nil || p != wiki.PlanAlreadyCurrent {
		t.Errorf("plan after swap = %v, %v; want PlanAlreadyCurrent", p, err)
	}
}

func TestUnembeddedStagingSkipsEmbeddedPrefixOnResume(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	// A prior run already embedded page 1 into staging before dying; the resume
	// read must offer only the still-pending chunks so the producer re-enqueues
	// only the remainder.
	stageChunks(t, store, testDumpVersion, []domain.EvidenceChunk{
		wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2"), wikiChunk(3, 0, "v3"),
	})
	embedStagingChunk(t, store, "simplewiki", "1", 0, unitVec(1))

	remaining, err := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedStaging: %v", err)
	}
	if len(remaining) != 2 || remaining[0].ExternalID != "2" || remaining[1].ExternalID != "3" {
		t.Fatalf("resume read = %+v; want only pages 2 and 3", remaining)
	}
	for _, c := range remaining {
		embedStagingChunk(t, store, c.Source, c.ExternalID, c.ChunkIndex, unitVec(pageSeed(t, c.ExternalID)))
	}
	if err := store.FinalizeStaging(ctx, "simplewiki", testDumpVersion, time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks WHERE embedding IS NULL"); n != 0 {
		t.Errorf("%d chunks unembedded after resume completed", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks"); n != 3 {
		t.Errorf("live corpus has %d chunks, want 3", n)
	}
}

func TestSetStagingChunkEmbedding(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	stageChunks(t, store, "v1", []domain.EvidenceChunk{wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2")})

	updated, err := store.SetStagingChunkEmbedding(ctx, "simplewiki", "1", 0, unitVec(1))
	if err != nil || !updated {
		t.Fatalf("first write: updated=%v, err=%v; want true, nil", updated, err)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 1 {
		t.Fatalf("remaining after one write = %d; want 1", rem.Chunks)
	}

	// A redelivery rewrites the same row safely and leaves the corpus unchanged.
	updated, err = store.SetStagingChunkEmbedding(ctx, "simplewiki", "1", 0, unitVec(1))
	if err != nil || !updated {
		t.Fatalf("idempotent rewrite: updated=%v, err=%v; want true, nil", updated, err)
	}
	if rem, _ := store.StagingRemaining(ctx); rem.Chunks != 1 {
		t.Fatalf("remaining after rewrite = %d; want 1 (idempotent)", rem.Chunks)
	}

	// A job for a chunk not in staging matches nothing: dropped, not an error.
	updated, err = store.SetStagingChunkEmbedding(ctx, "simplewiki", "999", 0, unitVec(1))
	if err != nil {
		t.Fatalf("absent row: err=%v; want nil", err)
	}
	if updated {
		t.Fatal("absent row: updated=true; want false")
	}
}

func TestSetStagingChunkEmbeddingRejectsWrongDimension(t *testing.T) {
	store := setupStore(t)
	stageChunks(t, store, "v1", []domain.EvidenceChunk{wikiChunk(1, 0, "v1")})
	if _, err := store.SetStagingChunkEmbedding(t.Context(), "simplewiki", "1", 0, []float32{1, 2, 3}); err == nil {
		t.Fatal("want error for wrong embedding dimension")
	}
}

func TestSetStagingChunkEmbeddingNoStagingTable(t *testing.T) {
	store := setupStore(t)
	// No staging table exists (none reset): a late job after a completed swap
	// must drop cleanly rather than error and loop.
	updated, err := store.SetStagingChunkEmbedding(t.Context(), "simplewiki", "1", 0, unitVec(1))
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
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	stageChunks(t, store, "v2", []domain.EvidenceChunk{wikiChunk(1, 0, "v1"), wikiChunk(2, 0, "v2")})

	// Drive the worker's per-chunk write for every staged chunk, then swap live.
	batch, err := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedStaging: %v", err)
	}
	for _, c := range batch {
		if _, err := store.SetStagingChunkEmbedding(ctx, c.Source, c.ExternalID, c.ChunkIndex, unitVec(pageSeed(t, c.ExternalID))); err != nil {
			t.Fatalf("SetStagingChunkEmbedding(%s): %v", c.ExternalID, err)
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

	got, err := store.SearchEvidence(ctx, unitVec(2), 1)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
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
		"SELECT to_regclass('evidence_chunks_staging') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("check staging existence: %v", err)
	}
	return exists
}

func wikiChunkMeta(t *testing.T, store *Store, pageID int64, idx int) (section, kind string) {
	t.Helper()
	if err := store.pool.QueryRow(
		t.Context(),
		"SELECT COALESCE(metadata->>'section', ''), kind FROM evidence_chunks WHERE source = 'simplewiki' AND external_id = $1 AND chunk_index = $2",
		strconv.FormatInt(pageID, 10), idx,
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

// liveEmbeddingNull reports whether live chunk (pageID, 0) has no embedding yet,
// the state a bulk-into-live ingest leaves until the fleet fills it.
func liveEmbeddingNull(t *testing.T, store *Store, pageID int64) bool {
	t.Helper()
	row, err := store.queries.GetEvidenceChunk(t.Context(), db.GetEvidenceChunkParams{Source: "simplewiki", ExternalID: strconv.FormatInt(pageID, 10), ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetEvidenceChunk(%d,0): %v", pageID, err)
	}
	return row.EmbeddingIsNull
}

func TestSetLiveChunkEmbeddingsWritesBatchInPlace(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.EvidenceChunk{
		wikiChunk(1, 0, "alpha"),
		wikiChunk(2, 0, "bravo"),
		wikiChunk(3, 0, "charlie"),
	}
	seedChunks(t, store, chunks) // all NULL embeddings

	matched, err := store.SetLiveChunkEmbeddings(ctx, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "alpha"), unitVec(0)),
		withEmbedding(wikiChunk(2, 0, "bravo"), unitVec(1)),
	})
	if err != nil {
		t.Fatalf("SetLiveChunkEmbeddings: %v", err)
	}
	if matched != 2 {
		t.Fatalf("matched = %d, want 2", matched)
	}
	if liveEmbeddingNull(t, store, 1) || liveEmbeddingNull(t, store, 2) {
		t.Error("embedded chunks should no longer be null")
	}
	if !liveEmbeddingNull(t, store, 3) {
		t.Error("un-embedded chunk 3 should remain null")
	}
	// The freshly embedded chunk is now searchable in place, no swap needed.
	got, err := store.SearchEvidence(ctx, unitVec(0), 1)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != "alpha" {
		t.Errorf("search = %+v, want the in-place embedded chunk 'alpha'", got)
	}
}

func TestSetLiveChunkEmbeddingsContentGuardSkipsChangedChunks(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.EvidenceChunk{wikiChunk(1, 0, "new text")})

	// A job carrying the old text must not attach its vector to a row whose
	// content has since changed.
	matched, err := store.SetLiveChunkEmbeddings(ctx, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "old text"), unitVec(0)),
	})
	if err != nil {
		t.Fatalf("SetLiveChunkEmbeddings: %v", err)
	}
	if matched != 0 {
		t.Errorf("matched = %d, want 0 (content guard)", matched)
	}
	if !liveEmbeddingNull(t, store, 1) {
		t.Error("chunk with changed content must stay null until its fresh vector lands")
	}
}

func TestSetLiveChunkEmbeddingsRejectsWrongDimension(t *testing.T) {
	store := setupStore(t)
	if _, err := store.SetLiveChunkEmbeddings(t.Context(), []domain.EvidenceChunk{
		{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0, Content: "x", Embedding: []float32{1, 2, 3}},
	}); err == nil {
		t.Fatal("want dimension error, got nil")
	}
}

func TestSetLiveChunkEmbeddingsEmptyIsNoop(t *testing.T) {
	store := setupStore(t)
	matched, err := store.SetLiveChunkEmbeddings(t.Context(), nil)
	if err != nil || matched != 0 {
		t.Fatalf("empty batch: matched=%d err=%v, want 0,nil", matched, err)
	}
}

func TestSetStagingChunkEmbeddingsWritesBatch(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	stageChunks(t, store, testDumpVersion, []domain.EvidenceChunk{
		wikiChunk(1, 0, "alpha"),
		wikiChunk(2, 0, "bravo"),
	})

	matched, err := store.SetStagingChunkEmbeddings(ctx, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "alpha"), unitVec(0)),
		withEmbedding(wikiChunk(2, 0, "bravo"), unitVec(1)),
	})
	if err != nil {
		t.Fatalf("SetStagingChunkEmbeddings: %v", err)
	}
	if matched != 2 {
		t.Fatalf("matched = %d, want 2", matched)
	}
	if remaining, err := store.CountUnembeddedStaging(ctx); err != nil || remaining != 0 {
		t.Fatalf("CountUnembeddedStaging = %d, %v; want 0", remaining, err)
	}
}

func TestSetStagingChunkEmbeddingsMissingTableIsObsolete(t *testing.T) {
	store := setupStore(t)
	// No staging table exists (corpus already swapped live): a late job must be
	// reported obsolete (matched 0, no error), not an error to retry.
	matched, err := store.SetStagingChunkEmbeddings(t.Context(), []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "alpha"), unitVec(0)),
	})
	if err != nil {
		t.Fatalf("SetStagingChunkEmbeddings: %v", err)
	}
	if matched != 0 {
		t.Errorf("matched = %d, want 0 (missing staging table is obsolete)", matched)
	}
}

func TestUnembeddedLiveReadsPendingWithMetadata(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Page 1 embedded, pages 2 and 3 pending.
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "embedded"), unitVec(0)),
		wikiChunk(2, 0, "pending-a"),
		wikiChunk(3, 0, "pending-b"),
	})

	count, err := store.CountUnembeddedLive(ctx)
	if err != nil || count != 2 {
		t.Fatalf("CountUnembeddedLive = %d, %v; want 2", count, err)
	}

	rem, err := store.LiveRemaining(ctx)
	if err != nil {
		t.Fatalf("LiveRemaining: %v", err)
	}
	if rem.Chunks != 2 || rem.Documents != 2 || rem.Chars == 0 {
		t.Fatalf("LiveRemaining = %+v, want 2 chunks/2 documents and some chars", rem)
	}

	// Keyset paging from the start returns only the un-embedded chunks, in order,
	// carrying the kind the producer maps to a priority.
	got, err := store.UnembeddedLive(ctx, domain.EvidenceCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedLive: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("UnembeddedLive returned %d chunks, want 2", len(got))
	}
	if got[0].ExternalID != "2" || got[1].ExternalID != "3" {
		t.Errorf("UnembeddedLive order = [%s,%s], want [2,3]", got[0].ExternalID, got[1].ExternalID)
	}
	if got[0].Kind != domain.EvidenceKindLead {
		t.Errorf("UnembeddedLive kind = %q, want lead", got[0].Kind)
	}

	// The cursor pages past the first pending chunk.
	page2, err := store.UnembeddedLive(ctx, domain.EvidenceCursor{Source: "simplewiki", ExternalID: "2", ChunkIndex: 0}, 10)
	if err != nil {
		t.Fatalf("UnembeddedLive page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ExternalID != "3" {
		t.Fatalf("UnembeddedLive after cursor = %+v, want only page 3", page2)
	}
}
