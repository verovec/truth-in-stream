// Command scheduler is the ingestion fleet's always-on local cron scheduler. It
// registers the Wikipedia, fact-check-archive, and scrutins producers, each on its
// own configurable cron cadence, and runs them until a signal arrives - replacing
// the manual `make`/compose one-shot producer runs. Every run publishes into its
// RabbitMQ queue for the worker fleet to drain, an overlapping tick of a source
// whose previous run is still in flight is skipped, and each run posts the shared
// start/finish/error Slack alerts through crawlnotify.
//
// It is a thin wiring layer over internal/schedule (the runtime-agnostic registry
// and tick loop) so a future cloud runner can reuse the same registry. Per-source
// enable flags and cron specs come from the environment (SCHEDULE_*), validated at
// startup; an invalid cron spec fails fast. The broker comes from RABBITMQ_URL,
// and each source reads the same producer config its standalone cmd does.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/evidencegate"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/schedule"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsarchive"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// crawlHTTPTimeout bounds each upstream API request a producer makes; a slow
// endpoint fails the request rather than stalling the run.
const crawlHTTPTimeout = 30 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("scheduler exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

// run builds the registry from config, wires the notifier, and runs the scheduler
// until a signal. Every broker client it opens is closed on shutdown via the
// returned closers. A registry with no enabled source is not an error: the
// always-on service idles until a signal rather than crash-looping a
// restart-on-exit container.
func run(logger *slog.Logger) error {
	scheduleCfg, err := config.LoadSchedule()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var reg schedule.Registry
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()

	if scheduleCfg.Wikipedia.Enabled {
		closer, regErr := registerWikipedia(&reg, scheduleCfg.Wikipedia, scheduleCfg.Jitter, logger)
		if regErr != nil {
			return regErr
		}
		closers = append(closers, closer)
	}
	if scheduleCfg.Factcheck.Enabled {
		closer, regErr := registerFactcheck(&reg, scheduleCfg.Factcheck, scheduleCfg.Jitter, logger)
		if regErr != nil {
			return regErr
		}
		closers = append(closers, closer)
	}
	if scheduleCfg.Scrutins.Enabled {
		closer, regErr := registerScrutins(&reg, scheduleCfg.Scrutins, scheduleCfg.Jitter, logger)
		if regErr != nil {
			return regErr
		}
		closers = append(closers, closer)
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	s := schedule.New(reg, notifier, logger)

	logger.InfoContext(ctx, "scheduler started",
		slog.Int("sources", reg.Len()),
		slog.Bool("wikipedia", scheduleCfg.Wikipedia.Enabled),
		slog.Bool("factcheck", scheduleCfg.Factcheck.Enabled),
		slog.Bool("scrutins", scheduleCfg.Scrutins.Enabled),
		slog.Duration("jitter", scheduleCfg.Jitter))

	if reg.Len() == 0 {
		// The always-on service idles when every source is disabled rather than
		// crash-looping; an operator enables a source with SCHEDULE_*_ENABLED.
		logger.InfoContext(ctx, "scheduler idle: no source enabled, waiting for signal")
	}

	s.Run(ctx)

	logger.InfoContext(ctx, "scheduler stopped")
	return nil
}

// registerWikipedia builds the Wikipedia crawl producer from its config and
// registers it on the given cron spec. It returns a closer for the broker client
// it opens.
func registerWikipedia(reg *schedule.Registry, src config.ScheduleSource, jitter time.Duration, logger *slog.Logger) (func(), error) {
	sched, err := schedule.ParseSpec(src.Cron)
	if err != nil {
		return nil, fmt.Errorf("wikipedia: %w", err)
	}
	crawlCfg, err := config.LoadCrawl()
	if err != nil {
		return nil, err
	}
	queueCfg, err := config.LoadCrawlQueue()
	if err != nil {
		return nil, err
	}
	gateCfg, err := config.LoadCrawlCheckworthy()
	if err != nil {
		return nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, err
	}
	closer := func() { _ = client.Close() }

	var gate wiki.Gate
	if gateCfg.Active() {
		gateClient, gateErr := evidencegate.New(evidencegate.Config{
			Provider:     llm.ProviderName(gateCfg.Provider),
			APIKey:       gateCfg.APIKey,
			GeminiAPIKey: gateCfg.GeminiAPIKey, DeepSeekAPIKey: gateCfg.DeepSeekAPIKey,
			Model: gateCfg.Model,
		})
		if gateErr != nil {
			closer()
			return nil, gateErr
		}
		gate = gateClient
	}

	categories := wiki.ShardCategories(crawlCfg.Categories, crawlCfg.Shards, crawlCfg.ShardIndex)
	if len(categories) == 0 {
		closer()
		return nil, fmt.Errorf("scheduler: wikipedia shard has no categories (shards=%d index=%d)", crawlCfg.Shards, crawlCfg.ShardIndex)
	}

	checkpoint, err := wiki.LoadCheckpoint(crawlCfg.CheckpointPath)
	if err != nil {
		closer()
		return nil, fmt.Errorf("scheduler: load crawl checkpoint: %w", err)
	}
	gateFailMode := wiki.GateFailOpen
	if crawlCfg.GateFailClosed {
		gateFailMode = wiki.GateFailClosed
	}

	producer := wikiProducer{
		run:    wiki.RunCrawl,
		logger: logger,
		src:    &wiki.APIClient{Corpus: crawlCfg.Project, HTTPClient: &http.Client{Timeout: crawlHTTPTimeout}},
		pub:    qPublisher{client: client},
		gate:   gate,
		cfg: wiki.CrawlConfig{
			Categories:      categories,
			Corpus:          crawlCfg.Corpus,
			Project:         crawlCfg.Project,
			MaxDepth:        crawlCfg.MaxDepth,
			MaxPages:        crawlCfg.MaxPages,
			IncludeBody:     crawlCfg.IncludeBody,
			MaxPriority:     queueCfg.MaxPriority,
			GateConcurrency: gateCfg.Concurrency,
			GateRPM:         gateCfg.RPM,
			GateFailMode:    gateFailMode,
			ErrorBudget:     crawlCfg.ErrorBudget,
			Checkpoint:      checkpoint,
		},
	}

	if err := reg.RegisterSchedule(producer.Name(), sched, jitter, producer); err != nil {
		closer()
		return nil, err
	}
	return closer, nil
}

// registerFactcheck builds the fact-check-archive producer and registers it.
func registerFactcheck(reg *schedule.Registry, src config.ScheduleSource, jitter time.Duration, logger *slog.Logger) (func(), error) {
	sched, err := schedule.ParseSpec(src.Cron)
	if err != nil {
		return nil, fmt.Errorf("factcheck: %w", err)
	}
	archiveCfg, err := config.LoadFactCheckArchive()
	if err != nil {
		return nil, err
	}
	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, err
	}
	closer := func() { _ = client.Close() }

	archive, err := factcheckarchive.New(factcheckarchive.Config{
		APIKey:       archiveCfg.APIKey,
		LanguageCode: archiveCfg.Language,
		MaxPriority:  queueCfg.MaxPriority,
	})
	if err != nil {
		closer()
		return nil, err
	}

	producer := factcheckProducer{
		client:   archive,
		logger:   logger,
		pub:      qPublisher{client: client},
		queries:  archiveCfg.Queries,
		maxPages: archiveCfg.MaxPages,
	}

	if err := reg.RegisterSchedule(producer.Name(), sched, jitter, producer); err != nil {
		closer()
		return nil, err
	}
	return closer, nil
}

// registerScrutins builds the scrutins-archive producer and registers it.
func registerScrutins(reg *schedule.Registry, src config.ScheduleSource, jitter time.Duration, logger *slog.Logger) (func(), error) {
	sched, err := schedule.ParseSpec(src.Cron)
	if err != nil {
		return nil, fmt.Errorf("scrutins: %w", err)
	}
	archiveCfg, err := config.LoadScrutinsArchive()
	if err != nil {
		return nil, err
	}
	queueCfg, err := config.LoadScrutinsQueue()
	if err != nil {
		return nil, err
	}

	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return nil, err
	}
	closer := func() { _ = client.Close() }

	producer, err := scrutinsarchive.New(scrutinsarchive.Config{
		Legislature: archiveCfg.Legislature,
		MarkerPath:  archiveCfg.MarkerPath,
		MaxPriority: queueCfg.MaxPriority,
	}, qPublisher{client: client}, logger)
	if err != nil {
		closer()
		return nil, err
	}

	if err := reg.RegisterSchedule(producer.Name(), sched, jitter, producer); err != nil {
		closer()
		return nil, err
	}
	return closer, nil
}
