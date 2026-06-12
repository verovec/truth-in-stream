// Command server is the truth-in-stream backend API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/handler"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/storage"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
	"github.com/verovec/truth-in-stream/backend/internal/ytdlp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	transcription, err := config.LoadTranscription()
	if err != nil {
		return err
	}
	embedding, err := config.LoadEmbedding()
	if err != nil {
		return err
	}
	matchCfg, err := config.LoadMatch()
	if err != nil {
		return err
	}
	precheckCfg, err := config.LoadPrecheck()
	if err != nil {
		return err
	}
	debugSearchCfg, err := config.LoadDebugSearch()
	if err != nil {
		return err
	}
	authCfg, err := config.LoadAuth()
	if err != nil {
		return err
	}
	credentials, err := service.NewCredentials(authCfg.Email, authCfg.PasswordHash)
	if err != nil {
		return err
	}
	auth := handler.AuthConfig{
		Credentials:  credentials,
		Sessions:     service.NewSessions(authCfg.SessionSecret, authCfg.SessionTTL),
		SecureCookie: authCfg.SecureCookie,
		// 5 attempts then one every 30s per client: invisible to the single
		// operator, glacial for a brute-force run.
		LoginLimiter: middleware.NewRateLimiter(rate.Every(30*time.Second), 5),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	storageCfg, err := config.LoadStorage()
	if err != nil {
		return err
	}
	uploadCfg, err := config.LoadUpload()
	if err != nil {
		return err
	}
	youtubeCfg, err := config.LoadYouTube()
	if err != nil {
		return err
	}

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	mediaStore, err := storage.New(ctx, storage.Config{
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

	videoSvc, err := service.NewVideoService(store, mediaStore, service.VideoConfig{
		MaxUploadBytes: uploadCfg.MaxBytes,
	})
	if err != nil {
		return err
	}

	youtubeSvc, err := service.NewIngestService(store, mediaStore, ytdlp.New(ytdlp.Config{
		BinaryPath: youtubeCfg.BinaryPath,
		MaxBytes:   youtubeCfg.MaxBytes,
	}), service.IngestConfig{
		MaxDownloadBytes: youtubeCfg.MaxBytes,
		DownloadTimeout:  youtubeCfg.Timeout,
	}, logger)
	if err != nil {
		return err
	}

	health := service.NewHealthChecker(store)

	embedder := embed.New(embed.Config{APIKey: embedding.APIKey, Model: embedding.Model, Dim: embedding.Dim})
	matcher, err := service.NewMatcher(embedder, store, store, service.MatcherConfig{
		TopK:              matchCfg.TopK,
		ScoreThreshold:    matchCfg.ScoreThreshold,
		EvidenceTopK:      matchCfg.EvidenceTopK,
		EvidenceThreshold: matchCfg.EvidenceThreshold,
		MaxResults:        matchCfg.MaxResults,
		EmbedConcurrency:  matchCfg.EmbedConcurrency,
		Timeout:           matchCfg.Timeout,
	})
	if err != nil {
		return err
	}

	prechecker, err := buildPrechecker(precheckCfg, embedder, store, store)
	if err != nil {
		return err
	}

	debugSearch, err := buildDebugSearch(debugSearchCfg, embedder, store)
	if err != nil {
		return err
	}

	segmentMatcher := service.NewSegmentMatchAdapter(matcher)
	liveAnalyzer, err := service.NewLiveAnalyzer(service.LiveAnalyzerConfig{
		Stream:     liveStream(transcription, logger),
		Matcher:    segmentMatcher,
		Prechecker: prechecker,
		Logger:     logger,
	})
	if err != nil {
		return err
	}

	liveOrigins := liveAllowedOrigins(cfg.CORSAllowedOrigin)
	if cfg.CORSAllowedOrigin != "" && len(liveOrigins) == 0 {
		logger.Warn("live websocket enforces same-origin: CORS_ALLOWED_ORIGIN has no parseable host",
			slog.String("cors_allowed_origin", cfg.CORSAllowedOrigin))
	}

	apiHandler := handler.NewMux(health, videoSvc, youtubeSvc, liveAnalyzer, liveOrigins, debugSearch, cfg.DemoMediaDir, auth, logger)
	if cfg.CORSAllowedOrigin != "" {
		apiHandler = middleware.CORS(cfg.CORSAllowedOrigin)(apiHandler)
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: apiHandler,
		// Tight server-wide bounds keep slow connections out; the live WebSocket
		// handler owns its own long-lived read/write deadlines after the upgrade.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		stop()
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// liveAllowedOrigins derives the WebSocket origin allow-list from the CORS
// origin: empty in production (frontend and API share an origin, so same-origin
// is enforced) and the dev frontend's host otherwise, matching the relaxation
// the CORS middleware already grants normal requests.
func liveAllowedOrigins(corsOrigin string) []string {
	if corsOrigin == "" {
		return nil
	}
	u, err := url.Parse(corsOrigin)
	if err != nil || u.Host == "" {
		return nil
	}
	return []string{u.Host}
}

// liveStream builds the AssemblyAI streaming transcriber and adapts it to the
// live pipeline's segment stream. AssemblyAI Universal-3 Pro is the sole
// transcription provider: live streams and imported videos alike transcribe
// over its realtime diarizing WebSocket.
func liveStream(cfg config.Transcription, logger *slog.Logger) service.SegmentStream {
	client := transcribe.NewAssemblyAI(transcribe.AssemblyAIConfig{
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		MaxSpeakers: cfg.MaxSpeakers,
		Logger:      logger,
	})
	return transcribe.NewStreamSegmenter(client, transcribe.Options{})
}

// buildDebugSearch assembles the developer wiki-search probe from config. A
// disabled flag returns a nil searcher, which NewMux reads as "do not register
// the route", so the endpoint does not exist in production. An enabled flag
// builds a probe over the same embedder and evidence store the matcher uses.
func buildDebugSearch(cfg config.DebugSearch, embedder service.QueryEmbedder, evidence service.EvidenceSearcher) (handler.WikiSearcher, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	search, err := service.NewWikiSearch(embedder, evidence, service.WikiSearchConfig{
		TopK:    cfg.TopK,
		Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return search, nil
}

// buildPrechecker assembles the check-worthiness gate from config. A disabled
// gate returns a nil prechecker, which the processor treats as "check
// everything" - the pre-gate behavior - with no special-casing. An enabled
// gate pairs the deterministic claim classifier with combined corpus coverage
// over the same embedder, claim store, and wiki store the matcher uses, so a
// segment grounded by either the curated claims or the embedded wiki corpus is
// checked.
func buildPrechecker(cfg config.Precheck, embedder service.QueryEmbedder, claims service.ClaimSearcher, wiki service.EvidenceSearcher) (service.SegmentPrechecker, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	classifier := service.NewHeuristicClassifier(cfg.MinWords)
	coverage, err := service.NewCombinedCoverage(embedder, claims, wiki, service.CoverageConfig{
		ClaimsThreshold: cfg.CoverageThreshold,
		WikiThreshold:   cfg.WikiCoverageThreshold,
		WikiEnabled:     cfg.WikiCoverageEnabled,
	})
	if err != nil {
		return nil, err
	}
	return service.NewGate(classifier, coverage), nil
}
