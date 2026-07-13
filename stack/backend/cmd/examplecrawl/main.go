// Command examplecrawl is the compile-checked template producer binary: the
// one-shot a new source's cmd/<name>crawl is copied from. It builds the example
// connector (internal/example), publishes a bounded set of placeholder
// connector.EvidenceJob bodies, and exits - mirroring cmd/wikicrawl and
// cmd/factcheckcrawl so the recipe is uniform across sources. The broker comes from
// RABBITMQ_URL; EXAMPLE_* selects the run's label and item bound.
//
// It is deliberately not wired into the connector registry or
// docker-compose.ingest.yml, and it publishes to a dedicated queue
// (config.LoadExampleQueue) that no production worker drains, so it can never write
// into the live crawl.chunks queue or the production evidence_chunks corpus. It
// exists as a buildable reference only.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/example"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("example crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	exampleCfg, err := config.LoadExample()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadExampleQueue()
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

	producer, err := example.New(qPublisher{client: client}, logger, example.Config{
		Label:       exampleCfg.Label,
		MaxItems:    exampleCfg.MaxItems,
		MaxPriority: queueCfg.MaxPriority,
	})
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "example crawl started",
		slog.String("label", exampleCfg.Label),
		slog.Int("max_items", exampleCfg.MaxItems),
		slog.String("queue", queueCfg.VersionedName()))

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	stats, err := crawlnotify.RunWithAlerts(ctx, notifier, producer)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "example crawl finished", slog.Int("published", stats.New))
	return nil
}
