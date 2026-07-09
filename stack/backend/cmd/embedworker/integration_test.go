package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// These tests drive the whole worker through a real broker and database: publish
// jobs, let the adapters feed them to the worker, and assert the vectors land in
// staging. They skip unless both TEST_RABBITMQ_URL and TEST_DATABASE_URL are set,
// mirroring the queue and store integration suites. A throwaway broker/database
// is required; the schema reset drops tables.

func integrationEnv(t *testing.T) (broker, dsn string) {
	t.Helper()
	broker = os.Getenv("TEST_RABBITMQ_URL")
	dsn = os.Getenv("TEST_DATABASE_URL")
	if broker == "" || dsn == "" {
		t.Skip("set TEST_RABBITMQ_URL and TEST_DATABASE_URL to run the embedding-worker integration test")
	}
	return broker, dsn
}

// recordingEmbedder maps each chunk's content "v<n>" to the orthogonal unit
// vector at index n, so a nearest-neighbor query is exact, and records the order
// in which contents were embedded so a priority test can assert ordering.
type recordingEmbedder struct {
	mu    sync.Mutex
	order []string
}

func (e *recordingEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		e.mu.Lock()
		e.order = append(e.order, text)
		e.mu.Unlock()
		n, _ := strconv.Atoi(strings.TrimPrefix(text, "v"))
		v := make([]float32, domain.EmbeddingDim)
		v[n] = 1
		out[i] = v
	}
	return out, nil
}

func (e *recordingEmbedder) embedded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.order...)
}

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
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, document_sentences, document_claims, segment_results, processed_videos, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, evidence_chunks, evidence_chunks_staging, evidence_chunks_old, evidence_sync_state, political_claims, voting_records"); err != nil {
		t.Fatalf("reset: drop tables: %v", err)
	}
	dir := filepath.Join("..", "..", "migrations")
	ups, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
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

func chunk(pageID int64, content string) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source: "simplewiki", ExternalID: strconv.FormatInt(pageID, 10), ChunkIndex: 0, Title: "Paris",
		URL: "https://simple.wikipedia.org/wiki/Paris", Content: content, Kind: domain.EvidenceKindLead,
		Metadata: domain.WikiMetadata{RevisionID: 100}.Map(),
	}
}

func publishJob(ctx context.Context, t *testing.T, client *queue.Client, j embedjob.Job, priority uint8) {
	t.Helper()
	body, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	if err := client.Publish(ctx, queue.Message{Body: body, Priority: priority}); err != nil {
		t.Fatalf("publish job %s/%s: %v", j.Source, j.ExternalID, err)
	}
}

// waitForRemaining polls staging until the unembedded count reaches want or the
// deadline passes. An integration test against a real broker has no deterministic
// completion signal, so a bounded poll is the honest way to wait.
func waitForRemaining(ctx context.Context, t *testing.T, store *postgres.Store, want int64) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		rem, err := store.StagingRemaining(ctx)
		if err != nil {
			t.Fatalf("StagingRemaining: %v", err)
		}
		if rem.Chunks == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d unembedded chunks, still %d", want, rem.Chunks)
		case <-tick.C:
		}
	}
}

func TestWorkerEmbedsQueuedChunksEndToEnd(t *testing.T) {
	broker, dsn := integrationEnv(t)
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
	if err := store.ResetStaging(ctx, "v2"); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	if err := store.UpsertStagingChunks(ctx, []domain.EvidenceChunk{chunk(1, "v1"), chunk(2, "v2"), chunk(3, "v3")}); err != nil {
		t.Fatalf("UpsertStagingChunks: %v", err)
	}

	client := newQueue(t, broker, "embedding.jobs.embedworker_e2e")
	for _, pid := range []int64{1, 2, 3} {
		publishJob(ctx, t, client, embedjob.Job{Source: "simplewiki", ExternalID: strconv.FormatInt(pid, 10), ChunkIndex: 0, Content: "v" + strconv.FormatInt(pid, 10)}, 5)
	}

	runCtx, cancel := context.WithCancel(ctx)
	worker := embedjob.NewWorker(&recordingEmbedder{}, store, qStream{client: client}, qEnqueuer{client: client},
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	waitForRemaining(ctx, t, store, 0)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}

	// Every chunk embedded; finalize and confirm the corpus is searchable.
	if err := store.MarkStagingReady(ctx, "v2"); err != nil {
		t.Fatalf("MarkStagingReady: %v", err)
	}
	if err := store.FinalizeStaging(ctx, "simplewiki", "v2", time.Time{}, "64MB", 0); err != nil {
		t.Fatalf("FinalizeStaging: %v", err)
	}
	q := make([]float32, domain.EmbeddingDim)
	q[2] = 1
	got, err := store.SearchEvidence(ctx, q, 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != "v2" {
		t.Fatalf("nearest = %+v; want the chunk embedded with content v2", got)
	}
}

func TestWorkerEmbedsHigherPriorityFirst(t *testing.T) {
	broker, dsn := integrationEnv(t)
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
	if err := store.ResetStaging(ctx, "v2"); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	if err := store.UpsertStagingChunks(ctx, []domain.EvidenceChunk{chunk(1, "v1"), chunk(2, "v2")}); err != nil {
		t.Fatalf("UpsertStagingChunks: %v", err)
	}

	// Enqueue both jobs before the worker starts, low priority first. With a
	// single-slot worker the broker must hand back the high-priority job first.
	client := newQueue(t, broker, "embedding.jobs.embedworker_prio")
	publishJob(ctx, t, client, embedjob.Job{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0, Content: "v1"}, 1)
	publishJob(ctx, t, client, embedjob.Job{Source: "simplewiki", ExternalID: "2", ChunkIndex: 0, Content: "v2"}, 9)

	rec := &recordingEmbedder{}
	runCtx, cancel := context.WithCancel(ctx)
	worker := embedjob.NewWorker(rec, store, qStream{client: client}, qEnqueuer{client: client},
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 1, MaxAttempts: 3, KnownVersions: []string{"1"}})
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	waitForRemaining(ctx, t, store, 0)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}

	order := rec.embedded()
	if len(order) != 2 || order[0] != "v2" {
		t.Fatalf("embed order = %v; want the high-priority chunk v2 first", order)
	}
}

func newQueue(t *testing.T, broker, name string) *queue.Client {
	t.Helper()
	// A test-scoped queue name keeps these runs off the real embedding.jobs queue
	// and isolates the two tests from each other.
	client, err := queue.New(queue.Config{URL: broker, QueueName: name, Version: "1", MaxPriority: 10, Prefetch: 1})
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
