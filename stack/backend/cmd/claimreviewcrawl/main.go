// Command claimreviewcrawl reads schema.org ClaimReview JSON-LD directly from an
// allowlist of vetted French fact-check outlets (sitemap-discovered, robots- and
// pacing-respecting) and publishes one self-contained curated-claim job per record
// to the fact-check queue, then exits. It extracts ONLY the categorical ClaimReview
// fields (claim, rating, review URL, date, outlet) — never article body text. It
// needs no database and no API key: the outlets are public. The broker comes from
// RABBITMQ_URL; CLAIMREVIEW_* tunes the user-agent, pacing, and per-outlet cap.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/claimreviewsite"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("claimreview crawl exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	sitesCfg, err := config.LoadClaimReviewSites()
	if err != nil {
		return err
	}
	queueCfg, err := config.LoadFactCheckQueue()
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

	outlets := make([]claimreviewsite.Outlet, 0, len(sitesCfg.Outlets))
	for _, o := range sitesCfg.Outlets {
		outlets = append(outlets, claimreviewsite.Outlet{Name: o.Name, Host: o.Host, Sitemap: o.Sitemap})
	}

	reader, err := claimreviewsite.New(claimreviewsite.Config{
		Outlets:          outlets,
		UserAgent:        sitesCfg.UserAgent,
		MinDelay:         sitesCfg.MinDelay,
		MaxURLsPerOutlet: sitesCfg.MaxURLsPerOutlet,
		MaxPriority:      queueCfg.MaxPriority,
	})
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "claimreview crawl started",
		slog.Int("outlets", len(outlets)),
		slog.String("user_agent", sitesCfg.UserAgent),
		slog.Duration("min_delay", sitesCfg.MinDelay),
		slog.String("queue", queueCfg.VersionedName()))

	p := claimreviewProducer{
		client:  reader,
		logger:  logger,
		pub:     qPublisher{client: client},
		outlets: len(outlets),
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	total, err := crawlnotify.RunWithAlerts(ctx, notifier, p)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "claimreview crawl finished",
		slog.Int("published_claims", total.New),
		slog.Int("skipped_claims", total.Skipped))
	return nil
}
