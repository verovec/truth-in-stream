// Command scrutinsworker drains self-contained scrutin jobs from the scrutins
// queue, parses each scrutin's raw AN open-data JSON into per-deputy voting
// records, and upserts them into the voting store. It is a long-running consumer
// with no HTTP surface: running several replicas scales throughput, and a
// graceful SIGTERM lets in-flight work finish or be requeued. The broker comes
// from RABBITMQ_URL, the database from DATABASE_URL; SCRUTINS_WORKER_* tunes
// concurrency and retries. Upserts are idempotent by (person, scrutin), so a
// redelivered job is safe.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsjob"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("scrutins worker exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadScrutinsQueue()
	if err != nil {
		return err
	}
	workerCfg, err := config.LoadScrutinsWorker()
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

	client, err := queue.New(queueCfg.ClientConfig(workerCfg.Concurrency))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	worker := scrutinsjob.NewWorker(
		store,
		qStream{client: client},
		qEnqueuer{client: client},
		logger,
		scrutinsjob.Config{Concurrency: workerCfg.Concurrency, MaxAttempts: workerCfg.MaxAttempts, KnownVersions: queueCfg.KnownVersions},
	)

	logger.InfoContext(ctx, "scrutins worker started",
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("concurrency", workerCfg.Concurrency),
		slog.Int("max_attempts", workerCfg.MaxAttempts))
	if err := worker.Run(ctx); err != nil {
		return err
	}
	logger.InfoContext(ctx, "scrutins worker stopped")
	return nil
}
