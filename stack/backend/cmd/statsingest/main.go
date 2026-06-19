// Command statsingest fetches the curated statistical series from every
// registered source (EU Eurostat, the French interior ministry's open-data
// permit/asylum CSVs, and the INSEE national labor-market series), renders each
// observation into a self-contained French evidence passage, embeds it with the
// shared document-embedding model, and upserts it into the evidence corpus with
// provenance. It is an offline ingest entrypoint mirroring cmd/ingest: idempotent
// on the (series, period) provenance key, so re-running refreshes the figures
// without duplicating passages. Each source writes under its own corpus label, so
// a retrieved passage's publisher is identifiable.
//
// The Eurostat, interior-ministry, and INSEE BDM endpoints need no key (the
// INSEE_API_KEY is optional and read from the environment only); the only
// required secret is the embedding API key, loaded from the environment like
// every other ingest.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
	"github.com/verovec/truth-in-stream/backend/internal/stats/eurostat"
	"github.com/verovec/truth-in-stream/backend/internal/stats/insee"
	"github.com/verovec/truth-in-stream/backend/internal/stats/interieur"
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

	sources := []stats.Source{
		eurostat.NewSource(eurostat.New(eurostat.Config{}), nil),
		interieur.NewSource(interieur.New(interieur.Config{}), nil),
		insee.NewSource(insee.New(insee.ConfigFromEnv()), nil),
	}

	// Each source writes a distinct, independent corpus, so a failure in one
	// (e.g. a transient outage at one provider) must not block the others. Log
	// and continue per source, then fail the run if any source failed so a
	// scheduled job still surfaces the error.
	total := 0
	var failed []string
	for _, source := range sources {
		n, err := stats.Run(ctx, source, embedder, store, 0)
		if err != nil {
			failed = append(failed, source.Corpus())
			logger.ErrorContext(ctx, "statsingest source failed",
				slog.String("corpus", source.Corpus()), slog.Any("err", err))
			continue
		}
		total += n
		logger.InfoContext(ctx, "statsingest source complete",
			slog.String("corpus", source.Corpus()), slog.Int("passages", n))
	}

	logger.InfoContext(ctx, "statsingest complete", slog.Int("passages", total))
	if len(failed) > 0 {
		return fmt.Errorf("statsingest: %d source(s) failed: %v", len(failed), failed)
	}
	return nil
}
