// Command wikisync builds the Wikipedia corpus in the verification store.
// Bulk mode downloads the corpus's multistream dump, extracts and chunks each
// article's lead section, upserts the chunks, then embeds every chunk and
// swaps the embedded corpus into place; re-running it is idempotent and an
// interrupted embed run resumes. The corpus comes from WIKI_CORPUS (default
// simplewiki), the database from DATABASE_URL, and embeddings from the Voyage
// API (EMBEDDING_API_KEY). With -dry-run it ingests and reports the embedding
// cost estimate without calling the embedding API or swapping anything.
package main

import (
	"context"
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

	mode := flag.String("mode", "bulk", "sync mode: bulk (delta arrives with the delta-sync work)")
	dir := flag.String("dir", os.TempDir(), "directory for downloaded dump files")
	dryRun := flag.Bool("dry-run", false, "ingest and report the embedding cost estimate without embedding or swapping")
	flag.Parse()

	if err := run(logger, *mode, *dir, *dryRun); err != nil {
		logger.Error("wikisync failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, mode, dir string, dryRun bool) error {
	if mode != "bulk" {
		return fmt.Errorf("wikisync: unsupported mode %q (only bulk exists yet)", mode)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wikiCfg, err := config.LoadWiki()
	if err != nil {
		return err
	}
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

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

	embedder := embed.WithRetry(
		embed.New(embed.Config{APIKey: embProvider.APIKey, Model: embProvider.Model, Dim: embProvider.Dim}),
		embed.RetryConfig{MaxAttempts: embedCfg.MaxRetries, BaseDelay: embedRetryBaseDelay, MaxDelay: embedRetryMaxDelay},
	)
	embedStats, err := wiki.RunBulkEmbed(ctx, store, embedder, wiki.Config{
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
