package main

import (
	"context"
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
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// This drives the whole bulk producer through a real broker and database: stage
// chunks, run RunBulkEnqueue to publish one job per chunk, let a real worker
// drain the queue, and assert the producer waits for the drain and swaps the
// embedded corpus live. It skips unless both TEST_RABBITMQ_URL and
// TEST_DATABASE_URL are set, mirroring the queue and store integration suites. A
// throwaway broker/database is required; the schema reset drops tables.

func wikisyncIntegrationEnv(t *testing.T) (broker, dsn string) {
	t.Helper()
	broker = os.Getenv("TEST_RABBITMQ_URL")
	dsn = os.Getenv("TEST_DATABASE_URL")
	if broker == "" || dsn == "" {
		t.Skip("set TEST_RABBITMQ_URL and TEST_DATABASE_URL to run the wikisync producer integration test")
	}
	return broker, dsn
}

// nnEmbedder maps each chunk's content "v<n>" to the orthogonal unit vector at
// index n, so a nearest-neighbor query is exact.
type nnEmbedder struct{}

func (nnEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		n, _ := strconv.Atoi(strings.TrimPrefix(text, "v"))
		v := make([]float32, domain.EmbeddingDim)
		v[n] = 1
		out[i] = v
	}
	return out, nil
}

// The adapters below bridge the broker to the worker's transport-free interfaces,
// the same wiring cmd/embedworker uses; the producer side under test uses the
// qPublisher adapter from this package.
type wDelivery struct{ d queue.Delivery }

func (q wDelivery) Body() []byte            { return q.d.Body }
func (q wDelivery) Priority() uint8         { return q.d.Priority }
func (q wDelivery) Version() string         { return q.d.Version }
func (q wDelivery) Ack() error              { return q.d.Ack() }
func (q wDelivery) Nack(requeue bool) error { return q.d.Nack(requeue) }

type wStream struct{ client *queue.Client }

func (s wStream) Consume(ctx context.Context) (<-chan embedjob.Delivery, error) {
	raw, err := s.client.Consume(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan embedjob.Delivery)
	go func() {
		defer close(out)
		for d := range raw {
			select {
			case out <- wDelivery{d: d}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

type wEnqueuer struct{ client *queue.Client }

func (e wEnqueuer) Enqueue(ctx context.Context, body []byte, priority uint8) error {
	return e.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}

func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state"); err != nil {
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

func stagedChunk(pageID int64, content string, kind domain.WikiChunkKind) domain.WikiChunk {
	return domain.WikiChunk{
		PageID: pageID, ChunkIndex: 0, Title: "T",
		URL: "https://simple.wikipedia.org/wiki/T", RevisionID: 100,
		Corpus: "simplewiki", Content: content, Kind: kind,
	}
}

func TestBulkEnqueueDrainsFleetAndSwapsLive(t *testing.T) {
	broker, dsn := wikisyncIntegrationEnv(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.EnsureCorpus(ctx, "simplewiki"); err != nil {
		t.Fatalf("EnsureCorpus: %v", err)
	}
	if err := store.ResetStaging(ctx, "v2"); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	// A mix of lead and body chunks exercises the priority mapping end to end.
	if err := store.UpsertStagingChunks(ctx, []domain.WikiChunk{
		stagedChunk(1, "v1", domain.WikiChunkKindLead),
		stagedChunk(2, "v2", domain.WikiChunkKindBody),
		stagedChunk(3, "v3", domain.WikiChunkKindLead),
	}); err != nil {
		t.Fatalf("UpsertStagingChunks: %v", err)
	}

	client, err := queue.New(queue.Config{URL: broker, QueueName: "embedding.jobs.wikisync_e2e.v1", Version: "1", MaxPriority: 10, Prefetch: 1})
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// A real worker drains the queue concurrently with the producer.
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	worker := embedjob.NewWorker(nnEmbedder{}, store, wStream{client: client}, wEnqueuer{client: client},
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})
	workerErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		workerErr <- worker.Run(runCtx)
	}()

	stats, err := wiki.RunBulkEnqueue(ctx, slog.New(slog.DiscardHandler), store, qPublisher{client: client}, wiki.ProducerConfig{
		Corpus:             "simplewiki",
		DumpVersion:        "v2",
		MaxPriority:        10,
		EnqueueBatchSize:   2,
		DrainPollInterval:  100 * time.Millisecond,
		DrainStallTimeout:  20 * time.Second,
		MaintenanceWorkMem: "64MB",
		MaxParallelWorkers: 0,
	})
	cancel()
	wg.Wait()
	if err != nil {
		t.Fatalf("RunBulkEnqueue: %v", err)
	}
	if werr := <-workerErr; werr != nil {
		t.Fatalf("worker Run: %v", werr)
	}
	if stats.Published != 3 {
		t.Errorf("published = %d, want 3", stats.Published)
	}

	// The producer swapped staging live only after the fleet drained: the corpus
	// is searchable and the checkpoint advanced, so a re-plan is a no-op.
	q := make([]float32, domain.EmbeddingDim)
	q[2] = 1
	got, err := store.SearchWiki(ctx, q, 1)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(got) != 1 || got[0].Content != "v2" {
		t.Fatalf("nearest = %+v; want the chunk the fleet embedded with content v2", got)
	}
	if p, err := store.StagingPlan(ctx, "v2"); err != nil || p != wiki.PlanAlreadyCurrent {
		t.Fatalf("plan after swap = %v, %v; want PlanAlreadyCurrent", p, err)
	}
}
