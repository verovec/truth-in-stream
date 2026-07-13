// Command embedworker drains embedding jobs from the priority queue, embeds each
// chunk's text through the Voyage API, and writes the vector into the staging
// corpus. It is a long-running consumer with no HTTP surface: running several
// replicas scales embedding throughput horizontally, the broker delivers
// higher-priority chunks first, and a graceful SIGTERM lets in-flight work finish
// or be requeued so a scale-down loses nothing. The broker comes from
// RABBITMQ_URL, the database from DATABASE_URL, and embeddings from the Voyage
// API (EMBEDDING_API_KEY); EMBED_WORKER_* tunes concurrency and retries.
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
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
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
		logger.Error("embedding worker exited with error", slog.Any("err", err))
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
	embedding, err := config.LoadEmbedding()
	if err != nil {
		return err
	}
	workerCfg, err := config.LoadEmbedWorker()
	if err != nil {
		return err
	}
	idle, err := config.LoadWorkerIdle()
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

	// Prefetch sizes the broker's in-flight window to a full batch per concurrent
	// slot, so each replica can fill every batch it embeds in parallel without
	// handing it a backlog it cannot work. The product is computed in int64 and
	// clamped to AMQP's 16-bit prefetch_count ceiling, so an extreme
	// concurrency x batch_size cannot overflow or exceed what the wire allows.
	const maxPrefetch = 65535
	prefetch64 := int64(workerCfg.Concurrency) * int64(workerCfg.BatchSize)
	if prefetch64 < 1 {
		prefetch64 = 1
	}
	if prefetch64 > maxPrefetch {
		prefetch64 = maxPrefetch
	}
	prefetch := int(prefetch64)
	client, err := queue.New(queueCfg.ClientConfig(prefetch))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	worker := embedjob.NewWorker(
		newEmbedder(logger, embedding, workerCfg),
		store,
		qStream{client: client, idle: idle},
		qEnqueuer{client: client},
		logger,
		embedjob.Config{
			Concurrency:    workerCfg.Concurrency,
			BatchSize:      workerCfg.BatchSize,
			BatchWait:      workerCfg.BatchWait,
			MaxAttempts:    workerCfg.MaxAttempts,
			MaxBatchTokens: workerCfg.MaxBatchTokens,
			KnownVersions:  queueCfg.KnownVersions,
		},
	)

	logger.InfoContext(ctx, "embedding worker started",
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("concurrency", workerCfg.Concurrency),
		slog.Int("batch_size", workerCfg.BatchSize),
		slog.Duration("batch_wait", workerCfg.BatchWait),
		slog.Int("max_batch_tokens", workerCfg.MaxBatchTokens),
		slog.Int("max_attempts", workerCfg.MaxAttempts),
		slog.Duration("idle_timeout", idle))

	// Announce the drain to Slack symmetrically to the producers (silent no-op when
	// SLACK_WEBHOOK_URL is unset), reporting processed and DLQ-parked counts on stop.
	notifier := crawlnotify.NewNotifier(config.LoadCrawlAlerts().WebhookURL)
	_, err = crawlnotify.RunConsumerWithAlerts(ctx, notifier, "embedding", queueCfg.VersionedName(),
		func(ctx context.Context) (crawlnotify.ConsumerStats, error) {
			runErr := worker.Run(ctx)
			s := worker.Stats()
			return crawlnotify.ConsumerStats{Processed: s.Processed, ParkedToDLQ: s.ParkedToDLQ}, runErr
		})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "embedding worker stopped")
	return nil
}

// newEmbedder builds the Voyage embedding client wrapped in the shared retry and
// rate-limit decorators, so the worker reuses the bulk pipeline's transient-fault
// handling rather than reimplementing it. The rate limiter sits beneath the retry
// decorator so every attempt is paced.
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
