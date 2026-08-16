// Command telemetryretention runs the claim-check telemetry retention sweep
// (VER-229): it removes analytics rows recorded before a cutoff so the
// telemetry table's growth stays bounded. It defaults to a dry run that only
// reports what it would remove; pass -apply to delete. Telemetry is an
// append-only analytical record, so an aged-out row is simply gone - nothing
// downstream references it.
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

	maxAge := flag.Duration("max-age", 0, "remove telemetry rows recorded longer ago than this (e.g. 2160h); required and positive")
	apply := flag.Bool("apply", false, "actually delete; without it the sweep is a dry run that only reports counts")
	flag.Parse()

	if err := run(logger, *maxAge, *apply); err != nil {
		logger.Error("telemetry retention sweep failed", slog.Any("err", err))
		os.Exit(1)
	}
}

// sweeper is the store surface the sweep needs, kept minimal so the decision
// logic is unit-testable with a fake.
type sweeper interface {
	CountClaimChecksBefore(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteClaimChecksBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

func run(logger *slog.Logger, maxAge time.Duration, apply bool) error {
	if maxAge <= 0 {
		return errors.New("telemetry retention: -max-age must be positive")
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

	return sweep(ctx, logger, store, maxAge, apply, time.Now())
}

// sweep runs the retention decision against store: a dry run reports the count
// it would remove, and an apply run deletes and reports the count it removed.
// now is injected so the cutoff is deterministic under test.
func sweep(ctx context.Context, logger *slog.Logger, store sweeper, maxAge time.Duration, apply bool, now time.Time) error {
	cutoff := now.Add(-maxAge)
	if !apply {
		n, err := store.CountClaimChecksBefore(ctx, cutoff)
		if err != nil {
			return fmt.Errorf("telemetry retention: count: %w", err)
		}
		logger.InfoContext(ctx, "telemetry retention dry run",
			slog.Duration("max_age", maxAge), slog.Time("cutoff", cutoff), slog.Int64("would_remove", n))
		return nil
	}
	n, err := store.DeleteClaimChecksBefore(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("telemetry retention: sweep: %w", err)
	}
	logger.InfoContext(ctx, "telemetry retention sweep complete",
		slog.Duration("max_age", maxAge), slog.Time("cutoff", cutoff), slog.Int64("removed", n))
	return nil
}
