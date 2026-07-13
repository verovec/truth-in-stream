// Command legifrancecrawl fetches a configured narrow corpus of French code
// articles from the DILA Legifrance API (via the PISTE OAuth2 gateway), diffs each
// against a persisted manifest, and publishes one generic connector.EvidenceJob
// per chunk of each new or changed article's consolidated text to the evidence
// queue, then exits. It needs no database: each job is self-contained, so the
// evidence worker (cmd/evidenceworker) embeds and upserts independently.
//
// The PISTE OAuth2 client-credentials come from env/secrets only
// (LEGIFRANCE_CLIENT_ID / LEGIFRANCE_CLIENT_SECRET, materialized on-host from
// Secrets Manager in the cloud). When they are absent the run degrades to a clean
// skip. The corpus is LEGIFRANCE_ARTICLES (comma-separated "LEGIARTI...=Label");
// requests are quota-paced. The broker comes from RABBITMQ_URL; the run posts
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
	"github.com/verovec/truth-in-stream/backend/internal/source/legifrance"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("legifrance crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadLegifrance()
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

	articles := make([]legifrance.ArticleRef, 0, len(cfg.Articles))
	for _, a := range cfg.Articles {
		articles = append(articles, legifrance.ArticleRef{ID: a.ID, Label: a.Label})
	}

	producer, err := legifrance.New(legifrance.Config{
		Credentials:  legifrance.Credentials{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret},
		TokenURL:     cfg.TokenURL,
		APIBaseURL:   cfg.APIBaseURL,
		Articles:     articles,
		ManifestPath: cfg.ManifestPath,
		MaxPriority:  queueCfg.MaxPriority,
		MaxItems:     cfg.MaxItems,
		MinInterval:  cfg.MinInterval,
	}, qPublisher{client: client}, logger)
	if err != nil {
		return err
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)

	logger.InfoContext(ctx, "legifrance crawl started",
		slog.String("queue", queueCfg.VersionedName()), slog.Int("articles", len(articles)))
	stats, err := crawlnotify.RunWithAlerts(ctx, notifier, producer)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "legifrance crawl finished",
		slog.Int("published_records", stats.New), slog.Int("unchanged_records", stats.Skipped))
	return nil
}

// qPublisher adapts a queue.Client to legifrance.Publisher, so the producer
// package never imports the broker transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
