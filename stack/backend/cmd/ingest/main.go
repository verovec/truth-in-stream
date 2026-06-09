// Command ingest embeds the curated seed claims and upserts them into the
// claim store. It is idempotent: re-running it replaces claims by ID.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/ingest"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	seedPath := flag.String("seed", "seed/claims.json", "path to the seed claims JSON file")
	flag.Parse()

	if err := run(logger, *seedPath); err != nil {
		logger.Error("ingest failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, seedPath string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	emb, err := config.LoadEmbedding()
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

	f, err := os.Open(seedPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	seeds, err := ingest.LoadSeed(f)
	if err != nil {
		return err
	}

	embedder := embed.New(embed.Config{APIKey: emb.APIKey, Model: emb.Model, Dim: emb.Dim})

	n, err := ingest.Run(ctx, store, embedder, seeds, 0)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "ingest complete", slog.Int("claims", n), slog.String("seed", seedPath))
	return nil
}
