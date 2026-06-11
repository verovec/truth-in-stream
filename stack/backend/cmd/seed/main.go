// Command seed loads the local-development dataset - curated claims, a small
// Wikipedia evidence subset, and a precomputed demo-video result set - into the
// store. It reads embeddings from a committed cache so a full reseed needs no
// external API key; -refresh regenerates that cache from the fixtures.
//
// Usage:
//
//	seed                 seed every dataset from the committed cache (offline)
//	seed -claims -wiki   seed only the named datasets
//	seed -videos         seed only the curated sample videos
//	seed -refresh        regenerate the embedding cache via Voyage, then exit
//	seed -refresh -offline   regenerate the cache with deterministic placeholder
//	                         vectors (no API key); used to bootstrap fixtures
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/ingest"
	"github.com/verovec/truth-in-stream/backend/internal/seed"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/storage"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

const (
	defaultSeedDir   = "seed"
	defaultCachePath = "seed/embeddings.cache.jsonl"
	// defaultMediaCacheDir holds fetched sample media so a later reseed reuses
	// the bytes instead of re-downloading. It sits under the bind-mounted seed
	// tree so it survives across `docker compose run --rm seed` invocations.
	defaultMediaCacheDir = "seed/media-cache"
	// sampleMediaFetchTimeout bounds the one-time download of a sample clip; a
	// stuck fetch is skipped (the record still seeds) rather than hanging reset.
	sampleMediaFetchTimeout = 5 * time.Minute
	claimsFile              = "claims.json"
	wikiFile                = "wiki_chunks.json"
	demoFile                = "demo_results.json"
)

// datasets selects which fixtures to seed. When none are requested on the
// command line, every dataset is seeded.
type datasets struct {
	claims bool
	wiki   bool
	demo   bool
	videos bool
}

func (d datasets) any() bool { return d.claims || d.wiki || d.demo || d.videos }

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
	offline := flag.Bool("offline", false, "with -refresh, fill the cache with deterministic placeholder vectors instead of calling Voyage (no API key); has no effect when seeding, which is always offline")
	doClaims := flag.Bool("claims", false, "seed curated claims")
	doWiki := flag.Bool("wiki", false, "seed the Wikipedia evidence subset")
	doDemo := flag.Bool("demo", false, "seed the demo-video results")
	doVideos := flag.Bool("videos", false, "seed the curated sample videos (records plus best-effort media)")
	seedDir := flag.String("seed-dir", defaultSeedDir, "directory holding the seed fixtures")
	cachePath := flag.String("cache", defaultCachePath, "embedding cache file")
	mediaCacheDir := flag.String("media-cache", defaultMediaCacheDir, "directory caching fetched sample media across reseeds")
	flag.Parse()

	sel := datasets{claims: *doClaims, wiki: *doWiki, demo: *doDemo, videos: *doVideos}
	if !sel.any() {
		sel = datasets{claims: true, wiki: true, demo: true, videos: true}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *refresh {
		return refreshCache(ctx, logger, *seedDir, *cachePath, *offline)
	}
	if *offline {
		logger.WarnContext(ctx, "-offline has no effect without -refresh: seeding is always offline")
	}
	return seedAll(ctx, logger, sel, *seedDir, *cachePath, *mediaCacheDir)
}

