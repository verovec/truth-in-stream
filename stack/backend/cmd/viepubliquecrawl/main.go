// Command viepubliquecrawl downloads the DILA "Discours publics" metadata dump
// from vie-publique.fr (Licence Ouverte / Open Licence v2.0, attribution "DILA -
// vie-publique.fr"), diffs it against a persisted per-identifier manifest, and
// publishes one generic connector.EvidenceJob per chunk of each new or changed
// speech metadata record to the evidence queue, then exits. It needs no database:
// each job is self-contained, so the evidence worker (cmd/evidenceworker) embeds
// and upserts independently.
//
// It is a producer in the ingestion fleet's producer -> queue -> worker pattern. A
// conditional GET (persisted ETag/Last-Modified) skips an unchanged dump entirely;
// the manifest diff skips unchanged records within a changed dump. The broker
// comes from RABBITMQ_URL; VIEPUBLIQUE_MAX_ITEMS bounds a backfill run; the run
// posts start/finish/failure alerts to Slack through the shared crawl notifier.
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
	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
	"github.com/verovec/truth-in-stream/backend/internal/source/viepublique"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("vie-publique crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadViePublique()
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

	producer, err := evidencesrc.NewDumpProducer(evidencesrc.DumpConfig{
		Source:       viepublique.Source,
		URL:          viepublique.DumpURL,
		Scope:        "vie-publique discours (metadonnees)",
		Extract:      viepublique.Extract,
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

	logger.InfoContext(ctx, "vie-publique crawl started", slog.String("queue", queueCfg.VersionedName()))
	stats, err := crawlnotify.RunWithAlerts(ctx, notifier, producer)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "vie-publique crawl finished",
		slog.Int("published_records", stats.New), slog.Int("unchanged_records", stats.Skipped))
	return nil
}

// qPublisher adapts a queue.Client to evidencesrc.Publisher, so the producer
// package never imports the broker transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
