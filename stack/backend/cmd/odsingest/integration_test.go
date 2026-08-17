package main

import (
	"context"
	"fmt"
	"hash/fnv"
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
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
	"github.com/verovec/truth-in-stream/backend/internal/stats/ods"
	"github.com/verovec/truth-in-stream/backend/internal/stats/ssmsi"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This end-to-end test proves the OpenDataSoft/SSMSI connector drives the real
// bulk-into-live pipeline against a throwaway Postgres: the adapters fetch from
// httptest servers speaking the real wire format, the stats foundation renders and
// upserts un-embedded passages and enqueues one embed job each, the embedworker
// drains an in-memory queue and writes the vectors into evidence_chunks, and an
// ingested delinquency passage becomes retrievable via SearchEvidence. It needs no
// broker and skips without TEST_DATABASE_URL. The schema reset drops tables, so a
// throwaway DB is required (never the shared dev database).

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the odsingest end-to-end integration test")
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

// orthogonalEmbedder maps each distinct content to a one-hot unit vector, so a
// query embedded the same way is an exact nearest neighbor of its passage and
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
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	v[h.Sum32()%domain.EmbeddingDim] = 1
	return v
}

const ssmsiDepartmentalCSV = `"Code_departement";"Code_region";"annee";"indicateur";"unite_de_compte";"nombre";"taux_pour_mille";"insee_pop";"insee_pop_millesime";"insee_log";"insee_log_millesime"
"01";"84";"2016";"Homicides";"Victime";"5";"0,0078318";"638425";"2016";"308491";"2016"
"75";"11";"2016";"Vols sans violence contre des personnes";"Infraction";"90000";"40,12";"2190327";"2016";"1300000";"2016"
`

// ssmsiTestServer serves the data.gouv.fr dataset API and the departmental CSV so
// the real ssmsi.Client resolves and downloads the base without a network call.
func ssmsiTestServer(t *testing.T) *ssmsi.Client {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	csvURL := srv.URL + "/donnee-dep.csv"
	mux.HandleFunc("/donnee-dep.csv", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ssmsiDepartmentalCSV))
	})
	mux.HandleFunc("/api/1/datasets/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"resources":[{"title":"Base départementale","format":"csv","url":%q}]}`, csvURL)
	})
	return ssmsi.New(ssmsi.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
}

func drainToEmpty(ctx context.Context, t *testing.T, store *postgres.Store, mq *memQueue, corpus string) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	worker := embedjob.NewWorker(orthogonalEmbedder{}, store, mq, mq,
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		n, err := store.CountUnembeddedLiveSource(ctx, corpus)
		if err != nil {
			t.Fatalf("CountUnembeddedLiveSource(%s): %v", corpus, err)
		}
		if n == 0 {
			break
		}
		select {
		case <-deadline.C:
			cancel()
			<-done
			t.Fatalf("timed out draining %s, still %d un-embedded", corpus, n)
		case <-tick.C:
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}
}

// TestSSMSIRoutesThroughFleetEndToEnd is the connector acceptance check: the SSMSI
// departmental base ingests through the real fleet into evidence_chunks under the
// ssmsi corpus, an ingested delinquency passage is retrievable via SearchEvidence,
// and a re-run is idempotent (same rows, nothing re-published).
func TestSSMSIRoutesThroughFleetEndToEnd(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	src := ssmsi.NewSource(ssmsiTestServer(t), []ssmsi.Spec{ssmsi.DepartmentalBase})
	mq := &memQueue{}
	st, err := stats.Run(ctx, slog.New(slog.DiscardHandler), src, store, mq,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("stats.Run: %v", err)
	}
	if st.Upserted != 2 || st.Published != 2 {
		t.Fatalf("producer upserted %d published %d, want 2/2", st.Upserted, st.Published)
	}

	drainToEmpty(ctx, t, store, mq, domain.SSMSIStatCorpus)

	// A verifier query embedded like the Ain homicides passage retrieves it: the
	// live fact-check of an ingested indicator surfaces the ingested passage.
	dps, err := src.Datapoints(ctx)
	if err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	want := stats.RenderFrench(dps[0])
	got, err := store.SearchEvidence(ctx, vectorFor(want), 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != want {
		t.Fatalf("nearest = %+v; want the embedded delinquency passage %q", got, want)
	}

	// Idempotent re-run: the same provenance keys upsert in place and nothing is
	// re-published (every passage is already embedded).
	mq2 := &memQueue{}
	st2, err := stats.Run(ctx, slog.New(slog.DiscardHandler), src, store, mq2,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("re-run stats.Run: %v", err)
	}
	if st2.Upserted != 2 || st2.Published != 0 {
		t.Fatalf("re-run upserted %d published %d, want 2/0 (idempotent, all embedded)", st2.Upserted, st2.Published)
	}
	total, err := store.CountUnembeddedLiveSource(ctx, domain.SSMSIStatCorpus)
	if err != nil {
		t.Fatalf("CountUnembeddedLiveSource: %v", err)
	}
	if total != 0 {
		t.Fatalf("un-embedded after idempotent re-run = %d, want 0", total)
	}
}

// TestODSRoutesThroughFleetEndToEnd proves the OpenDataSoft path ingests an Explore
// API v2.1 dataset into evidence_chunks under a portal corpus and the passage is
// retrievable, so the same fleet serves the DREES/DARES/URSSAF portals.
func TestODSRoutesThroughFleetEndToEnd(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":2,"results":[
		  {"annee":"2020","poste_niveau":1,"poste_code":"p10000","fin_lib":"Tout financeur","montants":1234.5},
		  {"annee":"2021","poste_niveau":1,"poste_code":"p10000","fin_lib":"Tout financeur","montants":1300.0}
		]}`))
	}))
	t.Cleanup(srv.Close)
	portal := ods.DREES
	portal.BaseURL = srv.URL
	src := ods.NewSource(ods.New(ods.Config{HTTPClient: srv.Client()}), portal)

	mq := &memQueue{}
	if _, err := stats.Run(ctx, slog.New(slog.DiscardHandler), src, store, mq,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64}); err != nil {
		t.Fatalf("stats.Run: %v", err)
	}
	drainToEmpty(ctx, t, store, mq, domain.DREESStatCorpus)

	dps, err := src.Datapoints(ctx)
	if err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	want := stats.RenderFrench(dps[0])
	got, err := store.SearchEvidence(ctx, vectorFor(want), 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != want {
		t.Fatalf("nearest = %+v; want the embedded DREES passage %q", got, want)
	}
}
