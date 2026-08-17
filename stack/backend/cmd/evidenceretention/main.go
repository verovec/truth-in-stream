// Command evidenceretention runs the per-source evidence retention sweep
// (VER-203, measure 3): it removes the chunks of one source last synced before a
// cutoff, so a superseded statistical generation ages out instead of accreting
// forever. It defaults to a dry run that only reports what it would remove; pass
// -apply to delete. Re-ingesting the source restores the rows cleanly, since the
// upsert recreates them from source.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	source := flag.String("source", "", "evidence source whose stale chunks to sweep (required)")
	maxAge := flag.Duration("max-age", 0, "remove chunks last synced longer ago than this (e.g. 720h); required and positive")
	apply := flag.Bool("apply", false, "actually delete; without it the sweep is a dry run that only reports counts")
	flag.Parse()

	if err := run(logger, *source, *maxAge, *apply); err != nil {
		logger.Error("evidence retention sweep failed", slog.Any("err", err))
		os.Exit(1)
	}
}

// sweeper is the store surface the sweep needs, kept minimal so the decision
// logic is unit-testable with a fake.
type sweeper interface {
	CountEvidenceOlderThan(ctx context.Context, source string, cutoff time.Time) (int64, error)
	SweepEvidenceOlderThan(ctx context.Context, source string, cutoff time.Time) (int64, error)
}

func run(logger *slog.Logger, source string, maxAge time.Duration, apply bool) error {
	if source == "" {
		return errors.New("evidence retention: -source is required")
	}
	if maxAge <= 0 {
		return errors.New("evidence retention: -max-age must be positive")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	return sweep(ctx, logger, store, source, maxAge, apply, time.Now())
}

// sweep runs the retention decision against store: a dry run reports the count it
// would remove, and an apply run deletes and reports the count it removed. now is
// injected so the cutoff is deterministic under test. It is the unit-testable
// core, free of config and signal wiring.
func sweep(ctx context.Context, logger *slog.Logger, store sweeper, source string, maxAge time.Duration, apply bool, now time.Time) error {
	cutoff := now.Add(-maxAge)
	if !apply {
		n, err := store.CountEvidenceOlderThan(ctx, source, cutoff)
		if err != nil {
			return fmt.Errorf("evidence retention: count: %w", err)
		}
		logger.InfoContext(ctx, "evidence retention dry run",
			slog.String("source", source), slog.Duration("max_age", maxAge),
			slog.Time("cutoff", cutoff), slog.Int64("would_remove", n))
		return nil
	}
	n, err := store.SweepEvidenceOlderThan(ctx, source, cutoff)
	if err != nil {
		return fmt.Errorf("evidence retention: sweep: %w", err)
	}
	logger.InfoContext(ctx, "evidence retention sweep complete",
		slog.String("source", source), slog.Duration("max_age", maxAge),
		slog.Time("cutoff", cutoff), slog.Int64("removed", n))
	return nil
}
