package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/source/sdmx"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This end-to-end test proves the SDMX connector drives the real bulk-into-live
// pipeline against a throwaway Postgres: an httptest server returns real-shaped
// SDMX-CSV, the sdmx.Source fetches and the stats foundation renders + upserts the
// passages un-embedded, the embedworker drains an in-process queue and writes the
// vectors into the live evidence_chunks table, and the now-embedded ECB passage is
// retrievable via SearchEvidence - the "live fact-check retrieves the ingested
// indicator" acceptance criterion. It needs no broker (an in-memory queue stands in
// for RabbitMQ) and skips without TEST_DATABASE_URL. The schema reset drops tables,
// so a throwaway DB is required (never the shared dev database).

const realECBCSV = `KEY,FREQ,REF_AREA,ADJUSTMENT,ICP_ITEM,STS_INSTITUTION,ICP_SUFFIX,TIME_PERIOD,OBS_VALUE,OBS_STATUS,OBS_CONF
ICP.M.U2.N.000000.4.ANR,M,U2,N,000000,4,ANR,2024-01,2.8,A,F
ICP.M.U2.N.000000.4.ANR,M,U2,N,000000,4,ANR,2024-02,2.6,A,F
`

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the SDMX connector integration test")
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
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claim_checks, claims, documents, document_sentences, document_claims, segment_results, processed_videos, video_analyses, videos, tv_channels, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, evidence_chunks, evidence_chunks_staging, evidence_chunks_old, evidence_sync_state, political_claims, voting_records"); err != nil {
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

// memQueue is an in-process stand-in for the broker satisfying the producer's
// Publish surface and the worker's Stream/Delivery/Enqueuer surfaces, so the e2e
// drives the real producer and worker without RabbitMQ.
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

// orthogonalEmbedder maps each distinct content to a near-orthogonal unit vector,
// so a query embedded the same way is an exact nearest neighbor of its passage and
// re-embedding the same content reproduces the same vector.
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
	var h uint32 = 2166136261
	for i := range len(text) {
		h ^= uint32(text[i])
		h *= 16777619
	}
	v[h%domain.EmbeddingDim] = 1
	return v
}

func TestSDMXConnectorRoutesThroughFleetEndToEnd(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realECBCSV))
	}))
	t.Cleanup(srv.Close)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	ep := sdmx.ECBEndpoint()
	ep.BaseURL = srv.URL
	ep.MinInterval = 0
	ep.Retry = httpx.RetryConfig{MaxRetries: -1}
	source := sdmx.NewSource(sdmx.New(ep), domain.ECBStatCorpus, sdmx.ECBSpecs(sdmx.Window{}))

	mq := &memQueue{}
	st, err := stats.Run(ctx, slog.New(slog.DiscardHandler), source, store, mq,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("stats.Run: %v", err)
	}
	// Two curated ECB specs x two observations each in the fixture.
	if st.Upserted != 4 || st.Published != 4 {
		t.Fatalf("producer upserted %d published %d, want 4/4", st.Upserted, st.Published)
	}

	rem, err := store.CountUnembeddedLiveSource(ctx, domain.ECBStatCorpus)
	if err != nil {
		t.Fatalf("CountUnembeddedLiveSource: %v", err)
	}
	if rem != 4 {
		t.Fatalf("un-embedded ECB chunks before drain = %d, want 4", rem)
	}

	runCtx, cancel := context.WithCancel(ctx)
	worker := embedjob.NewWorker(orthogonalEmbedder{}, store, mq, mq,
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	deadline := time.After(30 * time.Second)
	for {
		rem, err := store.CountUnembeddedLiveSource(ctx, domain.ECBStatCorpus)
		if err != nil {
			t.Fatalf("CountUnembeddedLiveSource: %v", err)
		}
		if rem == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("fleet did not embed all ECB chunks in time (remaining %d)", rem)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}

	// The now-embedded ECB inflation passage is retrievable: a query embedded like
	// one ingested passage returns it as the nearest neighbor.
	dps, err := source.Datapoints(ctx)
	if err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	want := stats.RenderFrench(dps[0])
	got, err := store.SearchEvidence(ctx, vectorFor(want), 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != want {
		t.Fatalf("nearest = %+v; want the embedded ECB passage %q", got, want)
	}
}
