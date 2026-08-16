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
	counted int64
	deleted int64
	calls   struct{ count, del int }
	err     error
}

func (f *fakeSweeper) CountClaimChecksBefore(_ context.Context, _ time.Time) (int64, error) {
	f.calls.count++
	return f.counted, f.err
}

func (f *fakeSweeper) DeleteClaimChecksBefore(_ context.Context, _ time.Time) (int64, error) {
	f.calls.del++
	return f.deleted, f.err
}

func TestSweepDryRunOnlyCounts(t *testing.T) {
	t.Parallel()
	s := &fakeSweeper{counted: 7}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := sweep(context.Background(), logger, s, 24*time.Hour, false, time.Now()); err != nil {
		t.Fatalf("sweep dry run: %v", err)
	}
	if s.calls.count != 1 || s.calls.del != 0 {
		t.Errorf("dry run calls = %+v, want one count and no delete", s.calls)
	}
}

func TestSweepApplyDeletes(t *testing.T) {
	t.Parallel()
	s := &fakeSweeper{deleted: 3}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := sweep(context.Background(), logger, s, 24*time.Hour, true, time.Now()); err != nil {
		t.Fatalf("sweep apply: %v", err)
	}
	if s.calls.del != 1 || s.calls.count != 0 {
		t.Errorf("apply calls = %+v, want one delete and no count", s.calls)
	}
}

func TestSweepErrorsPropagate(t *testing.T) {
	t.Parallel()
	s := &fakeSweeper{err: errors.New("db down")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := sweep(context.Background(), logger, s, 24*time.Hour, true, time.Now()); err == nil {
		t.Error("sweep with failing store returned nil error")
	}
	if err := run(logger, 0, false); err == nil {
		t.Error("run without -max-age returned nil error")
	}
}
