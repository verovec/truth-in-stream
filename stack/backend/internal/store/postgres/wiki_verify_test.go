package postgres

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

func TestWikiCorpusHealthOnHealthyCorpus(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(2, 0, "v2"), unitVec(2)),
	})

	h, err := store.EvidenceCorpusHealth(ctx)
	if err != nil {
		t.Fatalf("EvidenceCorpusHealth: %v", err)
	}
	if h.Chunks != 2 {
		t.Errorf("chunks = %d, want 2", h.Chunks)
	}
	if h.NullEmbeddings != 0 || h.ZeroVectors != 0 || h.MissingMetadata != 0 {
		t.Errorf("defect counts = (null %d, zero %d, meta %d), want all 0", h.NullEmbeddings, h.ZeroVectors, h.MissingMetadata)
	}
	if h.EmbeddingType != "halfvec(1024)" {
		t.Errorf("embedding type = %q, want halfvec(1024)", h.EmbeddingType)
	}
	if !h.HNSWPresent || !h.HNSWValid {
		t.Errorf("hnsw present=%t valid=%t, want both true (the migration's index)", h.HNSWPresent, h.HNSWValid)
	}
}

func TestWikiCorpusHealthDetectsNullAndZeroVectors(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	zero := make([]float32, domain.EmbeddingDim) // all-zero embedding: a bug-shaped vector
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		wikiChunk(2, 0, "v2"),                      // never embedded -> null
		withEmbedding(wikiChunk(3, 0, "v0"), zero), // zero vector
	})

	h, err := store.EvidenceCorpusHealth(ctx)
	if err != nil {
		t.Fatalf("EvidenceCorpusHealth: %v", err)
	}
	if h.Chunks != 3 {
		t.Errorf("chunks = %d, want 3", h.Chunks)
	}
	if h.NullEmbeddings != 1 {
		t.Errorf("null embeddings = %d, want 1", h.NullEmbeddings)
	}
	if h.ZeroVectors != 1 {
		t.Errorf("zero vectors = %d, want 1", h.ZeroVectors)
	}
}

func TestWikiCorpusHealthDetectsMissingMetadata(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.EvidenceChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	// Force an invalid kind directly (the store guards writes, so reach past it).
	if _, err := store.pool.Exec(ctx, "UPDATE evidence_chunks SET kind = '' WHERE source = 'simplewiki' AND external_id = '1'"); err != nil {
		t.Fatalf("corrupt kind: %v", err)
	}

	h, err := store.EvidenceCorpusHealth(ctx)
	if err != nil {
		t.Fatalf("EvidenceCorpusHealth: %v", err)
	}
	if h.MissingMetadata != 1 {
		t.Errorf("missing metadata = %d, want 1", h.MissingMetadata)
	}
}

func TestResetWikiCorpusClearsCorpusAndCheckpoint(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.EvidenceChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	// A leftover staging table from an interrupted run must also be cleared.
	if err := store.ResetStaging(ctx, "v1"); err != nil {
		t.Fatalf("ResetStaging: %v", err)
	}
	if !stagingExistsT(t, store) {
		t.Fatal("precondition: staging table should exist")
	}

	if err := store.ResetEvidenceCorpus(ctx); err != nil {
		t.Fatalf("ResetEvidenceCorpus: %v", err)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_chunks"); n != 0 {
		t.Errorf("evidence_chunks has %d rows after reset, want 0", n)
	}
	if n := scalarInt(t, store, "SELECT count(*) FROM evidence_sync_state"); n != 0 {
		t.Errorf("evidence_sync_state has %d rows after reset, want 0 (checkpoint cleared)", n)
	}
	if stagingExistsT(t, store) {
		t.Error("staging table survived the reset")
	}

	// With the checkpoint gone, a fresh bulk would rebuild rather than no-op.
	plan, err := store.StagingPlan(ctx, "any-version")
	if err != nil {
		t.Fatalf("StagingPlan: %v", err)
	}
	if plan == wiki.PlanAlreadyCurrent {
		t.Error("plan after reset is PlanAlreadyCurrent; the reset must force a rebuild")
	}
}
