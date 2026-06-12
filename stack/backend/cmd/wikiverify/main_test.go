package main

import (
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// healthyCorpus is a fully rebuilt corpus: every check should pass.
func healthyCorpus() domain.WikiCorpusHealth {
	return domain.WikiCorpusHealth{
		Chunks:          1000,
		NullEmbeddings:  0,
		ZeroVectors:     0,
		MissingMetadata: 0,
		EmbeddingType:   "halfvec(1024)",
		HNSWPresent:     true,
		HNSWValid:       true,
	}
}

func TestEvaluatePassesHealthyCorpus(t *testing.T) {
	t.Parallel()
	if !passed(evaluate(healthyCorpus())) {
		t.Fatal("a fully rebuilt corpus must pass every check")
	}
}

func TestEvaluateFailsOnEachDefect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*domain.WikiCorpusHealth)
	}{
		{"empty corpus", func(h *domain.WikiCorpusHealth) { h.Chunks = 0 }},
		{"unembedded chunks", func(h *domain.WikiCorpusHealth) { h.NullEmbeddings = 3 }},
		{"zero-vector embeddings", func(h *domain.WikiCorpusHealth) { h.ZeroVectors = 1 }},
		{"wrong embedding dimension", func(h *domain.WikiCorpusHealth) { h.EmbeddingType = "halfvec(512)" }},
		{"missing metadata", func(h *domain.WikiCorpusHealth) { h.MissingMetadata = 2 }},
		{"hnsw index absent", func(h *domain.WikiCorpusHealth) { h.HNSWPresent = false }},
		{"hnsw index invalid", func(h *domain.WikiCorpusHealth) { h.HNSWValid = false }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := healthyCorpus()
			tc.mutate(&h)
			if passed(evaluate(h)) {
				t.Errorf("%s must fail verification", tc.name)
			}
		})
	}
}

func TestReportReturnsErrorOnFailure(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	if err := report(t.Context(), logger, healthyCorpus()); err != nil {
		t.Errorf("healthy corpus report = %v, want nil", err)
	}
	bad := healthyCorpus()
	bad.NullEmbeddings = 5
	if err := report(t.Context(), logger, bad); err == nil {
		t.Error("a corpus with unembedded chunks must make report return an error (non-zero exit)")
	}
}
