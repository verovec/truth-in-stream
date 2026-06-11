// Command seed loads the local-development dataset - curated claims, a small
// Wikipedia evidence subset, and a precomputed demo-video result set - into the
// store. It reads embeddings from a committed cache so a full reseed needs no
// external API key; -refresh regenerates that cache from the fixtures.
//
// Usage:
//
//	seed                 seed every dataset from the committed cache (offline)
//	seed -claims -wiki   seed only the named datasets
//	seed -refresh        regenerate the embedding cache via Voyage, then exit
//	seed -refresh -offline   regenerate the cache with deterministic placeholder
//	                         vectors (no API key); used to bootstrap fixtures
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/ingest"
	"github.com/verovec/truth-in-stream/backend/internal/seed"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

const (
	// defaultEmbeddingModel mirrors config.defaultEmbeddingModel; the cache key
	// embeds the model, so seed and refresh must agree on it offline.
	defaultEmbeddingModel = "voyage-4"
	defaultSeedDir        = "seed"
	defaultCachePath      = "seed/embeddings.cache.jsonl"
	claimsFile            = "claims.json"
	wikiFile              = "wiki_chunks.json"
	demoFile              = "demo_results.json"
)

// datasets selects which fixtures to seed. When none are requested on the
// command line, every dataset is seeded.
type datasets struct {
	claims bool
	wiki   bool
	demo   bool
}

func (d datasets) any() bool { return d.claims || d.wiki || d.demo }

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("seed failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	refresh := flag.Bool("refresh", false, "regenerate the embedding cache from fixtures, then exit")
	offline := flag.Bool("offline", false, "use deterministic placeholder embeddings instead of calling Voyage")
	doClaims := flag.Bool("claims", false, "seed curated claims")
	doWiki := flag.Bool("wiki", false, "seed the Wikipedia evidence subset")
	doDemo := flag.Bool("demo", false, "seed the demo-video results")
	seedDir := flag.String("seed-dir", defaultSeedDir, "directory holding the seed fixtures")
	cachePath := flag.String("cache", defaultCachePath, "embedding cache file")
	flag.Parse()

	sel := datasets{claims: *doClaims, wiki: *doWiki, demo: *doDemo}
	if !sel.any() {
		sel = datasets{claims: true, wiki: true, demo: true}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *refresh {
		return refreshCache(ctx, logger, *seedDir, *cachePath, *offline)
	}
	return seedAll(ctx, logger, sel, *seedDir, *cachePath, *offline)
}

// embeddingModel returns the embedding model name used for cache keys, honoring
// EMBEDDING_MODEL and defaulting to voyage-4.
func embeddingModel() string {
	if m := os.Getenv("EMBEDDING_MODEL"); m != "" {
		return m
	}
	return defaultEmbeddingModel
}

// refreshCache re-embeds every fixture document text from scratch and writes the
// cache file. With -offline it uses deterministic placeholder vectors; otherwise
// it calls Voyage and requires EMBEDDING_API_KEY.
func refreshCache(ctx context.Context, logger *slog.Logger, seedDir, cachePath string, offline bool) error {
	model := embeddingModel()
	filler, err := refreshFiller(offline)
	if err != nil {
		return err
	}

	texts, err := documentTexts(seedDir)
	if err != nil {
		return err
	}

	cache := embed.NewCache()
	cached := embed.NewCached(cache, model, filler)
	if _, err := cached.EmbedDocuments(ctx, texts); err != nil {
		return fmt.Errorf("seed: refresh embeddings: %w", err)
	}
	if err := cache.Save(cachePath); err != nil {
		return err
	}
	logger.InfoContext(ctx, "refreshed embedding cache",
		slog.String("cache", cachePath),
		slog.Int("entries", cache.Len()),
		slog.Bool("offline", offline))
	return nil
}

// refreshFiller builds the embedder a refresh fills the cache with: the
// deterministic placeholder offline, or a real Voyage client otherwise.
func refreshFiller(offline bool) (embed.Filler, error) {
	if offline {
		return embed.NewDeterministic(domain.EmbeddingDim), nil
	}
	emb, err := config.LoadEmbedding()
	if err != nil {
		return nil, err
	}
	return embed.New(embed.Config{APIKey: emb.APIKey, Model: emb.Model, Dim: emb.Dim}), nil
}

// documentTexts returns every fixture text that needs an embedding: the curated
// claim statements and the Wikipedia chunk contents. Demo-result matches are
// precomputed and carry no embeddings.
func documentTexts(seedDir string) ([]string, error) {
	claimsF, err := os.Open(filepath.Join(seedDir, claimsFile))
	if err != nil {
		return nil, fmt.Errorf("seed: open claims fixture: %w", err)
	}
	defer func() { _ = claimsF.Close() }()
	claims, err := ingest.LoadSeed(claimsF)
	if err != nil {
		return nil, err
	}

	wikiF, err := os.Open(filepath.Join(seedDir, wikiFile))
	if err != nil {
		return nil, fmt.Errorf("seed: open wiki fixture: %w", err)
	}
	defer func() { _ = wikiF.Close() }()
	chunks, err := seed.LoadWikiChunks(wikiF)
	if err != nil {
		return nil, err
	}

	texts := make([]string, 0, len(claims)+len(chunks))
	for _, c := range claims {
		texts = append(texts, c.Text)
	}
	for _, c := range chunks {
		texts = append(texts, c.Content)
	}
	return texts, nil
}

// seedAll opens the store and seeds the selected datasets, persisting any new
// embeddings filled during the run back to the cache.
func seedAll(ctx context.Context, logger *slog.Logger, sel datasets, seedDir, cachePath string, offline bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	cache, err := embed.LoadCache(cachePath)
	if err != nil {
		return err
	}
	embedder, err := seedEmbedder(cache, offline)
	if err != nil {
		return err
	}

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	if sel.claims {
		if err := seedClaims(ctx, logger, store, embedder, seedDir); err != nil {
			return err
		}
	}
	if sel.wiki {
		if err := seedWiki(ctx, logger, store, embedder, seedDir); err != nil {
			return err
		}
	}
	if sel.demo {
		if err := seedDemo(ctx, logger, store, seedDir); err != nil {
			return err
		}
	}

	if cache.Dirty() {
		if err := cache.Save(cachePath); err != nil {
			return err
		}
		logger.InfoContext(ctx, "embedding cache updated with new entries", slog.String("cache", cachePath))
	}
	return nil
}

// seedEmbedder wraps the cache with a filler for cache misses: a real Voyage
// client when EMBEDDING_API_KEY is set and -offline is not, otherwise nil so a
// miss fails fast with guidance to run -refresh.
func seedEmbedder(cache *embed.Cache, offline bool) (*embed.Cached, error) {
	if offline || os.Getenv("EMBEDDING_API_KEY") == "" {
		return embed.NewCached(cache, embeddingModel(), nil), nil
	}
	emb, err := config.LoadEmbedding()
	if err != nil {
		return nil, err
	}
	return embed.NewCached(cache, emb.Model, embed.New(embed.Config{APIKey: emb.APIKey, Model: emb.Model, Dim: emb.Dim})), nil
}

func seedClaims(ctx context.Context, logger *slog.Logger, store *postgres.Store, embedder *embed.Cached, seedDir string) error {
	f, err := os.Open(filepath.Join(seedDir, claimsFile))
	if err != nil {
		return fmt.Errorf("seed: open claims fixture: %w", err)
	}
	defer func() { _ = f.Close() }()
	claims, err := ingest.LoadSeed(f)
	if err != nil {
		return err
	}
	n, err := ingest.Run(ctx, store, embedder, claims, 0)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "seeded claims", slog.Int("claims", n))
	return nil
}

