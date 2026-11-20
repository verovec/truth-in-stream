package postgres

import (
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TestRetentionDryRunThenSweep proves the retention sweep's contract: the dry-run
// count reports exactly what a real sweep removes, the real sweep removes it, a
// cutoff before every row removes nothing, and re-ingesting restores the corpus.
func TestRetentionDryRunThenSweep(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.EvidenceChunk{
		embeddedEvidence("insee-emploi", "series-1", unitVec(0)),
		embeddedEvidence("insee-emploi", "series-2", unitVec(1)),
		embeddedEvidence("insee-emploi", "series-3", unitVec(2)),
	}
	for _, c := range chunks {
		if err := store.UpsertEmbeddedChunk(ctx, c); err != nil {
			t.Fatalf("seed %s: %v", c.ExternalID, err)
		}
	}

	future := time.Now().Add(time.Hour) // every row was synced before this
	past := time.Now().Add(-time.Hour)  // every row was synced after this

	// A cutoff before every row removes nothing.
	if n, err := store.CountEvidenceOlderThan(ctx, "insee-emploi", past); err != nil || n != 0 {
		t.Fatalf("count older than past: n=%d err=%v, want 0", n, err)
	}

	// The dry-run count matches what the sweep will remove.
	wantRemove, err := store.CountEvidenceOlderThan(ctx, "insee-emploi", future)
	if err != nil {
		t.Fatalf("count older than future: %v", err)
	}
	if wantRemove != int64(len(chunks)) {
		t.Fatalf("dry-run count = %d, want %d", wantRemove, len(chunks))
	}

	deleted, err := store.SweepEvidenceOlderThan(ctx, "insee-emploi", future)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != wantRemove {
		t.Fatalf("sweep removed %d, dry run predicted %d", deleted, wantRemove)
	}

	// The corpus is empty for the source now.
	if n, err := store.CountEvidenceOlderThan(ctx, "insee-emploi", future); err != nil || n != 0 {
		t.Fatalf("after sweep: n=%d err=%v, want 0", n, err)
	}

	// Re-ingest restores cleanly.
	for _, c := range chunks {
		if err := store.UpsertEmbeddedChunk(ctx, c); err != nil {
			t.Fatalf("re-ingest %s: %v", c.ExternalID, err)
		}
	}
	hits, err := store.SearchEvidence(ctx, unitVec(0), 5, 0, []string{"insee-emploi"})
	if err != nil {
		t.Fatalf("search after re-ingest: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("re-ingest did not restore searchable rows")
	}
}

// TestRetentionScopedToSource proves a sweep of one source never touches another
// that shares the table.
func TestRetentionScopedToSource(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.UpsertEmbeddedChunk(ctx, embeddedEvidence("insee-emploi", "a", unitVec(0))); err != nil {
		t.Fatalf("seed insee: %v", err)
	}
	if err := store.UpsertEmbeddedChunk(ctx, embeddedEvidence("dares-emploi", "b", unitVec(1))); err != nil {
		t.Fatalf("seed dares: %v", err)
	}

	future := time.Now().Add(time.Hour)
	if _, err := store.SweepEvidenceOlderThan(ctx, "insee-emploi", future); err != nil {
		t.Fatalf("sweep insee: %v", err)
	}

	// The other source is intact.
	if n, err := store.CountEvidenceOlderThan(ctx, "dares-emploi", future); err != nil || n != 1 {
		t.Fatalf("other source count = %d err=%v, want 1 (untouched)", n, err)
	}
}
