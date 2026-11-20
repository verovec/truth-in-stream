// Command hatvpcrawl diffs the HATVP open-data declarations index (open data,
// attribution "Haute Autorite pour la transparence de la vie publique"), fetches
// each new or changed declaration's XML, and publishes one generic
// connector.EvidenceJob per chunk of the rendered structured summary to the
// evidence queue, then exits. It needs no database: each job is self-contained, so
// the evidence worker (cmd/evidenceworker) embeds and upserts independently.
//
// A conditional GET (persisted ETag/Last-Modified) skips an unchanged index
// entirely; the manifest diff then fetches and republishes only the declarations
// whose index row moved. The broker comes from RABBITMQ_URL; HATVP_MAX_ITEMS
// bounds a backfill run; the run posts start/finish/failure alerts to Slack
// through the shared crawl notifier.
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
	"github.com/verovec/truth-in-stream/backend/internal/source/hatvp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("hatvp crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadHATVP()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadEvidenceQueue()
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

	producer, err := hatvp.New(hatvp.Config{
		MarkerPath:   cfg.MarkerPath,
		ManifestPath: cfg.ManifestPath,
		MaxPriority:  queueCfg.MaxPriority,
		MaxItems:     cfg.MaxItems,
	}, qPublisher{client: client}, logger)
	if err != nil {
		return err
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)

	logger.InfoContext(ctx, "hatvp crawl started", slog.String("queue", queueCfg.VersionedName()))
	stats, err := crawlnotify.RunWithAlerts(ctx, notifier, producer)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "hatvp crawl finished",
		slog.Int("published_records", stats.New), slog.Int("unchanged_records", stats.Skipped))
	return nil
}

// qPublisher adapts a queue.Client to hatvp.Publisher, so the producer package
// never imports the broker transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
