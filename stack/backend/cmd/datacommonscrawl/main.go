// Command datacommonscrawl reads the DataCommons ClaimReview data feed (the
// aggregated, schema.org-standardized markup published through the Google Fact
// Check Markup Tool), keeps the records from an allowlist of vetted French
// fact-check outlets, and publishes one self-contained curated-claim job per
// record to the fact-check queue, then exits. It needs no database: every field a
// political_claims row requires travels in the message, so the fact-check worker
// (cmd/factcheckworker) drains the queue into the curated claim DB independently.
// It is the redundant, keyless companion to cmd/factcheckcrawl (the Google API
// path): both publish claim jobs keyed on the review URL to factcheck.claims, so a
// claim reviewed by an allowlisted outlet dedupes to one row whichever path
// ingested it. The broker comes from RABBITMQ_URL; DATACOMMONS_* selects the feed,
// outlet allowlist, and item cap.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("datacommons crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	archiveCfg, err := config.LoadDataCommonsArchive()
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

	producer, err := newFeedClient(archiveCfg, queueCfg.MaxPriority)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "datacommons crawl started",
		slog.String("feed", archiveCfg.FeedURL),
		slog.String("format", archiveCfg.Format),
		slog.Int("outlets", len(archiveCfg.OutletAllowlist)),
		slog.Int("max_items", archiveCfg.MaxItems),
		slog.String("queue", queueCfg.VersionedName()))

	p := datacommonsProducer{
		client:  producer,
		logger:  logger,
		pub:     qPublisher{client: client},
		outlets: archiveCfg.OutletAllowlist,
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	total, err := crawlnotify.RunWithAlerts(ctx, notifier, p)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "datacommons crawl finished",
		slog.Int("published_claims", total.New),
		slog.Int("skipped_claims", total.Skipped))
	return nil
}
