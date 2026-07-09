package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// embedded passages become retrievable via SearchEvidence. It needs no broker - an
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
	rem, err := store.CountUnembeddedLiveSource(ctx, domain.StatCorpus)
	if err != nil {
		t.Fatalf("CountUnembeddedLiveSource: %v", err)
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
	got, err := store.SearchEvidence(ctx, vectorFor(want), 1)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != want {
		t.Fatalf("nearest = %+v; want the embedded 2022 passage %q", got, want)
	}
}

// tokenEmbedder maps text to an L2-normalized bag-of-tokens vector so two
// passages that share words have graded (not orthogonal) cosine similarity,
// unlike orthogonalEmbedder. The retrieval-floor test needs this: macro passages
// must plausibly compete with a labor query in the same ANN search, so a trivial
// one-hot embedding would not prove the floor holds.
type tokenEmbedder struct{}

func (tokenEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = tokenVector(text)
	}
	return out, nil
}

func tokenVector(text string) []float32 {
	v := make([]float32, domain.EmbeddingDim)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[h.Sum32()%domain.EmbeddingDim]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(norm))
	for j := range v {
		v[j] *= inv
	}
	return v
}

// laborSeries yields a France employment-rate-of-immigrants observation under
// the INSEE labor corpus - the immigration-employment passage the floor test
// must keep retrievable when macro corpora are present.
func laborSeries() domain.Datapoint {
	return domain.Datapoint{
		SourceName: "Insee",
		SourceURL:  "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/010755676",
		Dataset:    "EEC",
		SeriesKey:  "010755676",
		Title:      "Taux d'emploi des immigrés",
		Geography:  "France",
		Dimensions: []string{"immigrés", "15 à 64 ans"},
		Period:     "2023",
		Figure:     59.8,
		Unit:       "%",
	}
}

// macroFiller yields n distinct macro datapoints (GDP, prices, salaried
// employment) under the INSEE macro corpora, so the floor test can flood the
// index with macro passages and prove the labor passage still surfaces.
func macroFiller(n int) []domain.Datapoint {
	titles := []string{
		"Produit intérieur brut en volume",
		"Indice des prix à la consommation",
		"Emploi salarié dans la construction",
		"Investissement des entreprises",
		"Dépense de consommation des ménages",
	}
	out := make([]domain.Datapoint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.Datapoint{
			SourceName: "Insee",
			SourceURL:  "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/macro",
			Dataset:    "CNT-2020-PIB-EQB-RF",
			SeriesKey:  fmt.Sprintf("macro-%d", i),
			Title:      titles[i%len(titles)],
			Geography:  "France",
			Period:     "2023",
			Figure:     float64(100 + i),
			Unit:       "indice",
		})
	}
	return out
}

func waitUnembedded(ctx context.Context, t *testing.T, store *postgres.Store, want int64) {
	t.Helper()
	waitUnembeddedCorpus(ctx, t, store, domain.StatCorpus, want)
}

func waitUnembeddedCorpus(ctx context.Context, t *testing.T, store *postgres.Store, corpus string, want int64) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		n, err := store.CountUnembeddedLiveSource(ctx, corpus)
		if err != nil {
			t.Fatalf("CountUnembeddedLiveSource(%s): %v", corpus, err)
		}
		if n == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d un-embedded chunks in %s, still %d", want, corpus, n)
		case <-tick.C:
		}
	}
}

// runFleet drains the in-memory queue through a real embedworker until every
// listed corpus is fully embedded, then stops the worker.
func runFleet(ctx context.Context, t *testing.T, store *postgres.Store, mq *memQueue, emb embedjob.Embedder, corpora ...string) {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	worker := embedjob.NewWorker(emb, store, mq, mq,
		slog.New(slog.DiscardHandler), embedjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}})
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()
	for _, c := range corpora {
		waitUnembeddedCorpus(ctx, t, store, c, 0)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}
}

// inseeDataflowSource is a fixed stand-in for insee.NewDataflowSource that yields
// the discovered member datapoints under the unemployment theme corpus, so the
// e2e drives the real producer/fleet without a network call.
type inseeDataflowSource struct{ dps []domain.Datapoint }

func (s inseeDataflowSource) Datapoints(context.Context) ([]domain.Datapoint, error) {
	return s.dps, nil
}
func (inseeDataflowSource) Corpus() string { return domain.INSEEUnemploymentCorpus }

func chomageQuarterly() []domain.Datapoint {
	base := domain.Datapoint{
		SourceName: "Insee",
		SourceURL:  "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM/001688526",
		Dataset:    "CHOMAGE-TRIM-NATIONAL",
		SeriesKey:  "001688526",
		Title:      "Taux de chômage au sens du BIT",
		Geography:  "France métropolitaine",
		Dimensions: []string{"ensemble", "15 ans ou plus"},
		Unit:       "%",
	}
	a, b := base, base
	a.Period, a.Figure = "2024-Q1", 7.5
	b.Period, b.Figure = "2024-Q2", 7.3
	return []domain.Datapoint{a, b}
}

