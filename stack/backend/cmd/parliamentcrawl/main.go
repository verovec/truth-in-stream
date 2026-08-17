// Command parliamentcrawl downloads a French parliamentary open-data bulk dump
// (Licence Ouverte / Open Licence 2.0, attribution "Assemblee nationale -
// data.assemblee-nationale.fr"), diffs it against a persisted per-identifier
// manifest, and publishes one generic connector.EvidenceJob per chunk of each new
// or changed record to the evidence queue, then exits. It needs no database: each
// job is self-contained, so the evidence worker (cmd/evidenceworker) embeds and
// upserts independently.
//
// It is a producer in the ingestion fleet's producer -> queue -> worker pattern. A
// conditional GET (persisted ETag/Last-Modified) skips an unchanged dump entirely;
// the manifest diff skips unchanged records within a changed dump, so a daily run
// reprocesses only what actually moved. The broker comes from RABBITMQ_URL;
// PARLIAMENT_* selects the dataset, legislature, and state paths; the run posts
// start/finish/failure alerts to Slack through the shared crawl notifier.
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
	"github.com/verovec/truth-in-stream/backend/internal/source/parliament"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("parliament crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	parliamentCfg, err := config.LoadParliament()
	if err != nil {
		return err
	}
	// A voting dataset (Senat scrutins) publishes chamber-aware scrutin jobs to the
	// scrutins queue drained by the scrutins worker; every other parliament dataset
	// publishes generic evidence jobs to the evidence queue drained by the evidence
	// worker.
	queueCfg, err := parliamentQueue(parliamentCfg.Dataset)
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

	producer, err := parliament.New(parliament.Config{
		Dataset:      parliamentCfg.Dataset,
		Legislature:  parliamentCfg.Legislature,
		MarkerPath:   parliamentCfg.MarkerPath,
		ManifestPath: parliamentCfg.ManifestPath,
		MaxPriority:  queueCfg.MaxPriority,
		MaxItems:     parliamentCfg.MaxItems,
		SinceYear:    parliamentCfg.SinceYear,
	}, qPublisher{client: client}, logger)
	if err != nil {
		return err
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)

	logger.InfoContext(ctx, "parliament crawl started",
		slog.String("dataset", parliamentCfg.Dataset),
		slog.String("legislature", parliamentCfg.Legislature),
		slog.String("queue", queueCfg.VersionedName()))

	stats, err := crawlnotify.RunWithAlerts(ctx, notifier, producer)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "parliament crawl finished",
		slog.Int("published_records", stats.New),
		slog.Int("unchanged_records", stats.Skipped))
	return nil
}

// parliamentQueue binds the dataset to its queue: the scrutins queue for the voting
// dataset, the evidence queue for every textual dataset.
func parliamentQueue(dataset string) (config.Queue, error) {
	if parliament.IsVotingDataset(dataset) {
		return config.LoadScrutinsQueue()
	}
	return config.LoadEvidenceQueue()
}

// qPublisher adapts a queue.Client to parliament.Publisher, so the producer
// package never imports the broker transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
