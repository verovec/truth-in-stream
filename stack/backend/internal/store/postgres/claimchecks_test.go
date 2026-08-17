package postgres

import (
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TestClaimChecksRoundTrip proves the telemetry storage path end to end:
// batched insert, oldest-first read-back with every typed column intact, the
// dry-run count, and the retention delete.
func TestClaimChecksRoundTrip(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)
	checks := []domain.ClaimCheck{
		{
			OccurredAt: old, SessionKind: "live", Locale: "fr", Speaker: "A",
			UnitText: "unit", ClaimText: "vieux claim", DecisionPath: domain.DecisionCurated,
			RetrievalTop: 0.91, RetrievalCandidates: 3, RetrievalClaimHits: 1, RetrievalEvidenceHits: 2,
			Verdict: "credible", Basis: "evidence", Confidence: 0.91, Source: "curated",
			LLMCalls: 0, LatencyMS: 12,
		},
		{
			OccurredAt: now, SessionKind: "live", Locale: "fr", Speaker: "B",
			UnitText: "unit", ClaimText: "claim frais", DecisionPath: domain.DecisionVerified,
			Verdict: "disputed", Basis: "evidence", Literal: "inaccurate", Confidence: 0.8,
			Source: "verified", Escalated: true, LLMCalls: 2, LatencyMS: 900,
		},
	}
	if err := store.InsertClaimChecks(ctx, checks); err != nil {
		t.Fatalf("InsertClaimChecks: %v", err)
	}

	rows, err := store.ListClaimChecksSince(ctx, old.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListClaimChecksSince: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	got := rows[1]
	if got.DecisionPath != domain.DecisionVerified || !got.Escalated || got.LLMCalls != 2 || got.Literal != "inaccurate" {
		t.Errorf("read-back row = %+v, want the verified escalated row intact", got)
	}
	if !rows[0].OccurredAt.Before(rows[1].OccurredAt) {
		t.Errorf("rows not oldest-first: %v then %v", rows[0].OccurredAt, rows[1].OccurredAt)
	}

	n, err := store.CountClaimChecksBefore(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountClaimChecksBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("count before cutoff = %d, want 1", n)
	}

	removed, err := store.DeleteClaimChecksBefore(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeleteClaimChecksBefore: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	rows, err = store.ListClaimChecksSince(ctx, old.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListClaimChecksSince after sweep: %v", err)
	}
	if len(rows) != 1 || rows[0].ClaimText != "claim frais" {
		t.Errorf("rows after sweep = %+v, want only the fresh row", rows)
	}
}