func seedWiki(ctx context.Context, logger *slog.Logger, store *postgres.Store, embedder *embed.Cached, seedDir string) error {
	f, err := os.Open(filepath.Join(seedDir, wikiFile))
	if err != nil {
		return fmt.Errorf("seed: open wiki fixture: %w", err)
	}
	defer func() { _ = f.Close() }()
	chunks, err := seed.LoadWikiChunks(f)
	if err != nil {
		return err
	}
	if err := seed.InsertWikiChunks(ctx, store, embedder, chunks); err != nil {
		return err
	}
	logger.InfoContext(ctx, "seeded wiki chunks", slog.Int("chunks", len(chunks)))
	return nil
}

func seedDemo(ctx context.Context, logger *slog.Logger, store *postgres.Store, seedDir string) error {
	f, err := os.Open(filepath.Join(seedDir, demoFile))
	if err != nil {
		return fmt.Errorf("seed: open demo fixture: %w", err)
	}
	defer func() { _ = f.Close() }()
	demo, err := seed.LoadDemoResults(f)
	if err != nil {
		return err
	}
	videoID := service.VideoID(demo.Source)
	if err := seed.InsertDemoResults(ctx, store, videoID, demo.Segments); err != nil {
		return err
	}
	logger.InfoContext(ctx, "seeded demo results",
		slog.String("source", demo.Source),
		slog.String("video_id", videoID),
		slog.Int("segments", len(demo.Segments)))
	return nil
}