// TestINSEEDataflowRoutesThroughFleetEndToEnd is the INSEE-source acceptance
// check: the discovered quarterly unemployment passages upsert un-embedded under
// the unemployment theme corpus, the fleet drains and embeds them, and a passage
// is retrievable via SearchEvidence. Re-running the ingest does not duplicate
// passages - the (IDBANK, period) provenance key upserts in place.
func TestINSEEDataflowRoutesThroughFleetEndToEnd(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	dps := chomageQuarterly()
	src := inseeDataflowSource{dps: dps}

	mq := &memQueue{}
	st, err := stats.Run(ctx, slog.New(slog.DiscardHandler), src, store, mq,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("stats.Run: %v", err)
	}
	if st.Upserted != 2 || st.Published != 2 {
		t.Fatalf("first run upserted %d published %d, want 2/2", st.Upserted, st.Published)
	}
	runFleet(ctx, t, store, mq, orthogonalEmbedder{}, domain.INSEEUnemploymentCorpus)

	want := stats.RenderFrench(dps[0])
	got, err := store.SearchEvidence(ctx, vectorFor(want), 1)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Content != want {
		t.Fatalf("nearest = %+v; want the embedded Q1 passage %q", got, want)
	}

	// Idempotent re-run: the same provenance keys upsert in place, the row count
	// is unchanged, and nothing is re-published (every passage is already embedded).
	mq2 := &memQueue{}
	st2, err := stats.Run(ctx, slog.New(slog.DiscardHandler), src, store, mq2,
		stats.Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("re-run stats.Run: %v", err)
	}
	if st2.Upserted != 2 {
		t.Fatalf("re-run upserted %d, want 2 (same rows)", st2.Upserted)
	}
	if st2.Published != 0 {
		t.Fatalf("re-run published %d, want 0 (all already embedded)", st2.Published)
	}
	total, err := store.CountUnembeddedLiveSource(ctx, domain.INSEEUnemploymentCorpus)
	if err != nil {
		t.Fatalf("CountUnembeddedLiveSource: %v", err)
	}
	if total != 0 {
		t.Fatalf("un-embedded after idempotent re-run = %d, want 0 (no new rows)", total)
	}
}

// TestLaborPassageSurvivesMacroCorpora is the retrieval-floor acceptance check:
// with a labor (immigration-employment) passage and a flood of macro passages
// all embedded into the shared index, a labor query still retrieves the labor
// passage as its top hit. Macro corpora must not crowd out immigration-employment
// retrieval. It uses a graded bag-of-tokens embedding so the macro passages
// genuinely compete in the ANN search rather than being trivially orthogonal.
func TestLaborPassageSurvivesMacroCorpora(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	labor := laborSeries()
	mq := &memQueue{}

	// Labor passage under the INSEE labor corpus.
	if _, err := stats.Run(ctx, slog.New(slog.DiscardHandler),
		statCorpusSource{corpus: domain.INSEEStatCorpus, dps: []domain.Datapoint{labor}},
		store, mq, stats.Config{MaxPriority: 10, EnqueueBatchSize: 64}); err != nil {
		t.Fatalf("labor run: %v", err)
	}
	// A flood of macro passages under the GDP theme corpus.
	macro := macroFiller(200)
	if _, err := stats.Run(ctx, slog.New(slog.DiscardHandler),
		statCorpusSource{corpus: domain.INSEEGDPCorpus, dps: macro},
		store, mq, stats.Config{MaxPriority: 10, EnqueueBatchSize: 64}); err != nil {
		t.Fatalf("macro run: %v", err)
	}

	runFleet(ctx, t, store, mq, tokenEmbedder{}, domain.INSEEStatCorpus, domain.INSEEGDPCorpus)

	// Query the way a verifier would: embed a labor-themed question and confirm
	// the labor passage is the nearest neighbor despite 200 macro passages.
	query := tokenVector("taux d'emploi des immigrés en France")
	got, err := store.SearchEvidence(ctx, query, 3)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no evidence returned for the labor query")
	}
	wantLabor := stats.RenderFrench(labor)
	if got[0].Content != wantLabor {
		t.Fatalf("top hit is not the labor passage despite macro corpora:\n top = %q\nwant = %q", got[0].Content, wantLabor)
	}
}

// statCorpusSource yields fixed datapoints under an arbitrary statistical corpus
// so a floor test can populate distinct corpora through the real foundation.
type statCorpusSource struct {
	corpus string
	dps    []domain.Datapoint
}

func (s statCorpusSource) Datapoints(context.Context) ([]domain.Datapoint, error) {
	return s.dps, nil
}
func (s statCorpusSource) Corpus() string { return s.corpus }
