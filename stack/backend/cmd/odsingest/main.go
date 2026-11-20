// Command odsingest fetches the curated OpenDataSoft datasets from three French
// institutional portals (DREES health/social policy, DARES labor market, URSSAF
// private-sector employment) plus the SSMSI recorded-delinquency CSV bases,
// renders each observation into a self-contained French evidence passage, upserts
// it into the live evidence corpus un-embedded with provenance, and publishes one
// prioritized embedding job per passage to the RabbitMQ queue (RABBITMQ_URL). The
// existing embedding-worker fleet drains the queue and fills the vectors in place -
// the exact same bulk-into-live path statsingest uses, so this connector reuses the
// embedding.jobs queue and the embedworker with no new consumer. It is idempotent
// on the (series, period) provenance key, so re-running refreshes the figures
// without duplicating passages and re-publishes only the still-unembedded ones.
// Each portal writes its own corpus (drees/dares/urssaf/ssmsi) so a retrieved
// passage's publisher is identifiable.
//
// The Explore API v2.1 and the data.gouv.fr resources are open under the Etalab
// Licence Ouverte 2.0 and need no key. Embedding happens in the fleet, so this
// producer needs no Voyage key; it needs the broker URL (RABBITMQ_URL) and the
// database (DATABASE_URL).
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
	"github.com/verovec/truth-in-stream/backend/internal/stats/ods"
	"github.com/verovec/truth-in-stream/backend/internal/stats/ssmsi"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	flag.Parse()

	if err := run(logger); err != nil {
		logger.Error("odsingest failed", slog.Any("err", err))
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
	// the same configuration the worker consumes, so both bind to the same queue -
	// the OpenDataSoft/SSMSI path shares the embedding fleet exactly like statsingest.
	client, err := queue.New(queueCfg.ClientConfig(queueCfg.Prefetch))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// One Explore API client covers the three OpenDataSoft portals as configuration;
	// the SSMSI client resolves and downloads the data.gouv.fr delinquency CSV bases.
	odsClient := ods.New(ods.Config{})
	sources := make([]stats.Source, 0, len(ods.CuratedPortals)+1)
	for _, portal := range ods.CuratedPortals {
		sources = append(sources, ods.NewSource(odsClient, portal))
	}
	sources = append(sources, ssmsi.NewSource(ssmsi.New(ssmsi.Config{}), nil))

	statsCfg := stats.Config{
		MaxPriority:      queueCfg.MaxPriority,
		EnqueueBatchSize: producerCfg.EnqueueBatchSize,
	}

	// Each source writes a distinct, independent corpus, so a failure in one (a
	// transient outage at one portal) must not block the others. Log and continue
	// per source, then fail the run if any source failed so a scheduled job still
	// surfaces the error.
	var upserted, published int
	var failed []string
	for _, source := range sources {
		st, err := stats.Run(ctx, logger, source, store, qPublisher{client: client}, statsCfg)
		if err != nil {
			failed = append(failed, source.Corpus())
			logger.ErrorContext(ctx, "odsingest source failed",
				slog.String("corpus", source.Corpus()), slog.Any("err", err))
			continue
		}
		upserted += st.Upserted
		published += st.Published
		logger.InfoContext(ctx, "odsingest source complete",
			slog.String("corpus", source.Corpus()),
			slog.Int("upserted", st.Upserted),
			slog.Int("published", st.Published))
	}

	logger.InfoContext(ctx, "odsingest complete; the worker fleet fills the vectors in place",
		slog.Int("upserted", upserted), slog.Int("published", published))
	if len(failed) > 0 {
		return fmt.Errorf("odsingest: %d source(s) failed: %v", len(failed), failed)
	}
	return nil
}
