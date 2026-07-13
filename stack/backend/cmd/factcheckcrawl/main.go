// Command factcheckcrawl reads already-checked French claims from the Google
// Fact Check Tools API (the standardized aggregator of schema.org ClaimReview
// data published by Les Decodeurs, AFP Factuel, and franceinfo Vrai ou Fake) and
// publishes one self-contained curated-claim job per reviewed claim to the
// fact-check queue, then exits. It needs no database: every field a
// political_claims row requires travels in the message, so the fact-check worker
// (cmd/factcheckworker) drains the queue into the curated claim DB independently.
// The broker comes from RABBITMQ_URL; FACTCHECK_* selects the API key, queries,
// and shape. Reading the API rather than scraping article HTML keeps ingestion
// within each outlet's terms of service.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("fact-check crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	archiveCfg, err := config.LoadFactCheckArchive()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	producer, err := factcheckarchive.New(factcheckarchive.Config{
		APIKey:       archiveCfg.APIKey,
		LanguageCode: archiveCfg.Language,
		MaxPriority:  queueCfg.MaxPriority,
	})
	if err != nil {
		return err
	}

	streams := factcheckarchive.BuildStreams(factcheckarchive.Strategy{
		Topics:         archiveCfg.Topics,
		PublisherSites: archiveCfg.PublisherSites,
		MaxPages:       archiveCfg.MaxPages,
		MaxAgeDays:     archiveCfg.MaxAgeDays,
	})
	checkpoint, err := factcheckarchive.LoadStreamCheckpoint(archiveCfg.CheckpointPath)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "fact-check crawl started",
		slog.Int("streams", len(streams)),
		slog.Int("topics", len(archiveCfg.Topics)),
		slog.Int("publisher_sites", len(archiveCfg.PublisherSites)),
		slog.String("language", archiveCfg.Language),
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("max_pages", archiveCfg.MaxPages))

	p := factcheckProducer{
		client:     producer,
		logger:     logger,
		pub:        qPublisher{client: client},
		streams:    streams,
		checkpoint: checkpoint,
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	total, err := crawlnotify.RunWithAlerts(ctx, notifier, p)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "fact-check crawl finished",
		slog.Int("published_claims", total.New),
		slog.Int("skipped_claims", total.Skipped))
	return nil
}
