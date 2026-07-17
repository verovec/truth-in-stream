package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This drives the clustering job over a real database: seed an embedded corpus,
// run clusterCorpus through the real store, and assert every chunk's cluster id
// and importance landed in the live table - the scores the next ingest's
// producer reads. It skips without TEST_DATABASE_URL, mirroring the store
// integration suite. A throwaway database is required; the schema reset drops
// tables.

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
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, document_sentences, document_claims, segment_results, processed_videos, video_analyses, videos, tv_channels, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, evidence_chunks, evidence_chunks_staging, evidence_chunks_old, evidence_sync_state, political_claims, voting_records"); err != nil {
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

func TestClusterCorpusWritesScoresEndToEnd(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the wikicluster integration test")
	}
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureSource(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}

	// Two well-separated groups of embedded chunks, so a healthy clustering finds
	// two clusters and scores everything.
	chunks := make([]domain.EvidenceChunk, 0, 10)
	for i := range 6 {
		c := chunk(int64(i+1), "v1")
		c.Embedding = unitVec(1)
		chunks = append(chunks, c)
	}
	for i := range 4 {
		c := chunk(int64(i+7), "v2")
		c.Embedding = unitVec(2)
		chunks = append(chunks, c)
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	if err := store.SetChunkEmbeddings(ctx, chunks); err != nil {
		t.Fatalf("SetChunkEmbeddings: %v", err)
	}

	// A verification pool reads the live table directly (the store's pool is
	// unexported), so the test asserts on the rows the job wrote.
	verify, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("verify pool: %v", err)
	}
	t.Cleanup(verify.Close)

	cfg := config.WikiCluster{K: 2, MaxIters: 20, Seed: 1, ReadBatch: 4, WriteBatch: 3}
	st, err := clusterCorpus(ctx, discardLogger(), store, cfg)
	if err != nil {
		t.Fatalf("clusterCorpus: %v", err)
	}
	if st.Chunks != 10 {
		t.Errorf("clustered %d chunks, want 10", st.Chunks)
	}
	if st.Clusters != 2 {
		t.Errorf("found %d clusters, want 2 (two separated groups)", st.Clusters)
	}

	// Every embedded chunk now carries a non-null cluster id and importance.
	var scored int
	if err := verify.QueryRow(
		ctx,
		"SELECT count(*)::int FROM evidence_chunks WHERE metadata->>'cluster_id' IS NOT NULL AND metadata->>'importance' IS NOT NULL",
	).Scan(&scored); err != nil {
		t.Fatalf("count scored: %v", err)
	}
	if scored != 10 {
		t.Errorf("%d chunks have clustering scores, want 10", scored)
	}

	// Re-running over the unchanged corpus is idempotent: the scores do not move.
	before := importanceSnapshot(ctx, t, verify)
	if _, err := clusterCorpus(ctx, discardLogger(), store, cfg); err != nil {
		t.Fatalf("clusterCorpus (rerun): %v", err)
	}
	after := importanceSnapshot(ctx, t, verify)
	for page, imp := range before {
		if after[page] != imp {
			t.Errorf("page %s importance moved on a re-run: %v -> %v (must be idempotent)", page, imp, after[page])
		}
	}
}

func chunk(pageID int64, content string) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     "simplewiki",
		ExternalID: strconv.FormatInt(pageID, 10),
		ChunkIndex: 0,
		Title:      "T",
		URL:        "https://simple.wikipedia.org/wiki/T",
		Content:    content,
		Kind:       domain.EvidenceKindLead,
		Metadata:   domain.WikiMetadata{RevisionID: 1}.Map(),
	}
}

func importanceSnapshot(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]float64 {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT external_id, (metadata->>'importance')::float8 FROM evidence_chunks WHERE metadata->>'importance' IS NOT NULL")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var (
			externalID string
			imp        float64
		)
		if err := rows.Scan(&externalID, &imp); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		out[externalID] = imp
	}
	return out
}
