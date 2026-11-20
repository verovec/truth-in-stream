// Command sdmxcrawl is the SDMX connector's one-shot producer: it fetches the
// curated politically-relevant macro series from the selected SDMX institutions
// (the European Central Bank and the OECD today), renders each observation into a
// self-contained French evidence passage, upserts it into the live evidence
// corpus un-embedded with per-institution provenance, and publishes one prioritized
// embedding job per passage to the RabbitMQ queue (RABBITMQ_URL). The existing
// embedding-worker fleet drains the queue and fills the vectors in place - the same
// bulk-into-live path statsingest uses, so a broad sweep scales by worker replica
// count rather than one synchronous Voyage burst. It is idempotent on the (series,
// period) provenance key, so re-running refreshes the figures without duplicating
// passages and re-publishes only the still-unembedded ones. Each institution writes
// under its own corpus label, so a retrieved passage's publisher is identifiable.
//
// The ECB and OECD SDMX endpoints need no key. Embedding happens in the fleet, so
// this producer needs no Voyage key; it needs the broker URL (RABBITMQ_URL) and the
// database (DATABASE_URL). SDMX_SOURCES / SDMX_START_PERIOD / SDMX_END_PERIOD are
// optional, non-secret knobs.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/source/sdmx"
	"github.com/verovec/truth-in-stream/backend/internal/stats"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("sdmxcrawl failed", slog.Any("err", err))
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
	producerCfg, err := config.LoadWikiProducer()
	if err != nil {
		return err
	}
	sdmxCfg, err := config.LoadSDMX()
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

	client, err := queue.New(queueCfg.ClientConfig(queueCfg.Prefetch))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	sources, err := buildSources(sdmxCfg)
	if err != nil {
		return err
	}

	statsCfg := stats.Config{
		MaxPriority:      queueCfg.MaxPriority,
		EnqueueBatchSize: producerCfg.EnqueueBatchSize,
	}

	// Each institution writes a distinct, independent corpus, so a failure in one
	// (a transient outage or a stale series key at one provider) must not block the
	// others. Log and continue per source, then fail the run if any source failed so
	// a scheduled job still surfaces the error - the same isolation statsingest uses.
	var upserted, published int
	var failed []string
	for _, source := range sources {
		st, err := stats.Run(ctx, logger, source, store, qPublisher{client: client}, statsCfg)
		if err != nil {
			failed = append(failed, source.Corpus())
			logger.ErrorContext(ctx, "sdmxcrawl source failed",
				slog.String("corpus", source.Corpus()), slog.Any("err", err))
			continue
		}
		upserted += st.Upserted
		published += st.Published
		logger.InfoContext(ctx, "sdmxcrawl source complete",
			slog.String("corpus", source.Corpus()),
			slog.Int("upserted", st.Upserted),
			slog.Int("published", st.Published))
	}

	logger.InfoContext(ctx, "sdmxcrawl complete; the worker fleet fills the vectors in place",
		slog.Any("sources", sdmxCfg.Sources), slog.Int("upserted", upserted), slog.Int("published", published))
	if len(failed) > 0 {
		return fmt.Errorf("sdmxcrawl: %d source(s) failed: %v", len(failed), failed)
	}
	return nil
}

// buildSources constructs one stats.Source per selected institution, each with its
// own SDMX client (endpoint, rate limit) and curated, windowed series list. An
// unknown source name is a config error caught before any network call.
func buildSources(cfg config.SDMX) ([]stats.Source, error) {
	window := sdmx.Window{Start: cfg.Start, End: cfg.End}
	sources := make([]stats.Source, 0, len(cfg.Sources))
	for _, name := range cfg.Sources {
		switch name {
		case "eurostat":
			sources = append(sources, sdmx.NewSource(sdmx.New(sdmx.EurostatEndpoint()), domain.StatCorpus, sdmx.EurostatSpecs(window)))
		case "ecb":
			sources = append(sources, sdmx.NewSource(sdmx.New(sdmx.ECBEndpoint()), domain.ECBStatCorpus, sdmx.ECBSpecs(window)))
		case "oecd":
			sources = append(sources, sdmx.NewSource(sdmx.New(sdmx.OECDEndpoint()), domain.OECDStatCorpus, sdmx.OECDSpecs(window)))
		default:
			return nil, fmt.Errorf("sdmxcrawl: unknown source %q", name)
		}
	}
	return sources, nil
}
