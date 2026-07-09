package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestNewWikiSearchRejectsBadConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  WikiSearchConfig
	}{
		{name: "zero topK", cfg: WikiSearchConfig{TopK: 0, Timeout: time.Second}},
		{name: "negative topK", cfg: WikiSearchConfig{TopK: -1, Timeout: time.Second}},
		{name: "zero timeout", cfg: WikiSearchConfig{TopK: 5, Timeout: 0}},
		{name: "negative timeout", cfg: WikiSearchConfig{TopK: 5, Timeout: -time.Second}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewWikiSearch(&fakeEmbedder{}, &fakeEvidence{}, tc.cfg); err == nil {
				t.Fatalf("NewWikiSearch(%+v) = nil error, want error", tc.cfg)
			}
		})
	}
}

func TestWikiSearchReturnsNeighboursWithSimilarity(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	evidence := &fakeEvidence{hits: []domain.EvidenceHit{
		{Title: "Red fox", URL: "https://en.wikipedia.org/wiki/Red_fox", Content: "foxes are fast", Distance: 0.1},
		{Title: "Dog", URL: "https://en.wikipedia.org/wiki/Dog", Content: "dogs are loyal", Distance: 0.4},
	}}
	search, err := NewWikiSearch(embedder, evidence, WikiSearchConfig{TopK: 3, Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewWikiSearch: %v", err)
	}

	got, err := search.Search(t.Context(), "tell me about foxes")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := []WikiHit{
		{Title: "Red fox", URL: "https://en.wikipedia.org/wiki/Red_fox", Content: "foxes are fast", Similarity: 0.9},
		{Title: "Dog", URL: "https://en.wikipedia.org/wiki/Dog", Content: "dogs are loyal", Similarity: 0.6},
	}
	if diff := cmp.Diff(want, got, scoreApprox); diff != "" {
		t.Errorf("hits mismatch (-want +got):\n%s", diff)
	}
	if evidence.gotTopK != 3 {
		t.Errorf("evidence topK = %d, want 3", evidence.gotTopK)
	}
	if len(embedder.gotTexts) != 1 || embedder.gotTexts[0] != "tell me about foxes" {
		t.Errorf("embedded texts = %v, want the raw query", embedder.gotTexts)
	}
}

func TestWikiSearchBlankQuerySkipsEmbedding(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	evidence := &fakeEvidence{}
	search, err := NewWikiSearch(embedder, evidence, WikiSearchConfig{TopK: 3, Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewWikiSearch: %v", err)
	}

	got, err := search.Search(t.Context(), "   ")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got == nil {
		t.Fatal("blank query returned nil slice, want empty non-nil")
	}
	if len(got) != 0 {
		t.Errorf("blank query hits = %v, want empty", got)
	}
	if embedder.gotTexts != nil {
		t.Errorf("blank query embedded %v, want no embedding call", embedder.gotTexts)
	}
}

func TestWikiSearchPropagatesEmbedDimMismatch(t *testing.T) {
	t.Parallel()
	// A vector of the wrong width means the embedder disagrees with the store;
	// embedQuery is the single home of that invariant and must surface it.
	embedder := &fakeEmbedder{vecs: [][]float32{{1, 2, 3}}}
	search, err := NewWikiSearch(embedder, &fakeEvidence{}, WikiSearchConfig{TopK: 3, Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewWikiSearch: %v", err)
	}
	if _, err := search.Search(t.Context(), "foxes"); err == nil {
		t.Fatal("Search with wrong-dim embedding = nil error, want error")
	}
}

func TestWikiSearchWrapsSearchError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("corpus offline")
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	search, err := NewWikiSearch(embedder, &fakeEvidence{err: wantErr}, WikiSearchConfig{TopK: 3, Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewWikiSearch: %v", err)
	}
	_, err = search.Search(t.Context(), "foxes")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Search error = %v, want it to wrap %v", err, wantErr)
	}
}
