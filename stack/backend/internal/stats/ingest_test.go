package stats

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
)

// fakeSource yields fixed datapoints (or an error) so the foundation is tested
// without a network call.
type fakeSource struct {
	dps []domain.Datapoint
	err error
}

func (f fakeSource) Datapoints(context.Context) ([]domain.Datapoint, error) {
	return f.dps, f.err
}

func (fakeSource) Corpus() string { return domain.StatCorpus }

// fakeStore models the live wiki_chunks table for one corpus: it records every
// upserted chunk on its (page_id, chunk_index) provenance key and serves back the
// still-unembedded ones, so a test can assert idempotency and that the producer
// enqueues only un-embedded rows. embedded marks the keys whose vector the fleet
// has filled, simulating a prior run's embedding for the idempotent-re-run case.
type fakeStore struct {
	rows     map[[2]int64]domain.WikiChunk
	embedded map[[2]int64]bool

	upsertErr error
	countErr  error
	readErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[[2]int64]domain.WikiChunk{}, embedded: map[[2]int64]bool{}}
}

func key(c domain.WikiChunk) [2]int64 { return [2]int64{c.PageID, int64(c.ChunkIndex)} }

func (f *fakeStore) UpsertChunks(_ context.Context, chunks []domain.WikiChunk) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	for _, c := range chunks {
		k := key(c)
		// Mirror UpsertWikiChunk: an unchanged re-upsert keeps the existing vector;
		// a content change clears it so the fleet re-embeds.
		if prev, ok := f.rows[k]; ok && prev.Content != c.Content {
			f.embedded[k] = false
		}
		f.rows[k] = c
	}
	return nil
}

