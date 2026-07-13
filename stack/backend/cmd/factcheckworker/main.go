// Command factcheckworker drains self-contained curated-claim jobs from the
// fact-check queue, embeds each claim's text through the Voyage API, and upserts
// the curated claim record (text + verdict + flags + source + vector) straight
// into the political claim DB. It is a long-running consumer with no HTTP
// surface: running several replicas scales throughput, and a graceful SIGTERM
// lets in-flight work finish or be requeued. The broker comes from RABBITMQ_URL,
// the database from DATABASE_URL, embeddings from the Voyage API
// (EMBEDDING_API_KEY); CRAWL_WORKER_* tunes concurrency and retries (shared with
// the wiki crawl worker so the embedding-fault handling is reused, not redefined).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// Embedding-retry backoff bounds, shared with the bulk pipeline: a rate-limited
// request waits at least a second and at most a minute between attempts.
const (
	embedRetryBaseDelay = 1 * time.Second
	embedRetryMaxDelay  = 60 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("fact-check worker exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return err
	}
	embedding, err := config.LoadEmbedding()
	if err != nil {
		return err
	}
	workerCfg, err := config.LoadCrawlWorker()
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

	client, err := queue.New(queueCfg.ClientConfig(workerCfg.Concurrency))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	worker := factcheckjob.NewWorker(
		newEmbedder(logger, embedding, workerCfg),
		store,
		qStream{client: client},
		qEnqueuer{client: client},
		logger,
		factcheckjob.Config{Concurrency: workerCfg.Concurrency, MaxAttempts: workerCfg.MaxAttempts, KnownVersions: queueCfg.KnownVersions},
	)

	logger.InfoContext(ctx, "fact-check worker started",
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("concurrency", workerCfg.Concurrency),
		slog.Int("max_attempts", workerCfg.MaxAttempts))

	// Announce the drain to Slack symmetrically to the producers (silent no-op when
	// SLACK_WEBHOOK_URL is unset), reporting processed and DLQ-parked counts on stop.
	notifier := crawlnotify.NewNotifier(config.LoadCrawlAlerts().WebhookURL)
	_, err = crawlnotify.RunConsumerWithAlerts(ctx, notifier, "fact-check", queueCfg.VersionedName(),
		func(ctx context.Context) (crawlnotify.ConsumerStats, error) {
			runErr := worker.Run(ctx)
			s := worker.Stats()
			return crawlnotify.ConsumerStats{Processed: s.Processed, ParkedToDLQ: s.ParkedToDLQ}, runErr
		})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "fact-check worker stopped")
	return nil
}

// newEmbedder builds the Voyage embedding client wrapped in the shared retry and
// rate-limit decorators, identical to the crawl worker so transient-fault
// handling is reused rather than reimplemented. The rate limiter sits beneath the
// retry decorator so every attempt is paced.
func newEmbedder(logger *slog.Logger, p config.Embedding, w config.EmbedWorker) *embed.RetryClient {
	return embed.WithRetry(
		embed.WithRateLimit(
			embed.New(embed.Config{
				APIKey:     p.APIKey,
				Model:      p.Model,
				Dim:        p.Dim,
				HTTPClient: &http.Client{Timeout: w.HTTPTimeout},
			}),
			w.RequestsPerMinute,
		),
		embed.RetryConfig{MaxAttempts: w.EmbedMaxRetries, BaseDelay: embedRetryBaseDelay, MaxDelay: embedRetryMaxDelay, Logger: logger},
	)
}
