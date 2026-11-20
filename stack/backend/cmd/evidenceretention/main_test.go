package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeSweeper struct {
	countN, sweepN int64
	countErr       error
	sweepErr       error

	counted, swept bool
	gotSource      string
	gotCutoff      time.Time
}

func (f *fakeSweeper) CountEvidenceOlderThan(_ context.Context, source string, cutoff time.Time) (int64, error) {
	f.counted, f.gotSource, f.gotCutoff = true, source, cutoff
	return f.countN, f.countErr
}

func (f *fakeSweeper) SweepEvidenceOlderThan(_ context.Context, source string, cutoff time.Time) (int64, error) {
	f.swept, f.gotSource, f.gotCutoff = true, source, cutoff
	return f.sweepN, f.sweepErr
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestSweepDryRunOnlyCounts proves a dry run reports what it would remove and
// never deletes, and that the cutoff is now minus max-age.
func TestSweepDryRunOnlyCounts(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeSweeper{countN: 7}
	if err := sweep(t.Context(), quietLogger(), f, "insee-emploi", 720*time.Hour, false, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !f.counted || f.swept {
		t.Fatalf("dry run: counted=%v swept=%v, want counted only", f.counted, f.swept)
	}
	if f.gotSource != "insee-emploi" {
		t.Errorf("source = %q, want insee-emploi", f.gotSource)
	}
	if want := now.Add(-720 * time.Hour); !f.gotCutoff.Equal(want) {
		t.Errorf("cutoff = %v, want %v", f.gotCutoff, want)
	}
}

// TestSweepApplyDeletes proves the apply run deletes and never counts-only.
func TestSweepApplyDeletes(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeSweeper{sweepN: 3}
	if err := sweep(t.Context(), quietLogger(), f, "insee-emploi", 24*time.Hour, true, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !f.swept || f.counted {
		t.Fatalf("apply run: swept=%v counted=%v, want swept only", f.swept, f.counted)
	}
}

// TestSweepPropagatesStoreError proves a store failure surfaces rather than being
// swallowed.
func TestSweepPropagatesStoreError(t *testing.T) {
	f := &fakeSweeper{sweepErr: errors.New("boom")}
	if err := sweep(t.Context(), quietLogger(), f, "s", time.Hour, true, time.Now()); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestRunRejectsMissingArgs proves the wiring guard rejects an empty source or a
// non-positive max-age before opening any store.
func TestRunRejectsMissingArgs(t *testing.T) {
	if err := run(quietLogger(), "", time.Hour, false); err == nil {
		t.Error("empty source: expected error")
	}
	if err := run(quietLogger(), "s", 0, false); err == nil {
		t.Error("zero max-age: expected error")
	}
}
