// Command claimskgseed is the one-time ClaimsKG seed importer. It reads a ClaimsKG
// CSV/TSV export (a large, 2023-vintage snapshot of internationally fact-checked
// claims) and publishes one self-contained curated-claim job per row to the
// fact-check queue for the existing worker to embed and upsert into political_claims.
// It is deliberately gated: it does nothing unless CLAIMSKG_SEED_ENABLED=true and
// CLAIMSKG_SEED_FILE points at an export, so the stale snapshot is only ingested on a
// considered operator action. Records are marked with ClaimsKG provenance and the
// vintage; the broker comes from RABBITMQ_URL.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/claimskg"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("claimskg seed exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	seedCfg, err := config.LoadClaimsKGSeed()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !seedCfg.Enabled {
		logger.InfoContext(ctx, "claimskg seed disabled, nothing to do (set CLAIMSKG_SEED_ENABLED=true and CLAIMSKG_SEED_FILE)")
		return nil
	}
	if seedCfg.SeedFile == "" {
		return fmt.Errorf("claimskg seed enabled but CLAIMSKG_SEED_FILE is empty")
	}

	queueCfg, err := config.LoadFactCheckQueue()
	if err != nil {
		return err
	}
	client, err := queue.New(queueCfg.ClientConfig(0))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	delim := rune(0)
	if seedCfg.TSV {
		delim = '\t'
	}
	seeder, err := claimskg.New(claimskg.Config{
		Enabled:     seedCfg.Enabled,
		Vintage:     seedCfg.Vintage,
		MaxPriority: queueCfg.MaxPriority,
		Delimiter:   delim,
	})
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "claimskg seed started",
		slog.String("seed_file", seedCfg.SeedFile),
		slog.String("vintage", seedCfg.Vintage),
		slog.String("queue", queueCfg.VersionedName()))

	p := claimskgProducer{
		seeder:   seeder,
		logger:   logger,
		pub:      qPublisher{client: client},
		seedFile: seedCfg.SeedFile,
		vintage:  seedCfg.Vintage,
	}

	alerts := config.LoadCrawlAlerts()
	notifier := crawlnotify.FleetNotifier(ctx, logger, alerts.WebhookURL, alerts.RunMetricsNamespace)
	total, err := crawlnotify.RunWithAlerts(ctx, notifier, p)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "claimskg seed finished",
		slog.Int("published_claims", total.New),
		slog.Int("skipped_claims", total.Skipped))
	return nil
}
