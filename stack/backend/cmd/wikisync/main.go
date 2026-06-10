// Command wikisync ingests a Wikipedia corpus into the verification store.
// Bulk mode downloads the corpus's multistream dump, extracts and chunks each
// article's lead section, and upserts the chunks; re-running it is
// idempotent. The corpus comes from WIKI_CORPUS (default simplewiki), the
// database from DATABASE_URL. Embeddings are filled by the separate
// bulk-embedding pipeline.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	mode := flag.String("mode", "bulk", "sync mode: bulk (delta arrives with the delta-sync work)")
	dir := flag.String("dir", os.TempDir(), "directory for downloaded dump files")
	flag.Parse()

	if err := run(logger, *mode, *dir); err != nil {
		logger.Error("wikisync failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, mode, dir string) error {
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

	stats, err := wiki.RunBulk(ctx, store, files, wikiCfg.Corpus)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "bulk sync complete",
		slog.String("corpus", wikiCfg.Corpus),
		slog.Int("pages_seen", stats.PagesSeen),
		slog.Int("pages_skipped", stats.PagesSkipped),
		slog.Int("pages_stored", stats.PagesStored),
		slog.Int("chunks", stats.Chunks))
	return nil
}
