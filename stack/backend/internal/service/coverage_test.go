package service

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestCoverageRetrieverTopSimilarity(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	searcher := &fakeSearcher{hits: []domain.ClaimMatch{
		{ID: "a", Distance: 0.2},
		{ID: "b", Distance: 0.6},
	}}
	r := NewCoverageRetriever(embedder, searcher)

	score, found, err := r.TopSimilarity(t.Context(), "the earth orbits the sun")
	if err != nil {
		t.Fatalf("TopSimilarity: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	// Best hit is the nearest (smallest distance): score = 1 - 0.2.
	if diff := cmp.Diff(0.8, score, scoreApprox); diff != "" {
		t.Errorf("score mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"the earth orbits the sun"}, embedder.gotTexts); diff != "" {
		t.Errorf("embedded texts mismatch (-want +got):\n%s", diff)
	}
}

func TestCoverageRetrieverEmptyCorpus(t *testing.T) {
	t.Parallel()
	r := NewCoverageRetriever(&fakeEmbedder{vecs: [][]float32{queryVec()}}, &fakeSearcher{hits: nil})
	_, found, err := r.TopSimilarity(t.Context(), "an obscure claim")
	if err != nil {
		t.Fatalf("TopSimilarity: %v", err)
	}
	if found {
		t.Error("found = true on empty corpus, want false")
	}
}

func TestCoverageRetrieverBlankText(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{vecs: [][]float32{queryVec()}}
	r := NewCoverageRetriever(embedder, &fakeSearcher{})
	_, found, err := r.TopSimilarity(t.Context(), "   ")
	if err != nil {
		t.Fatalf("TopSimilarity: %v", err)
	}
	if found {
		t.Error("found = true for blank text, want false")
	}
	if embedder.gotTexts != nil {
		t.Errorf("blank text was embedded (%v); it must short-circuit", embedder.gotTexts)
	}
}

func TestCoverageRetrieverEmbedError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("voyage down")
	r := NewCoverageRetriever(&fakeEmbedder{err: sentinel}, &fakeSearcher{})
	if _, _, err := r.TopSimilarity(t.Context(), "a claim"); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping %v", err, sentinel)
	}
}

func TestCoverageRetrieverDimMismatch(t *testing.T) {
	t.Parallel()
	r := NewCoverageRetriever(&fakeEmbedder{vecs: [][]float32{{1, 2, 3}}}, &fakeSearcher{})
	if _, _, err := r.TopSimilarity(t.Context(), "a claim"); err == nil {
		t.Fatal("err = nil for wrong embedding dimension, want error")
	}
}
