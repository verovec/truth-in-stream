package stats

import (
	"context"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
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

// fakeEmbedder returns a deterministic 1024-dim vector per text, so re-running
// with the same text yields the same vector (idempotency of stored embeddings).
type fakeEmbedder struct {
	calls int
	err   error
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls += len(texts)
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		v := make([]float32, domain.EmbeddingDim)
		var seed float32
		for _, r := range txt {
			seed += float32(r)
		}
		for j := range v {
			v[j] = seed + float32(j)
		}
		out[i] = v
	}
	return out, nil
}

// fakeStore records every upsert so the test can assert idempotency by stable
// (page_id, chunk_index) key.
type fakeStore struct {
	rows map[[2]int64]domain.WikiChunk
	err  error
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[[2]int64]domain.WikiChunk{}} }

func (f *fakeStore) UpsertEmbeddedChunk(_ context.Context, c domain.WikiChunk) error {
	if f.err != nil {
		return f.err
	}
	f.rows[[2]int64{c.PageID, int64(c.ChunkIndex)}] = c
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

func TestRunStoresRenderedPassages(t *testing.T) {
	store := newFakeStore()
	emb := &fakeEmbedder{}
	n, err := Run(context.Background(), fakeSource{dps: twoPermits()}, emb, store, 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 2 {
		t.Fatalf("Run wrote %d, want 2", n)
	}
	if len(store.rows) != 2 {
		t.Fatalf("store has %d rows, want 2", len(store.rows))
	}
	for _, row := range store.rows {
		if len(row.Embedding) != domain.EmbeddingDim {
			t.Errorf("row missing embedding: %d dims", len(row.Embedding))
		}
		if row.Content == "" || row.URL == "" || row.Title == "" {
			t.Errorf("row missing provenance fields: %+v", row)
		}
		if !row.Kind.Valid() {
			t.Errorf("row kind %q invalid", row.Kind)
		}
		if row.Content[:len("Premiers")] != "Premiers" {
			t.Errorf("row content not the rendered passage: %q", row.Content)
		}
	}
}

func TestRunIdempotentOnProvenanceKey(t *testing.T) {
	store := newFakeStore()
	dps := twoPermits()

	if _, err := Run(context.Background(), fakeSource{dps: dps}, &fakeEmbedder{}, store, 0); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := make(map[[2]int64]string, len(store.rows))
	for k, v := range store.rows {
		first[k] = v.Content
	}

	// Re-run identical data: must overwrite the same keys, not add new rows.
	if _, err := Run(context.Background(), fakeSource{dps: dps}, &fakeEmbedder{}, store, 0); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(store.rows) != len(first) {
		t.Fatalf("re-run changed row count: %d != %d (duplicate passages)", len(store.rows), len(first))
	}
	for k, content := range first {
		if store.rows[k].Content != content {
			t.Errorf("key %v content changed on re-run", k)
		}
	}
}

func TestRunRejectsInvalidDatapoint(t *testing.T) {
	bad := twoPermits()
	bad[1].Unit = "" // invalid
	_, err := Run(context.Background(), fakeSource{dps: bad}, &fakeEmbedder{}, newFakeStore(), 0)
	if err == nil {
		t.Fatal("Run accepted an invalid datapoint")
	}
}

func TestRunWrapsSourceError(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := Run(context.Background(), fakeSource{err: sentinel}, &fakeEmbedder{}, newFakeStore(), 0)
	if !errors.Is(err, sentinel) {
		t.Errorf("Run error = %v, want wrap of %v", err, sentinel)
	}
}

func TestRunWrapsEmbedError(t *testing.T) {
	sentinel := errors.New("embed down")
	_, err := Run(context.Background(), fakeSource{dps: twoPermits()}, &fakeEmbedder{err: sentinel}, newFakeStore(), 0)
	if !errors.Is(err, sentinel) {
		t.Errorf("Run error = %v, want wrap of %v", err, sentinel)
	}
}

func TestRunEmptySource(t *testing.T) {
	n, err := Run(context.Background(), fakeSource{}, &fakeEmbedder{}, newFakeStore(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 0 {
		t.Errorf("Run wrote %d for empty source, want 0", n)
	}
}
