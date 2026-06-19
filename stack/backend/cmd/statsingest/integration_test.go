package main

import (
	"context"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This end-to-end test proves the stats path drives the real bulk-into-live
// pipeline against a throwaway Postgres: the producer upserts un-embedded stat
// passages and enqueues one embed job each, the embedworker drains the in-process
// queue and writes the vectors into the live wiki_chunks table, and the now
// embedded passages become retrievable via SearchWiki. It needs no broker - an
// in-memory queue stands in for RabbitMQ so the test is hermetic - and skips
// without TEST_DATABASE_URL. The schema reset drops tables, so a throwaway DB is
// required (never the shared dev database).

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the stats fleet-routing integration test")
	}
	return dsn
}

func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
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
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, political_claims, voting_records"); err != nil {
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

// memQueue is an in-process stand-in for the broker that satisfies the producer's
// Publish surface and the worker's Stream/Delivery/Enqueuer surfaces, so the e2e
// drives the real producer and worker without RabbitMQ. Priority is irrelevant to
// correctness here, so deliveries come back in publish order.
type memQueue struct {
	mu   sync.Mutex
	msgs [][]byte
}

func (q *memQueue) Publish(_ context.Context, body []byte, _ uint8) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.msgs = append(q.msgs, append([]byte(nil), body...))
	return nil
}

func (q *memQueue) Enqueue(ctx context.Context, body []byte, priority uint8) error {
	return q.Publish(ctx, body, priority)
}

func (q *memQueue) Consume(ctx context.Context) (<-chan embedjob.Delivery, error) {
	out := make(chan embedjob.Delivery)
	go func() {
		defer close(out)
		for {
			q.mu.Lock()
			var body []byte
			if len(q.msgs) > 0 {
				body = q.msgs[0]
				q.msgs = q.msgs[1:]
			}
			q.mu.Unlock()
			if body == nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Millisecond):
					continue
				}
			}
			select {
			case out <- &memDelivery{q: q, body: body}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// memDelivery is one in-process message awaiting acknowledgement. A Nack returns
// the body to the queue so a requeued job is redelivered, mirroring the broker.
type memDelivery struct {
	q    *memQueue
	body []byte
}

func (d *memDelivery) Body() []byte    { return d.body }
func (d *memDelivery) Priority() uint8 { return 0 }
func (d *memDelivery) Version() string { return "1" }
func (d *memDelivery) Ack() error      { return nil }
func (d *memDelivery) Nack(requeue bool) error {
	if requeue {
		d.q.mu.Lock()
		d.q.msgs = append(d.q.msgs, d.body)
		d.q.mu.Unlock()
	}
	return nil
}

// orthogonalEmbedder maps each distinct content to a near-orthogonal unit vector
// by hashing it to a single hot dimension, so a query embedded the same way is an
// exact nearest neighbor of its passage (and clearly distinct from the others),
// and re-embedding the same content reproduces the same vector.
type orthogonalEmbedder struct{}

func (orthogonalEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = vectorFor(text)
	}
	return out, nil
}

func vectorFor(text string) []float32 {
	v := make([]float32, domain.EmbeddingDim)
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	v[h.Sum32()%domain.EmbeddingDim] = 1
	return v
}

// statSource yields fixed datapoints under the eurostat corpus so the e2e drives
// the real foundation without a network call.
type statSource struct{ dps []domain.Datapoint }

func (s statSource) Datapoints(context.Context) ([]domain.Datapoint, error) { return s.dps, nil }
func (statSource) Corpus() string                                           { return domain.StatCorpus }

func twoPermitsE2E() []domain.Datapoint {
	base := domain.Datapoint{
		SourceName: "Eurostat",
		SourceURL:  "https://ec.europa.eu/eurostat/MIGR_RESFIRST",
		Dataset:    "MIGR_RESFIRST",
		SeriesKey:  "A.TOTAL.TOTAL.TOTAL.PER.FR",
		Title:      "Premiers titres de séjour délivrés",
		Geography:  "France",
		Dimensions: []string{"toutes nationalités"},
		Unit:       "personnes",
	}
	a, b := base, base
	a.Period, a.Figure = "2021", 287179
	b.Period, b.Figure = "2022", 326948
	return []domain.Datapoint{a, b}
}

func TestStatsIngestRoutesThroughFleetEndToEnd(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	mq := &memQueue{}
	dps := twoPermitsE2E()

	// Producer: render, upsert un-embedded, enqueue one job per passage. It must
	// make NO inline embedding call - the producer is given no embedder at all.
	st, err := stats.Run(ctx, slog.New(slog.DiscardHandler), statSource{dps: dps}, store, mq,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("stats.Run: %v", err)
	}
	if st.Upserted != 2 || st.Published != 2 {
		t.Fatalf("producer upserted %d published %d, want 2/2", st.Upserted, st.Published)
	}

	// Before the fleet runs, the passages are present but un-embedded.
	rem, err := store.CountUnembeddedLiveCorpus(ctx, domain.StatCorpus)
	if err != nil {
		t.Fatalf("CountUnembeddedLiveCorpus: %v", err)
	}
	if rem != 2 {
		t.Fatalf("un-embedded stat chunks before drain = %d, want 2", rem)
	}

	// Fleet: drain the queue, embedding each job and writing the vector in place.
	runCtx, cancel := context.WithCancel(ctx)
	worker := embedjob.NewWorker(orthogonalEmbedder{}, store, mq, mq,
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	waitUnembedded(ctx, t, store, 0)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}

	// The now-embedded passages are retrievable: a query embedded like the 2022
	// passage returns it as the nearest neighbor above the floor.
	want := stats.RenderFrench(dps[1])
	got, err := store.SearchWiki(ctx, vectorFor(want), 1)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(got) != 1 || got[0].Content != want {
		t.Fatalf("nearest = %+v; want the embedded 2022 passage %q", got, want)
	}
}

func waitUnembedded(ctx context.Context, t *testing.T, store *postgres.Store, want int64) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		n, err := store.CountUnembeddedLiveCorpus(ctx, domain.StatCorpus)
		if err != nil {
			t.Fatalf("CountUnembeddedLiveCorpus: %v", err)
		}
		if n == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d un-embedded chunks, still %d", want, n)
		case <-tick.C:
		}
	}
}
