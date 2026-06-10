// Command wikisync builds and maintains the Wikipedia corpus in the
// verification store. Bulk mode downloads the corpus's multistream dump,
// extracts and chunks each article's lead section, upserts the chunks, then
// embeds every chunk and swaps the embedded corpus into place; re-running it is
// idempotent and an interrupted embed run resumes. Delta mode asks the
// MediaWiki RecentChanges API what changed since the stored checkpoint,
// refetches and re-embeds only those articles in place, removes deleted pages,
// and advances the checkpoint. The corpus comes from WIKI_CORPUS (default
// simplewiki), the database from DATABASE_URL, and embeddings from the Voyage
// API (EMBEDDING_API_KEY). With -dry-run, bulk ingests and reports the
// embedding cost estimate without calling the embedding API or swapping
// anything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// Embedding-retry backoff bounds: a rate-limited request waits at least a
// second and at most a minute between attempts.
const (
	embedRetryBaseDelay = 1 * time.Second
	embedRetryMaxDelay  = 60 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	mode := flag.String("mode", "bulk", "sync mode: bulk (full dump ingest) or delta (incremental catch-up)")
	dir := flag.String("dir", os.TempDir(), "directory for downloaded dump files")
	dryRun := flag.Bool("dry-run", false, "ingest and report the embedding cost estimate without embedding or swapping")
	flag.Parse()

	if err := run(logger, *mode, *dir, *dryRun); err != nil {
		logger.Error("wikisync failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, mode, dir string, dryRun bool) error {
	if mode != "bulk" && mode != "delta" {
		return fmt.Errorf("wikisync: unsupported mode %q (want bulk or delta)", mode)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wikiCfg, err := config.LoadWiki()
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

	if mode == "delta" {
		return runDelta(ctx, logger, store, wikiCfg, dryRun)
	}
	return runBulk(ctx, logger, store, wikiCfg, dir, dryRun)
}

// runBulk downloads the dump, ingests it, then either reports the embedding cost
// (dry-run) or embeds the corpus and swaps it into place.
func runBulk(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, dir string, dryRun bool) error {
	embedCfg, err := config.LoadWikiEmbed()
	if err != nil {
		return err
	}
	// The embedding provider key is only needed for a real embed run; a dry-run
	// estimates locally and must work without it.
	var embProvider config.Embedding
	if !dryRun {
		if embProvider, err = config.LoadEmbedding(); err != nil {
			return err
		}
	}

	logger.InfoContext(ctx, "downloading dump",
		slog.String("corpus", wikiCfg.Corpus), slog.String("dir", dir))
	var dl wiki.Downloader
	files, err := dl.Fetch(ctx, wikiCfg.Corpus, dir)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "dump downloaded",
		slog.String("dump", files.DumpPath), slog.String("version", files.Version))

	ingestStats, err := wiki.RunBulk(ctx, store, files, wikiCfg.Corpus)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "ingest complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("pages_seen", ingestStats.PagesSeen),
		slog.Int("pages_skipped", ingestStats.PagesSkipped),
		slog.Int("pages_stored", ingestStats.PagesStored),
		slog.Int("chunks", ingestStats.Chunks))

	if dryRun {
		est, err := wiki.EstimateBulkEmbed(ctx, store)
		if err != nil {
			return err
		}
		logger.InfoContext(ctx, "embedding cost estimate (dry run)",
			slog.String("corpus", wikiCfg.Corpus),
			slog.Int64("pages", est.Pages),
			slog.Int64("chunks", est.Chunks),
			slog.Int64("estimated_tokens", est.Tokens),
			slog.Float64("estimated_cost_usd", est.CostUSD))
		return nil
	}

	embedStats, err := wiki.RunBulkEmbed(ctx, store, newEmbedder(embProvider, embedCfg.MaxRetries), wiki.Config{
		Corpus:             wikiCfg.Corpus,
		BatchSize:          embedCfg.BatchSize,
		Concurrency:        embedCfg.Concurrency,
		MaintenanceWorkMem: embedCfg.MaintenanceWorkMem,
		MaxParallelWorkers: embedCfg.MaxParallelWorkers,
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "bulk embed complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("embedded_chunks", embedStats.Embedded))
	return nil
}

// runDelta catches the corpus up to the live wiki incrementally via the
// MediaWiki API, embedding only what changed.
func runDelta(ctx context.Context, logger *slog.Logger, store *postgres.Store, wikiCfg config.Wiki, dryRun bool) error {
	if dryRun {
		return errors.New("wikisync: -dry-run is only supported for bulk mode")
	}
	deltaCfg, err := config.LoadWikiDelta()
	if err != nil {
		return err
	}
	embedCfg, err := config.LoadWikiEmbed()
	if err != nil {
		return err
	}
	embProvider, err := config.LoadEmbedding()
	if err != nil {
		return err
	}

	api := &wiki.APIClient{Corpus: wikiCfg.Corpus}
	logger.InfoContext(ctx, "starting delta sync", slog.String("corpus", wikiCfg.Corpus))
	stats, err := wiki.RunDelta(ctx, store, api, newEmbedder(embProvider, embedCfg.MaxRetries), wiki.DeltaConfig{
		Corpus:        wikiCfg.Corpus,
		RetentionDays: deltaCfg.RetentionDays,
		BulkFraction:  deltaCfg.BulkFraction,
		BatchSize:     embedCfg.BatchSize,
		Concurrency:   embedCfg.Concurrency,
	}, time.Now().UTC())
	if err != nil {
		return err
	}
	if stats.RecommendBulk {
		logger.WarnContext(ctx, "change set exceeds the bulk threshold; a -mode=bulk re-run would rebuild the index more cleanly",
			slog.String("corpus", wikiCfg.Corpus))
	}
	logger.InfoContext(ctx, "delta sync complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("pages_changed", stats.Changed),
		slog.Int("pages_skipped", stats.Skipped),
		slog.Int("pages_deleted", stats.Deleted),
		slog.Int("embedded_chunks", stats.Embedded))
	return nil
}

// newEmbedder builds the Voyage embedding client wrapped in the shared retry
// decorator both sync modes use.
func newEmbedder(p config.Embedding, maxRetries int) *embed.RetryClient {
	return embed.WithRetry(
		embed.New(embed.Config{APIKey: p.APIKey, Model: p.Model, Dim: p.Dim}),
		embed.RetryConfig{MaxAttempts: maxRetries, BaseDelay: embedRetryBaseDelay, MaxDelay: embedRetryMaxDelay},
	)
}
