// Command statsingest fetches the curated statistical series from every
// registered source (EU Eurostat, the French interior ministry's open-data
// permit/asylum CSVs, and the INSEE national labor-market series), renders each
// observation into a self-contained French evidence passage, upserts it into the
// live evidence corpus un-embedded with provenance, and publishes one prioritized
// embedding job per passage to the RabbitMQ queue (RABBITMQ_URL). The existing
// embedding-worker fleet drains the queue and fills the vectors in place - the
// same bulk-into-live pattern the Wikipedia corpus uses, so a broad sweep scales
// by worker replica count rather than one synchronous Voyage burst. It is
// idempotent on the (series, period) provenance key, so re-running refreshes the
// figures without duplicating passages and re-publishes only the still-unembedded
// ones. Each source writes under its own corpus label, so a retrieved passage's
// publisher is identifiable.
//
// The Eurostat, interior-ministry, and INSEE BDM endpoints need no key (the
// INSEE_API_KEY is optional and read from the environment only). Embedding now
// happens in the fleet, so this producer needs no Voyage key; it needs the broker
// URL (RABBITMQ_URL) and the database (DATABASE_URL).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
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
	queueCfg, err := config.LoadQueue()
	if err != nil {
		return err
	}
	producerCfg, err := config.LoadWikiProducer()
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

	// The producer publishes to the active versioned embedding queue resolved from
	// the same configuration the worker consumes, so both bind to the same queue
	// without touching the enqueue logic - the stats path shares the wiki fleet.
	client, err := queue.New(queueCfg.ClientConfig(queueCfg.Prefetch))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// The INSEE BDM client is shared across the hand-curated labor series and the
	// dataflow-discovery sweep so successive requests to the same host are spaced
	// by one rate-limiter; each dataflow expands its live members off the SDMX
	// catalog at ingest time and writes under its own economic-theme corpus.
	inseeClient := insee.New(insee.ConfigFromEnv())
	sources := []stats.Source{
		eurostat.NewSource(eurostat.New(eurostat.Config{}), nil),
		interieur.NewSource(interieur.New(interieur.Config{}), nil),
		insee.NewSource(inseeClient, nil),
	}
	for _, df := range insee.CuratedDataflows {
		sources = append(sources, insee.NewDataflowSource(inseeClient, df))
	}

	statsCfg := stats.Config{
		MaxPriority:      queueCfg.MaxPriority,
		EnqueueBatchSize: producerCfg.EnqueueBatchSize,
	}

	// Each source writes a distinct, independent corpus, so a failure in one
	// (e.g. a transient outage at one provider) must not block the others. Log
	// and continue per source, then fail the run if any source failed so a
	// scheduled job still surfaces the error.
	var upserted, published int
	var failed []string
	for _, source := range sources {
		st, err := stats.Run(ctx, logger, source, store, qPublisher{client: client}, statsCfg)
		if err != nil {
			failed = append(failed, source.Corpus())
			logger.ErrorContext(ctx, "statsingest source failed",
				slog.String("corpus", source.Corpus()), slog.Any("err", err))
			continue
		}
		upserted += st.Upserted
		published += st.Published
		logger.InfoContext(ctx, "statsingest source complete",
			slog.String("corpus", source.Corpus()),
			slog.Int("upserted", st.Upserted),
			slog.Int("published", st.Published))
	}

	logger.InfoContext(ctx, "statsingest complete; the worker fleet fills the vectors in place",
		slog.Int("upserted", upserted), slog.Int("published", published))
	if len(failed) > 0 {
		return fmt.Errorf("statsingest: %d source(s) failed: %v", len(failed), failed)
	}
	return nil
}