// embeddingModel returns the embedding model name used for cache keys, honoring
// EMBEDDING_MODEL and defaulting to voyage-4-large.
func embeddingModel() string {
	if m := os.Getenv("EMBEDDING_MODEL"); m != "" {
		return m
	}
	return config.DefaultEmbeddingModel
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

// seedAll opens the store and seeds the selected datasets from the committed
// embedding cache. Seeding is strictly offline, so the cache is read-only here.
func seedAll(ctx context.Context, logger *slog.Logger, sel datasets, seedDir, cachePath, mediaCacheDir string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	// Only the text datasets need the embedding cache; seeding videos alone must
	// not require the committed cache to be present, so the cache stays local to
	// this branch. The model is resolved once so the embedder and any cache-miss
	// hint name the same value.
	if sel.claims || sel.wiki {
		cache, err := embed.LoadCache(cachePath)
		if err != nil {
			return err
		}
		model := embeddingModel()
		embedder := seedEmbedder(cache, model)
		if sel.claims {
			if err := seedClaims(ctx, logger, store, embedder, seedDir); err != nil {
				return cacheMissHint(err, model)
			}
		}
		if sel.wiki {
			if err := seedWiki(ctx, logger, store, embedder, seedDir); err != nil {
				return cacheMissHint(err, model)
			}
		}
	}
	if sel.demo {
		if err := seedDemo(ctx, logger, store, seedDir); err != nil {
			return err
		}
	}
	if sel.videos {
		if err := seedVideos(ctx, logger, store, mediaCacheDir); err != nil {
			return err
		}
	}
	return nil
}

// seedEmbedder wraps the committed cache as a strictly offline embedder (nil
// filler) keyed under model. Normal seeding never calls Voyage, so a stray or
// invalid EMBEDDING_API_KEY can never turn `make seed` into a live request that
// fails with a provider error. A cache miss is a hard error, surfaced with
// guidance by cacheMissHint; rebuilding the cache is the explicit job of
// `seed -refresh`.
func seedEmbedder(cache *embed.Cache, model string) *embed.Cached {
	return embed.NewCached(cache, model, nil)
}

// cacheMissHint augments a committed-cache miss with actionable guidance. The
// wrapped embed error already names the missing text; this is the single owner
// of the seed-workflow remediation, so the guidance is not duplicated. A miss
// during offline seeding means either a fixture's text changed, or
// EMBEDDING_MODEL no longer matches the model the committed cache was built
// under. Other errors pass through unchanged.
func cacheMissHint(err error, model string) error {
	if errors.Is(err, embed.ErrCacheMiss) {
		return fmt.Errorf("%w\nno committed embedding for that fixture under model %q: "+
			"a fixture's text changed, or EMBEDDING_MODEL no longer matches the model the committed "+
			"cache was built under (its default is %q) - run `make refresh-embeddings` (needs a valid "+
			"EMBEDDING_API_KEY) to rebuild the cache", err, model, config.DefaultEmbeddingModel)
	}
	return err
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

// seedVideos upserts the curated sample records and best-effort places their
// media in object storage. The records are keyed by their UUID id and are
// independent of the demo results, which key segment results by the processing
// id derived from the demo source filename (service.VideoID) and have no videos
// row; the two stay separate on purpose. Storage configuration is required (the
// media bytes need somewhere to go); the external clip fetch is best-effort and
// SAMPLE_VIDEO_URL overrides the default clip.
func seedVideos(ctx context.Context, logger *slog.Logger, store *postgres.Store, mediaCacheDir string) error {
	storageCfg, err := config.LoadStorage()
	if err != nil {
		return err
	}
	media, err := storage.New(ctx, storage.Config{
		Endpoint:       storageCfg.Endpoint,
		PublicEndpoint: storageCfg.PublicEndpoint,
		Region:         storageCfg.Region,
		Bucket:         storageCfg.Bucket,
		AccessKey:      storageCfg.AccessKey,
		SecretKey:      storageCfg.SecretKey,
		UsePathStyle:   storageCfg.UsePathStyle,
		PutTTL:         storageCfg.PutTTL,
		GetTTL:         storageCfg.GetTTL,
	})
	if err != nil {
		return err
	}

	samples := seed.Samples(os.Getenv("SAMPLE_VIDEO_URL"))
	fetcher := httpMediaFetcher{client: &http.Client{Timeout: sampleMediaFetchTimeout}}
	if err := seed.InsertSampleVideos(ctx, store, media, fetcher, mediaCacheDir, samples, logger); err != nil {
		return err
	}
	logger.InfoContext(ctx, "seeded sample videos", slog.Int("samples", len(samples)))
	return nil
}

// httpMediaFetcher fetches sample media over HTTP. It is the wiring-layer
// implementation of seed.MediaFetcher; the seed package stays free of transport
// types and is exercised in tests with a fake fetcher.
type httpMediaFetcher struct {
	client *http.Client
}

// Fetch issues a GET for url and returns the response body. A non-2xx status is
// an error so a captive-portal or error page is never cached as media. The
// caller owns the returned reader and MUST close it.
func (f httpMediaFetcher) Fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("seed: build media request: %w", err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("seed: fetch media: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("seed: fetch media: unexpected status %s", resp.Status)
	}
	return resp.Body, nil
}
