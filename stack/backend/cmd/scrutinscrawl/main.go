// Command scrutinscrawl conditionally downloads the Assemblee Nationale open-data
// Scrutins.json.zip bulk archive (Etalab open-data license, attribution
// "Assemblee nationale - data.assemblee-nationale.fr"), discovers each recorded
// scrutin inside it, and publishes one self-contained scrutin job to the scrutins
// queue, then exits. It needs no database: each job carries the scrutin payload,
// so the scrutins worker (cmd/scrutinsworker) parses and upserts independently.
//
// It is the scrutins half of the ingestion fleet's producer -> queue -> worker
// pattern, replacing the manual cmd/scrutinsingest -dir path for normal operation
// (that backfill path remains for loading a pre-downloaded archive). A persisted
// ETag/Last-Modified marker makes the download a conditional GET, so an unchanged
// archive returns 304 and the run does no redundant work. The broker comes from
// RABBITMQ_URL; SCRUTINS_* selects the legislature and marker path; the run posts
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
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsarchive"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("scrutins crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	archiveCfg, err := config.LoadScrutinsArchive()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadScrutinsQueue()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := queue.New(queue.Config{
		URL:         queueCfg.URL,
		QueueName:   queueCfg.VersionedName(),
		Version:     queueCfg.Version,
		MaxPriority: queueCfg.MaxPriority,
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	producer, err := scrutinsarchive.New(scrutinsarchive.Config{
		Legislature: archiveCfg.Legislature,
		MarkerPath:  archiveCfg.MarkerPath,
		MaxPriority: queueCfg.MaxPriority,
	}, qPublisher{client: client}, logger)
	if err != nil {
		return err
	}

	notifier := crawlnotify.NewNotifier(config.LoadCrawlAlerts().WebhookURL)

	logger.InfoContext(ctx, "scrutins crawl started",
		slog.String("legislature", archiveCfg.Legislature),
		slog.String("queue", queueCfg.VersionedName()))

	stats, err := crawlnotify.RunWithAlerts(ctx, notifier, producer)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "scrutins crawl finished",
		slog.Int("published_scrutins", stats.New),
		slog.Int("skipped_scrutins", stats.Skipped))
	return nil
}

// qPublisher adapts a queue.Client to scrutinsarchive.Publisher, so the producer
// package never imports the broker transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
