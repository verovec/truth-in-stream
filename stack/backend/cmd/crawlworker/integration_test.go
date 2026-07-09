package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This test drives the whole crawl worker through a real broker and database:
// publish self-contained crawl jobs, let the adapters feed them to the worker,
// and assert the embedded chunks land in live wiki_chunks. It skips unless both
// TEST_RABBITMQ_URL and TEST_DATABASE_URL are set, mirroring the embedding-worker
// suite. A throwaway broker/database is required; the schema reset drops tables.

func integrationEnv(t *testing.T) (broker, dsn string) {
	t.Helper()
	broker = os.Getenv("TEST_RABBITMQ_URL")
	dsn = os.Getenv("TEST_DATABASE_URL")
	if broker == "" || dsn == "" {
		t.Skip("set TEST_RABBITMQ_URL and TEST_DATABASE_URL to run the crawl-worker integration test")
	}
	return broker, dsn
}

// fixedEmbedder returns a constant orthogonal unit vector for every input, so a
// nearest-neighbor query against it is exact and the test needs no Voyage key.
type fixedEmbedder struct{}

func (fixedEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, domain.EmbeddingDim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
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

func newQueue(t *testing.T, broker, name string) *queue.Client {
	t.Helper()
	client, err := queue.New(queue.Config{URL: broker, QueueName: name, Version: "1", MaxPriority: 10, Prefetch: 2})
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func publishCrawlJob(ctx context.Context, t *testing.T, client *queue.Client, j crawljob.CrawlJob, priority uint8) {
	t.Helper()
	body, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	if err := client.Publish(ctx, queue.Message{Body: body, Priority: priority}); err != nil {
		t.Fatalf("publish job page %d: %v", j.PageID, err)
	}
}

// waitForEmbeddedRows polls SearchEvidence until it returns at least want hits for
// the embedder's fixed vector, or the deadline passes. A real broker has no
// deterministic completion signal, so a bounded poll is the honest way to wait.
func waitForEmbeddedRows(ctx context.Context, t *testing.T, store *postgres.Store, want int) {
	t.Helper()
	query := make([]float32, domain.EmbeddingDim)
	query[0] = 1
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		got, err := store.SearchEvidence(ctx, query, want+5)
		if err != nil {
			t.Fatalf("SearchEvidence: %v", err)
		}
		if len(got) >= want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d embedded rows, have %d", want, len(got))
		case <-tick.C:
		}
	}
}

func TestCrawlWorkerDrainsQueueIntoLive(t *testing.T) {
	broker, dsn := integrationEnv(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	client := newQueue(t, broker, "crawl.chunks.crawlworker_e2e")
	for i := 1; i <= 2; i++ {
		publishCrawlJob(ctx, t, client, crawljob.CrawlJob{
			PageID: int64(100 + i), ChunkIndex: 0, Title: "T", URL: "u",
			RevisionID: 1, Corpus: "test-crawl", Content: "content", Section: "", Kind: "lead",
		}, 5)
	}

	worker := crawljob.NewWorker(fixedEmbedder{}, store, qStream{client: client}, qEnqueuer{client: client},
		slog.New(slog.DiscardHandler), crawljob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	waitForEmbeddedRows(ctx, t, store, 2)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}
}
