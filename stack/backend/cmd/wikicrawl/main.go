// Command wikicrawl walks one or more Wikipedia categories over the MediaWiki
// Action API, chunks each article's lead (and optionally body), and publishes one
// self-contained chunk job per chunk to the crawl queue, then exits. It needs no
// database: every field a live wiki_chunks row requires travels in the message,
// so the crawl worker (cmd/crawlworker) drains the queue into the corpus
// independently. The broker comes from RABBITMQ_URL; CRAWL_* selects the
// categories and shape.
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
	"github.com/verovec/truth-in-stream/backend/internal/evidencegate"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// crawlHTTPTimeout bounds each Action API request; a slow endpoint fails the
// request rather than stalling the whole crawl.
const crawlHTTPTimeout = 30 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("wiki crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	crawlCfg, err := config.LoadCrawl()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadCrawlQueue()
	if err != nil {
		return err
	}
	gateCfg, err := config.LoadCrawlCheckworthy()
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

	api := &wiki.APIClient{
		Corpus:     crawlCfg.Project,
		HTTPClient: &http.Client{Timeout: crawlHTTPTimeout},
	}

	// A nil gate disables fact-checkability filtering (publish everything); only
	// an active gate is assigned, so the interface stays an untyped nil rather
	// than a typed-nil that RunCrawl would mistake for a live gate.
	var gate wiki.Gate
	if gateCfg.Active() {
		client, err := evidencegate.New(evidencegate.Config{APIKey: gateCfg.APIKey, Model: gateCfg.Model})
		if err != nil {
			return err
		}
		gate = client
	}

	logger.InfoContext(ctx, "wiki crawl started",
		slog.Any("categories", crawlCfg.Categories),
		slog.String("corpus", crawlCfg.Corpus),
		slog.String("queue", queueCfg.VersionedName()),
		slog.Int("max_depth", crawlCfg.MaxDepth),
		slog.Int("max_pages", crawlCfg.MaxPages),
		slog.Bool("include_body", crawlCfg.IncludeBody),
		slog.Bool("checkworthy_gate", gateCfg.Active()))

	stats, err := wiki.RunCrawl(ctx, logger, api, qPublisher{client: client}, gate, wiki.CrawlConfig{
		Categories:      crawlCfg.Categories,
		Corpus:          crawlCfg.Corpus,
		Project:         crawlCfg.Project,
		MaxDepth:        crawlCfg.MaxDepth,
		MaxPages:        crawlCfg.MaxPages,
		IncludeBody:     crawlCfg.IncludeBody,
		MaxPriority:     queueCfg.MaxPriority,
		GateConcurrency: gateCfg.Concurrency,
		GateRPM:         gateCfg.RPM,
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "wiki crawl finished",
		slog.Int("pages", stats.Pages),
		slog.Int("published_chunks", stats.Published),
		slog.Int("dropped_chunks", stats.Dropped))
	return nil
}
