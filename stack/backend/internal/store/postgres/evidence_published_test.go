package postgres

import (
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TestPublishedAtRoundTrip proves the typed publication date survives the whole
// storage path: the embedded upsert writes it, a re-upsert keeps it in sync
// with what the source last said, and the search hit surfaces it - nil staying
// nil for undated sources.
func TestPublishedAtRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	published := time.Date(2025, 7, 17, 0, 0, 0, 0, time.UTC)
	vec := make([]float32, domain.EmbeddingDim)
	vec[0] = 1

	dated := domain.EvidenceChunk{
		Source: "an-questions", ExternalID: "q-1", ChunkIndex: 0,
		Title: "Question au gouvernement", URL: "https://x/q1",
		Content: "Le ministre confirme 7,3% de chomage fin 2024.",
		Kind:    domain.EvidenceKindLead, Embedding: vec, PublishedAt: &published,
	}
	undatedVec := make([]float32, domain.EmbeddingDim)
	undatedVec[1] = 1
	undated := domain.EvidenceChunk{
		Source: "wikipedia", ExternalID: "p-1", ChunkIndex: 0,
		Title: "Chomage en France", URL: "https://x/p1",
		Content: "Le chomage est un indicateur economique.",
		Kind:    domain.EvidenceKindLead, Embedding: undatedVec,
	}
	if err := store.UpsertEmbeddedChunk(ctx, dated); err != nil {
		t.Fatalf("UpsertEmbeddedChunk(dated): %v", err)
	}
	if err := store.UpsertEmbeddedChunk(ctx, undated); err != nil {
		t.Fatalf("UpsertEmbeddedChunk(undated): %v", err)
	}

	hits, err := store.SearchEvidence(ctx, vec, 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hit for the dated chunk")
	}
	if hits[0].PublishedAt == nil || !hits[0].PublishedAt.Equal(published) {
		t.Errorf("dated hit PublishedAt = %v, want %v", hits[0].PublishedAt, published)
	}

	hits, err = store.SearchEvidence(ctx, undatedVec, 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence(undated): %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hit for the undated chunk")
	}
	if hits[0].PublishedAt != nil {
		t.Errorf("undated hit PublishedAt = %v, want nil", hits[0].PublishedAt)
	}

	// A re-sync that no longer carries a date clears the stale one: the source
	// stays authoritative in both directions.
	dated.PublishedAt = nil
	if err := store.UpsertEmbeddedChunk(ctx, dated); err != nil {
		t.Fatalf("UpsertEmbeddedChunk(cleared): %v", err)
	}
	hits, err = store.SearchEvidence(ctx, vec, 1, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence(cleared): %v", err)
	}
	if len(hits) == 0 || hits[0].PublishedAt != nil {
		t.Errorf("cleared hit PublishedAt = %v, want nil after a dateless re-sync", hits[0].PublishedAt)
	}
}