func (f *fakeStore) CountUnembeddedLiveCorpus(_ context.Context, corpus string) (int64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	var n int64
	for k, c := range f.rows {
		if c.Corpus == corpus && !f.embedded[k] {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) UnembeddedLiveCorpus(_ context.Context, corpus string, after domain.WikiCursor, limit int) ([]domain.WikiChunk, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	var out []domain.WikiChunk
	for k, c := range f.rows {
		if c.Corpus != corpus || f.embedded[k] {
			continue
		}
		if k[0] < after.PageID || (k[0] == after.PageID && int32(k[1]) <= after.ChunkIndex) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PageID != out[j].PageID {
			return out[i].PageID < out[j].PageID
		}
		return out[i].ChunkIndex < out[j].ChunkIndex
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// markEmbedded simulates the fleet filling the vectors for every currently
// un-embedded row, so a follow-up Run sees nothing left to publish.
func (f *fakeStore) markEmbedded() {
	for k := range f.rows {
		f.embedded[k] = true
	}
}

// fakePublisher records every published embedding-job body so a test can assert
// one valid job per un-embedded chunk and inspect their identity.
type fakePublisher struct {
	mu   sync.Mutex
	jobs []embedjob.Job
	err  error
}

func (p *fakePublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	var j embedjob.Job
	if err := json.Unmarshal(body, &j); err != nil {
		return err
	}
	p.jobs = append(p.jobs, j)
	return nil
}

func twoPermits() []domain.Datapoint {
	base := domain.Datapoint{
		SourceName: "Eurostat",
		SourceURL:  "https://e/migr",
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

func runStats(t *testing.T, src Source, store Store, pub Publisher) Stats {
	t.Helper()
	got, err := Run(context.Background(), nil, src, store, pub, Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return got
}

// reusable jobValidate exercises embedjob's own validation via a published body,
// so the test asserts the exact contract the worker enforces.
func assertValidJob(t *testing.T, j embedjob.Job) {
	t.Helper()
	body, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	// Round-trip through the worker's decode path: a Worker.Process on a fake
	// embedder would reject an invalid job, but the cheapest equivalent is to
	// assert the fields the validator checks directly.
	if j.PageID <= 0 {
		t.Errorf("job page id %d must be positive (body %s)", j.PageID, body)
	}
	if j.ChunkIndex < 0 {
		t.Errorf("job chunk index %d must not be negative", j.ChunkIndex)
	}
	if j.Content == "" {
		t.Errorf("job page %d chunk %d has empty content", j.PageID, j.ChunkIndex)
	}
	if j.Staging {
		t.Errorf("stats job must publish to live, not staging: %+v", j)
	}
}

func TestRunUpsertsUnembeddedAndPublishesOneJobPerChunk(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}

	got := runStats(t, fakeSource{dps: twoPermits()}, store, pub)

	if got.Upserted != 2 {
		t.Fatalf("upserted = %d, want 2", got.Upserted)
	}
	if got.Published != 2 {
		t.Fatalf("published = %d, want 2", got.Published)
	}
	if len(store.rows) != 2 {
		t.Fatalf("store has %d rows, want 2", len(store.rows))
	}
	for _, row := range store.rows {
		if len(row.Embedding) != 0 {
			t.Errorf("stats path must upsert un-embedded, got a %d-dim vector", len(row.Embedding))
		}
		if row.Content == "" || row.URL == "" || row.Title == "" {
			t.Errorf("row missing provenance fields: %+v", row)
		}
		if !row.Kind.Valid() {
			t.Errorf("row kind %q invalid", row.Kind)
		}
	}
	if len(pub.jobs) != 2 {
		t.Fatalf("published %d jobs, want one per chunk (2)", len(pub.jobs))
	}
	for _, j := range pub.jobs {
		assertValidJob(t, j)
		row, ok := store.rows[[2]int64{j.PageID, int64(j.ChunkIndex)}]
		if !ok {
			t.Errorf("published job (%d,%d) has no matching upserted row", j.PageID, j.ChunkIndex)
			continue
		}
		if j.Content != row.Content {
			t.Errorf("job content %q != row content %q", j.Content, row.Content)
		}
	}
}

func TestRunIdempotentReRunPublishesNothingOnceEmbedded(t *testing.T) {
	store := newFakeStore()
	dps := twoPermits()

	first := runStats(t, fakeSource{dps: dps}, store, &fakePublisher{})
	if first.Published != 2 {
		t.Fatalf("first run published = %d, want 2", first.Published)
	}

	// The fleet fills the vectors, then an identical refresh re-runs: the upsert
	// keeps the same rows and the producer enqueues nothing already embedded.
	store.markEmbedded()
	pub := &fakePublisher{}
	second := runStats(t, fakeSource{dps: dps}, store, pub)

	if len(store.rows) != 2 {
		t.Fatalf("re-run changed row count: %d, want 2 (duplicate passages)", len(store.rows))
	}
	if second.Published != 0 {
		t.Fatalf("re-run published %d already-embedded jobs, want 0", second.Published)
	}
	if len(pub.jobs) != 0 {
		t.Fatalf("re-run enqueued %d jobs, want 0", len(pub.jobs))
	}
}

func TestRunRePublishesOnlyChangedRows(t *testing.T) {
	store := newFakeStore()
	dps := twoPermits()
	if _, err := Run(context.Background(), nil, fakeSource{dps: dps}, store, &fakePublisher{}, Config{MaxPriority: 10, EnqueueBatchSize: 64}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	store.markEmbedded()

	// A revised figure for one period changes its content; the upsert clears that
	// row's vector, so the producer re-publishes exactly that one.
	revised := twoPermits()
	revised[1].Figure = 999999
	pub := &fakePublisher{}
	got := runStats(t, fakeSource{dps: revised}, store, pub)

	if got.Published != 1 {
		t.Fatalf("published = %d, want 1 (only the changed row)", got.Published)
	}
	if len(pub.jobs) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(pub.jobs))
	}
}

func TestRunDeduplicatesSameProvenanceKey(t *testing.T) {
	// A source that lists the same series+period twice maps both onto one
	// provenance key; the run must write one row and count it once, not two.
	dps := twoPermits()
	dup := dps[1]
	dup.Figure = 999999 // last occurrence wins
	src := fakeSource{dps: []domain.Datapoint{dps[0], dps[1], dup}}

	store := newFakeStore()
	pub := &fakePublisher{}
	got := runStats(t, src, store, pub)

	if got.Upserted != 2 {
		t.Fatalf("upserted = %d, want 2 (the duplicate collapsed)", got.Upserted)
	}
	if len(store.rows) != 2 {
		t.Fatalf("store has %d rows, want 2", len(store.rows))
	}
	if got.Published != 2 || len(pub.jobs) != 2 {
		t.Fatalf("published = %d (jobs %d), want 2", got.Published, len(pub.jobs))
	}
}

func TestRunStampsSourceCorpus(t *testing.T) {
	store := newFakeStore()
	src := corpusSource{corpus: domain.InteriorStatCorpus, dps: twoPermits()}
	runStats(t, src, store, &fakePublisher{})

	for _, row := range store.rows {
		if row.Corpus != domain.InteriorStatCorpus {
			t.Errorf("row corpus = %q, want %q", row.Corpus, domain.InteriorStatCorpus)
		}
	}
}

func TestRunRejectsUnregisteredCorpus(t *testing.T) {
	src := corpusSource{corpus: "simplewiki", dps: twoPermits()}
	_, err := Run(context.Background(), nil, src, newFakeStore(), &fakePublisher{}, Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err == nil {
		t.Fatal("Run accepted a non-statistical corpus label")
	}
}

func TestRunRejectsInvalidDatapoint(t *testing.T) {
	bad := twoPermits()
	bad[1].Unit = ""
	_, err := Run(context.Background(), nil, fakeSource{dps: bad}, newFakeStore(), &fakePublisher{}, Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if err == nil {
		t.Fatal("Run accepted an invalid datapoint")
	}
}

func TestRunWrapsSourceError(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := Run(context.Background(), nil, fakeSource{err: sentinel}, newFakeStore(), &fakePublisher{}, Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if !errors.Is(err, sentinel) {
		t.Errorf("Run error = %v, want wrap of %v", err, sentinel)
	}
}

func TestRunWrapsUpsertError(t *testing.T) {
	sentinel := errors.New("db down")
	store := newFakeStore()
	store.upsertErr = sentinel
	_, err := Run(context.Background(), nil, fakeSource{dps: twoPermits()}, store, &fakePublisher{}, Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if !errors.Is(err, sentinel) {
		t.Errorf("Run error = %v, want wrap of %v", err, sentinel)
	}
}

func TestRunWrapsPublishError(t *testing.T) {
	sentinel := errors.New("broker down")
	_, err := Run(context.Background(), nil, fakeSource{dps: twoPermits()}, newFakeStore(), &fakePublisher{err: sentinel}, Config{MaxPriority: 10, EnqueueBatchSize: 64})
	if !errors.Is(err, sentinel) {
		t.Errorf("Run error = %v, want wrap of %v", err, sentinel)
	}
}

func TestRunEmptySource(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	got := runStats(t, fakeSource{}, store, pub)
	if got.Upserted != 0 || got.Published != 0 {
		t.Errorf("empty source: upserted %d published %d, want 0/0", got.Upserted, got.Published)
	}
	if len(pub.jobs) != 0 {
		t.Errorf("empty source enqueued %d jobs, want 0", len(pub.jobs))
	}
}

// corpusSource yields fixed datapoints under a chosen corpus label, so a test
// can assert Run stamps the source's corpus and guards an unregistered one.
type corpusSource struct {
	corpus string
	dps    []domain.Datapoint
}

func (c corpusSource) Datapoints(context.Context) ([]domain.Datapoint, error) {
	return c.dps, nil
}
func (c corpusSource) Corpus() string { return c.corpus }
