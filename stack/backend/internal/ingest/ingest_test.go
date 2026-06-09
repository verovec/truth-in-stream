package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeEmbedder struct {
	calls [][]string
	err   error
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.calls = append(f.calls, texts)
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i]))}
	}
	return out, nil
}

type fakeStore struct {
	upserted []domain.Claim
	err      error
}

func (f *fakeStore) Upsert(_ context.Context, claims []domain.Claim) error {
	if f.err != nil {
		return f.err
	}
	f.upserted = append(f.upserted, claims...)
	return nil
}

func sampleSeeds() []SeedClaim {
	return []SeedClaim{
		{ID: "a", Text: "alpha", Verdict: domain.VerdictCorroborates, Sources: []domain.Source{{Title: "A", URL: "https://a"}}},
		{ID: "b", Text: "bravo", Verdict: domain.VerdictContradicts, Sources: []domain.Source{{Title: "B", URL: "https://b"}}},
		{ID: "c", Text: "charlie", Verdict: domain.VerdictUnclear, Sources: []domain.Source{{Title: "C", URL: "https://c"}}},
	}
}

func TestRunEmbedsAndUpsertsInBatches(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{}
	store := &fakeStore{}

	n, err := Run(t.Context(), store, embedder, sampleSeeds(), 2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 3 {
		t.Errorf("upserted count = %d, want 3", n)
	}
	if len(embedder.calls) != 2 {
		t.Fatalf("embed calls = %d, want 2 (batches of 2 and 1)", len(embedder.calls))
	}
	if len(embedder.calls[0]) != 2 || len(embedder.calls[1]) != 1 {
		t.Errorf("batch sizes = %d,%d, want 2,1", len(embedder.calls[0]), len(embedder.calls[1]))
	}
	if len(store.upserted) != 3 {
		t.Fatalf("stored = %d, want 3", len(store.upserted))
	}
	first := store.upserted[0]
	if first.ID != "a" || first.Verdict != domain.VerdictCorroborates || len(first.Embedding) != 1 {
		t.Errorf("first claim = %+v, want embedded claim a", first)
	}
	if len(first.Sources) != 1 || first.Sources[0].URL != "https://a" {
		t.Errorf("sources not mapped: %+v", first.Sources)
	}
}

func TestRunPropagatesEmbedError(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{err: errors.New("provider down")}
	store := &fakeStore{}
	if _, err := Run(t.Context(), store, embedder, sampleSeeds(), 0); err == nil {
		t.Fatal("want embed error, got nil")
	}
	if len(store.upserted) != 0 {
		t.Errorf("nothing should be stored on embed failure, got %d", len(store.upserted))
	}
}

func TestRunPropagatesStoreError(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{}
	store := &fakeStore{err: errors.New("store down")}
	if _, err := Run(t.Context(), store, embedder, sampleSeeds(), 0); err == nil {
		t.Fatal("want store error, got nil")
	}
}

func TestLoadSeedValidates(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{name: "valid", json: `[{"id":"a","text":"t","verdict":"corroborates","sources":[{"title":"T","url":"https://u"}]}]`},
		{name: "empty id", json: `[{"id":"","text":"t","verdict":"corroborates","sources":[{"title":"T","url":"https://u"}]}]`, wantErr: true},
		{name: "empty text", json: `[{"id":"a","text":"","verdict":"corroborates","sources":[{"title":"T","url":"https://u"}]}]`, wantErr: true},
		{name: "bad verdict", json: `[{"id":"a","text":"t","verdict":"maybe","sources":[{"title":"T","url":"https://u"}]}]`, wantErr: true},
		{name: "no sources", json: `[{"id":"a","text":"t","verdict":"unclear","sources":[]}]`, wantErr: true},
		{name: "source missing url", json: `[{"id":"a","text":"t","verdict":"unclear","sources":[{"title":"T","url":""}]}]`, wantErr: true},
		{name: "duplicate id", json: `[{"id":"a","text":"t","verdict":"unclear","sources":[{"title":"T","url":"https://u"}]},{"id":"a","text":"u","verdict":"unclear","sources":[{"title":"T","url":"https://u"}]}]`, wantErr: true},
		{name: "unknown field", json: `[{"id":"a","text":"t","verdict":"unclear","sources":[{"title":"T","url":"https://u"}],"extra":1}]`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadSeed(strings.NewReader(tc.json))
			if tc.wantErr != (err != nil) {
				t.Fatalf("LoadSeed err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestSeedFileIsValid guards the shipped seed dataset: it must parse, hold at
// least 20 claims, and span corroborating and contradicting verdicts.
func TestSeedFileIsValid(t *testing.T) {
	t.Parallel()
	f, err := os.Open(filepath.Join("..", "..", "seed", "claims.json"))
	if err != nil {
		t.Fatalf("open seed file: %v", err)
	}
	defer func() { _ = f.Close() }()

	seeds, err := LoadSeed(f)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if len(seeds) < 20 {
		t.Errorf("seed has %d claims, want at least 20", len(seeds))
	}
	counts := map[domain.Verdict]int{}
	for _, s := range seeds {
		counts[s.Verdict]++
	}
	if counts[domain.VerdictCorroborates] == 0 || counts[domain.VerdictContradicts] == 0 {
		t.Errorf("seed must span corroborates and contradicts, got %v", counts)
	}
}
