package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This drives the verifier over a corpus built through the real swap path - the
// same FinalizeStaging the reingest ends with - so the HNSW index, embeddings,
// and metadata are exactly what `make reingest` produces. It asserts the verifier
// passes a healthy corpus and fails a corrupted one. It skips without
// TEST_DATABASE_URL; the schema reset drops tables.

func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	// Hold the shared schema-reset lock for the whole test, not just the
	// reset: the integration packages share one database, so releasing after
	// the reset would let another package drop these tables mid-test. Cleanup
	// runs at test end, serializing every DB-touching test across packages.
	release, err := pgtest.AcquireSchemaLock(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(release)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, document_sentences, document_claims, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, political_claims, voting_records"); err != nil {
		t.Fatalf("reset: drop tables: %v", err)
	}
	ups, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("reset: glob migrations: %v", err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		sql, err := os.ReadFile(up)
		if err != nil {
			t.Fatalf("reset: read %s: %v", up, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("reset: apply %s: %v", up, err)
		}
	}
}

func unitVec(hot int) []float32 {
	v := make([]float32, domain.EmbeddingDim)
	v[hot%domain.EmbeddingDim] = 1
	return v
}

// buildSwappedCorpus stages a few chunks, embeds them through the worker's
// per-chunk write, and finalizes the swap, leaving a live corpus identical in
// shape to what the reingest produces.
func buildSwappedCorpus(ctx context.Context, t *testing.T, store *postgres.Store) {
	t.Helper()
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	if err := store.ResetStaging(ctx, "v1"); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	chunks := []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, Title: "A", URL: "u1", RevisionID: 1, Corpus: "simplewiki", Content: "a", Kind: domain.WikiChunkKindLead},
		{PageID: 2, ChunkIndex: 0, Title: "B", URL: "u2", RevisionID: 1, Corpus: "simplewiki", Content: "b", Kind: domain.WikiChunkKindLead},
		{PageID: 3, ChunkIndex: 0, Title: "C", URL: "u3", RevisionID: 1, Corpus: "simplewiki", Content: "c", Kind: domain.WikiChunkKindLead},
	}
	if err := store.UpsertStagingChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertStagingChunks: %v", err)
	}
	for _, c := range chunks {
		if _, err := store.SetStagingChunkEmbedding(ctx, c.PageID, c.ChunkIndex, unitVec(int(c.PageID))); err != nil {
			t.Fatalf("SetStagingChunkEmbedding(%d): %v", c.PageID, err)
		}
	}
	if err := store.MarkStagingReady(ctx, "v1"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}
	if err := store.FinalizeStaging(ctx, "simplewiki", "v1", time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}
}

func TestVerifierPassesHealthyAndFailsCorruptedCorpus(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the wikiverify integration test")
	}
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	buildSwappedCorpus(ctx, t, store)

	logger := slog.New(slog.DiscardHandler)

	// A fully embedded, swapped corpus passes every check.
	healthy, err := store.WikiCorpusHealth(ctx)
	if err != nil {
		t.Fatalf("WikiCorpusHealth (healthy): %v", err)
	}
	if err := report(ctx, logger, healthy); err != nil {
		t.Fatalf("verifier failed a healthy reingested corpus: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("verify pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// An un-embedded chunk is no longer a failure: a bulk-into-live corpus fills in
	// over time, so coverage is reported, not gated. The verifier still passes.
	if _, err := pool.Exec(ctx, "UPDATE wiki_chunks SET embedding = NULL WHERE page_id = 2"); err != nil {
		t.Fatalf("null one embedding: %v", err)
	}
	partial, err := store.WikiCorpusHealth(ctx)
	if err != nil {
		t.Fatalf("WikiCorpusHealth (partial): %v", err)
	}
	if err := report(ctx, logger, partial); err != nil {
		t.Fatalf("verifier failed a partially embedded corpus; coverage is progress, not a gate: %v", err)
	}

	// A real consistency defect among the embedded rows (a zero-norm vector) must
	// still exit non-zero.
	zero := "[" + strings.TrimSuffix(strings.Repeat("0,", 1024), ",") + "]"
	if _, err := pool.Exec(ctx, "UPDATE wiki_chunks SET embedding = $1::halfvec WHERE page_id = 1", zero); err != nil {
		t.Fatalf("write zero vector: %v", err)
	}
	corrupt, err := store.WikiCorpusHealth(ctx)
	if err != nil {
		t.Fatalf("WikiCorpusHealth (corrupt): %v", err)
	}
	if err := report(ctx, logger, corrupt); err == nil {
		t.Fatal("verifier passed a corpus with a zero-vector embedding; it must exit non-zero")
	}
}
