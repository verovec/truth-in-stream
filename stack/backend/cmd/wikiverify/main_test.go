package main

import (
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// healthyCorpus is a fully rebuilt corpus: every check should pass.
func healthyCorpus() domain.EvidenceCorpusHealth {
	return domain.EvidenceCorpusHealth{
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
		mutate func(*domain.EvidenceCorpusHealth)
	}{
		{"empty corpus", func(h *domain.EvidenceCorpusHealth) { h.Chunks = 0 }},
		{"zero-vector embeddings", func(h *domain.EvidenceCorpusHealth) { h.ZeroVectors = 1 }},
		{"wrong embedding dimension", func(h *domain.EvidenceCorpusHealth) { h.EmbeddingType = "halfvec(512)" }},
		{"missing metadata", func(h *domain.EvidenceCorpusHealth) { h.MissingMetadata = 2 }},
		{"hnsw index absent", func(h *domain.EvidenceCorpusHealth) { h.HNSWPresent = false }},
		{"hnsw index invalid", func(h *domain.EvidenceCorpusHealth) { h.HNSWValid = false }},
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

// TestEvaluatePartiallyEmbeddedPasses is the bulk-into-live property: a corpus
// still filling in (some chunks not yet embedded) is consistent and usable, so
// verification passes - coverage is reported, not gated.
func TestEvaluatePartiallyEmbeddedPasses(t *testing.T) {
	t.Parallel()
	h := healthyCorpus()
	h.NullEmbeddings = 400 // 600/1000 embedded, the fleet is mid-drain
	if !passed(evaluate(h)) {
		t.Fatal("a partially embedded but consistent corpus must pass; 100% is not a gate")
	}
}

func TestReportReturnsErrorOnFailure(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.DiscardHandler)
	if err := report(t.Context(), logger, healthyCorpus()); err != nil {
		t.Errorf("healthy corpus report = %v, want nil", err)
	}
	// A partially embedded corpus is not a failure.
	partial := healthyCorpus()
	partial.NullEmbeddings = 5
	if err := report(t.Context(), logger, partial); err != nil {
		t.Errorf("partially embedded corpus report = %v, want nil (coverage is progress)", err)
	}
	// A real consistency defect still fails.
	bad := healthyCorpus()
	bad.ZeroVectors = 1
	if err := report(t.Context(), logger, bad); err == nil {
		t.Error("a corpus with a zero-vector embedding must make report return an error (non-zero exit)")
	}
}
