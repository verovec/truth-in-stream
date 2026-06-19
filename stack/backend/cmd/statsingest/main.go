// Command statsingest fetches the curated Eurostat statistical series, renders
// each observation into a self-contained French evidence passage, embeds it
// with the shared document-embedding model, and upserts it into the evidence
// corpus with provenance. It is an offline ingest entrypoint mirroring
// cmd/ingest: idempotent on the (series, period) provenance key, so re-running
// refreshes the figures without duplicating passages.
//
// The Eurostat dissemination API needs no key; the only secret is the embedding
// API key, loaded from the environment like every other ingest.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
	"github.com/verovec/truth-in-stream/backend/internal/stats/eurostat"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	flag.Parse()

	if err := run(logger); err != nil {
		logger.Error("statsingest failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
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

	embedder := embed.WithRetry(
		embed.New(embed.Config{APIKey: emb.APIKey, Model: emb.Model, Dim: emb.Dim}),
		embed.RetryConfig{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second, Logger: logger},
	)

	source := eurostat.NewSource(eurostat.New(eurostat.Config{}), nil)

	n, err := stats.Run(ctx, source, embedder, store, 0)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "statsingest complete", slog.Int("passages", n), slog.String("corpus", stats.StatCorpus))
	return nil
}
