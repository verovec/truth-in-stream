// Command server is the truth-in-stream backend API.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

	"github.com/verovec/truth-in-stream/backend/internal/audioextract"
	"github.com/verovec/truth-in-stream/backend/internal/auth"
	"github.com/verovec/truth-in-stream/backend/internal/checkworthy"
	"github.com/verovec/truth-in-stream/backend/internal/claimdecomp"
	"github.com/verovec/truth-in-stream/backend/internal/claimtype"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/handler"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/localworthy"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/nli"
	"github.com/verovec/truth-in-stream/backend/internal/rerank"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/source"
	"github.com/verovec/truth-in-stream/backend/internal/source/press"
	"github.com/verovec/truth-in-stream/backend/internal/source/stats"
	"github.com/verovec/truth-in-stream/backend/internal/source/voting"
	"github.com/verovec/truth-in-stream/backend/internal/source/websearch"
	"github.com/verovec/truth-in-stream/backend/internal/stance"
	"github.com/verovec/truth-in-stream/backend/internal/storage"
	"github.com/verovec/truth-in-stream/backend/internal/store"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
	"github.com/verovec/truth-in-stream/backend/internal/verify"
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
	rerankCfg, err := config.LoadRerank()
	if err != nil {
		return err
	}
	telemetryCfg, err := config.LoadTelemetry()
	if err != nil {
		return err
	}
	precheckCfg, err := config.LoadPrecheck()
	if err != nil {
		return err
	}
	liveCfg, err := config.LoadLive()
	if err != nil {
		return err
	}
	consistencyCfg, err := config.LoadConsistency()
	if err != nil {
		return err
	}
	checkWorthinessCfg, err := config.LoadCheckWorthiness()
	if err != nil {
		return err
	}
	checkWorthinessLocalCfg, err := config.LoadCheckWorthinessLocal()
	if err != nil {
		return err
	}
	verifyPathCfg, err := config.LoadVerifyPath()
	if err != nil {
		return err
	}
	politicalCfg, err := config.LoadPolitical()
	if err != nil {
		return err
	}
	secondPassCfg, err := config.LoadSecondPass()
	if err != nil {
		return err
	}
	finalGateCfg, err := config.LoadFinalGate(secondPassCfg)
	if err != nil {
		return err
	}
	verifyNLICfg, err := config.LoadVerifyNLI()
	if err != nil {
		return err
	}
	locale := politicalCfg.Locale()
	debugSearchCfg, err := config.LoadDebugSearch()
	if err != nil {
		return err
	}
	debugFactCheck, err := config.LoadDebugFactCheck()
	if err != nil {
		return err
	}
	legacyPasswordLogin, err := config.LoadLegacyPasswordLogin()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	keycloakCfg := config.LoadKeycloak()
	verifier, err := buildVerifier(ctx, keycloakCfg, logger)
	if err != nil {
		return err
	}

	authConfig := handler.AuthConfig{
		Verifier:            verifier,
		LegacyPasswordLogin: legacyPasswordLogin,
	}
	// The retired password-session login is wired only when explicitly opted in
	// for an environment with no Keycloak yet; otherwise its AUTH_* / SESSION_SECRET
	// env vars are not read and /api is gated solely by the Keycloak identity.
	if legacyPasswordLogin {
		authCfg, err := config.LoadAuth()
		if err != nil {
			return err
		}
		credentials, err := service.NewCredentials(authCfg.Email, authCfg.PasswordHash)
		if err != nil {
			return err
		}
		authConfig.Credentials = credentials
		authConfig.Sessions = service.NewSessions(authCfg.SessionSecret, authCfg.SessionTTL)
		authConfig.SecureCookie = authCfg.SecureCookie
		// 5 attempts then one every 30s per client: invisible to the single
		// operator, glacial for a brute-force run.
		authConfig.LoginLimiter = middleware.NewRateLimiter(rate.Every(30*time.Second), 5)
	}

	storageCfg, err := config.LoadStorage()
	if err != nil {
		return err
	}
	uploadCfg, err := config.LoadUpload()
	if err != nil {
		return err
	}
	documentsCfg, err := config.LoadDocuments()
	if err != nil {
		return err
	}
	youtubeCfg, err := config.LoadYouTube()
	if err != nil {
		return err
	}
	analysisCacheCfg, err := config.LoadAnalysisCache()
	if err != nil {
		return err
	}
	preanalysisCfg, err := config.LoadPreanalysis()
	if err != nil {
		return err
	}

	bqMultiplier, err := config.EvidenceBinaryQuantizationMultiplier()
	if err != nil {
		return err
	}
	pgStore, err := postgres.Open(ctx, cfg.DatabaseURL, postgres.WithBinaryQuantization(bqMultiplier))
	if err != nil {
		return err
	}
	defer pgStore.Close()

	// Hybrid evidence search always runs its vector branch as the single-stage
	// halfvec search, so with hybrid on the binary-quantization two-stage path is
	// not used for evidence retrieval. Warn once at startup when both are enabled
	// so the operator knows BQ's RAM-saving path is not in effect for the evidence
	// hits the matcher and verify path retrieve while hybrid is on (curated-claims
	// BQ is unaffected; combining the two is VER-202 tuning scope).
	if bqMultiplier > 0 && matchCfg.HybridSearch && matchCfg.EvidenceTopK > 0 {
		logger.Warn("hybrid evidence search does not use the binary-quantization two-stage path; EVIDENCE_BQ_MULTIPLIER is not in effect for evidence retrieval while MATCH_HYBRID_SEARCH is on",
			slog.Int("evidence_bq_multiplier", bqMultiplier))
	}

	// The cache is wired and lifecycle-managed here. The snapshot persister tees a
	// completed finite video's live analysis into it; the snapshot reader serves it
	// back on a later open, so re-opening a finished video replays its full analysis
	// instantly with no transcriber or LLM call. When caching is disabled the cache
	// is the no-op store, so the persister is a clean no-op, every read is a miss,
	// and the live path is unchanged.
	analysisCache, closeCache := buildAnalysisCache(ctx, analysisCacheCfg, logger)
	defer func() {
		if err := closeCache(); err != nil {
			logger.WarnContext(ctx, "closing analysis cache", slog.Any("err", err))
		}
	}()
	snapshotPersister, err := service.NewSnapshotPersister(analysisCache, analysisCacheCfg.TTL, logger)
	if err != nil {
		return err
	}
	snapshotReader, err := service.NewSnapshotReader(analysisCache, logger)
	if err != nil {
		return err
	}
	// The durable tier of the replay path: a deliberate pre-analysis persisted
	// in Postgres outlives the cache TTL and wins over it, while the Redis tier
	// keeps serving live-view replays for videos with no stored analysis. The
	// recorder stays Redis-only above, so a lossy browser view never overwrites
	// a stored pre-analysis.
	storedAnalysisReader, err := service.NewStoredAnalysisReader(pgStore, pgStore, logger)
	if err != nil {
		return err
	}
	analysisReplayer, err := service.NewCompositeReplayer(logger, storedAnalysisReader, snapshotReader)
	if err != nil {
		return err
	}

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

	videoSvc, err := service.NewVideoService(pgStore, mediaStore, service.VideoConfig{
		MaxUploadBytes: uploadCfg.MaxBytes,
	})
	if err != nil {
		return err
	}

	youtubeSvc, err := service.NewIngestService(pgStore, mediaStore, ytdlp.New(ytdlp.Config{
		BinaryPath: youtubeCfg.BinaryPath,
		MaxBytes:   youtubeCfg.MaxBytes,
	}), service.IngestConfig{
		MaxDownloadBytes: youtubeCfg.MaxBytes,
		DownloadTimeout:  youtubeCfg.Timeout,
	}, logger)
	if err != nil {
		return err
	}

	tvChannelSvc, err := service.NewTVChannelService(pgStore)
	if err != nil {
		return err
	}

	tvRecordingSvc, err := service.NewTVRecordingService(pgStore, pgStore, mediaStore, 0)
	if err != nil {
		return err
	}

	health := service.NewHealthChecker(pgStore)

	embedder := embed.New(embed.Config{APIKey: embedding.APIKey, Model: embedding.Model, Dim: embedding.Dim})
	matcherOpts, err := buildMatcherOpts(rerankCfg, logger)
	if err != nil {
		return err
	}
	matcher, err := service.NewMatcher(embedder, pgStore, pgStore, service.MatcherConfig{
		TopK:                  matchCfg.TopK,
		ScoreThreshold:        matchCfg.ScoreThreshold,
		EvidenceTopK:          matchCfg.EvidenceTopK,
		EvidenceThreshold:     matchCfg.EvidenceThreshold,
		MaxResults:            matchCfg.MaxResults,
		EmbedConcurrency:      matchCfg.EmbedConcurrency,
		Timeout:               matchCfg.Timeout,
		ConfidenceClusterSize: matchCfg.ConfidenceClusterSize,
		ConfidenceLeadWeight:  matchCfg.ConfidenceLeadWeight,
		ConfidenceBodyWeight:  matchCfg.ConfidenceBodyWeight,
		HybridSearch:          matchCfg.HybridSearch,
		LexicalTopK:           matchCfg.LexicalTopK,
		RRFK:                  matchCfg.RRFK,
		ClaimsEfSearch:        matchCfg.ClaimsEfSearch,
		EvidenceEfSearch:      matchCfg.EvidenceEfSearch,
		RerankCandidates:      rerankCfg.Candidates,
		RerankTimeout:         rerankCfg.Timeout,
		RecencyHalfLife:       matchCfg.RecencyHalfLife,
	}, matcherOpts...)
	if err != nil {
		return err
	}

	// The retrieve-then-verify path replaces the coverage gate with a check-worthy
	// claim gate (coverage is discovered by retrieval, not pre-judged), so when it
	// is active the prechecker is the coverage-free ClaimGate. When it is off the
	// legacy two-stage gate (claim + coverage) is used, unchanged.
	var prechecker service.SegmentPrechecker
	var precheckCloser io.Closer
	if verifyPathCfg.Active() {
		prechecker, precheckCloser, err = buildClaimGate(precheckCfg, checkWorthinessCfg, checkWorthinessLocalCfg, locale, logger)
	} else {
		prechecker, precheckCloser, err = buildPrechecker(precheckCfg, checkWorthinessCfg, checkWorthinessLocalCfg, locale, embedder, pgStore, pgStore, logger)
	}
	if err != nil {
		return err
	}
	if precheckCloser != nil {
		defer func() {
			if err := precheckCloser.Close(); err != nil {
				logger.Warn("closing local check-worthiness scorer", slog.String("error", err.Error()))
			}
		}()
	}

	debugSearch, err := buildDebugSearch(debugSearchCfg, embedder, pgStore)
	if err != nil {
		return err
	}

	stanceClassifier, err := buildStanceClassifier(consistencyCfg, logger)
	if err != nil {
		return err
	}

	segmentMatcher := service.NewSegmentMatchAdapter(matcher)
	verifyMatcher, err := buildVerifyMatcher(verifyPathCfg, matchCfg, rerankCfg, embedder, pgStore, segmentMatcher, matcherOpts)
	if err != nil {
		return err
	}
	var telemetryRec *service.TelemetryRecorder
	if telemetryCfg.Enabled {
		telemetryRec, err = service.NewTelemetryRecorder(pgStore, service.TelemetryConfig{
			QueueDepth: telemetryCfg.QueueDepth,
			BatchSize:  32,
			FlushEvery: telemetryCfg.FlushEvery,
			SampleRate: telemetryCfg.SampleRate,
			Locale:     string(locale),
			Logger:     logger,
		})
		if err != nil {
			return err
		}
		// Fire-and-forget by design: telemetry is lossy by contract, so the
		// final in-flight batch may be lost on shutdown rather than delaying it.
		go telemetryRec.Run(ctx)
	}
	// The NLI stance scorer holds one native ONNX session, so it is built once
	// and shared by the live and batch verify paths (unlike their isolated
	// verify pools); its own inference semaphore bounds concurrent use.
	nliStance, nliCloser := buildNLIStance(verifyNLICfg, logger)
	if nliCloser != nil {
		defer func() {
			if err := nliCloser.Close(); err != nil {
				logger.Warn("closing nli stance scorer", slog.String("error", err.Error()))
			}
		}()
	}
	verifyPath, err := buildVerifyPath(verifyPathCfg, politicalCfg, finalGateCfg, nliStance, verifyMatcher, pgStore, pgStore, locale, telemetryRec, logger)
	if err != nil {
		return err
	}

	// The document analyzer reuses the verify path to fact-check stored PDF
	// sentences as an in-process background job. It gets its OWN VerifyPath
	// instance (with its own verify-pool semaphore), not the live one: a batch
	// run blocks on the pool for minutes without shedding, so sharing the live
	// pool would starve live claims into capacity sheds for the duration of an
	// analysis. The two instances share the stateless retrieval matcher; only
	// the verify pool (the slow LLM bottleneck) needs isolating. verifyPath is a
	// typed nil when the feature is off, so the analyzer is handed a non-nil
	// BatchVerifier only when the path is actually built - otherwise it would
	// report itself enabled over a nil pointer. Startup recovery flips any run
	// interrupted by a prior crash to failed so the admin can reanalyse.
	var batchVerifier service.BatchVerifier
	if verifyPath != nil {
		analyzerVerifyPath, err := buildVerifyPath(verifyPathCfg, politicalCfg, finalGateCfg, nliStance, verifyMatcher, pgStore, pgStore, locale, telemetryRec, logger)
		if err != nil {
			return err
		}
		batchVerifier = analyzerVerifyPath
	}
	documentAnalyzer, err := service.NewDocumentAnalyzer(pgStore, batchVerifier, prechecker, service.DocumentAnalyzerConfig{
		Timeout: documentsCfg.AnalysisTimeout,
	}, logger)
	if err != nil {
		return err
	}
	if err := documentAnalyzer.Recover(ctx); err != nil {
		return err
	}

	documentSvc, err := service.NewDocumentService(pgStore, mediaStore, documentAnalyzer, service.DocumentConfig{
		MaxSizeBytes: documentsCfg.MaxSizeBytes,
		MaxSentences: documentsCfg.MaxSentences,
	}, logger)
	if err != nil {
		return err
	}

	liveAnalyzer, err := service.NewLiveAnalyzer(service.LiveAnalyzerConfig{
		Stream:           liveStream(transcription, locale, logger),
		Matcher:          segmentMatcher,
		Prechecker:       prechecker,
		Logger:           logger,
		Concurrency:      liveCfg.Concurrency,
		QueueDepth:       liveCfg.QueueDepth,
		MaxSentences:     liveCfg.MaxSentences,
		Stance:           stanceClassifier,
		ConsistencyTopK:  consistencyCfg.TopK,
		ConsistencyFloor: consistencyCfg.SimilarityFloor,
		Verify:           verifyPath,
	})
	if err != nil {
		return err
	}

	// The TV hub runs one live analysis session per channel over the same
	// analyzer the browser path uses: each Run call gets fresh per-session state,
	// so a single analyzer instance serves every channel and every viewer.
	tvHub, err := service.NewTVHub(liveAnalyzer, logger)
	if err != nil {
		return err
	}

	// The headless pre-analysis job replays a stored video's audio through the
	// same live analyzer a browser session uses (each Run gets fresh per-session
	// state), paced at realtime by the ffmpeg extractor, and persists the teed
	// events durably. Startup recovery flips any run a prior crash left
	// analysing to failed so the operator can re-run it.
	audioExtractor, err := audioextract.New(audioextract.Config{PacingFactor: preanalysisCfg.PacingFactor})
	if err != nil {
		return err
	}
	storedAnalysisPersister, err := service.NewStoredAnalysisPersister(pgStore)
	if err != nil {
		return err
	}
	videoAnalyzer, err := service.NewVideoAnalyzer(pgStore, videoAudioStreamer{extractor: audioExtractor, media: internalDownloadPresigner{store: mediaStore}}, liveAnalyzer, storedAnalysisPersister, service.VideoAnalyzerConfig{
		Timeout:       preanalysisCfg.RunTimeout,
		MaxConcurrent: preanalysisCfg.MaxConcurrent,
		Engine:        preanalysisEngine(transcription, preanalysisCfg, verifyPathCfg, politicalCfg, finalGateCfg, matchCfg),
	}, logger)
	if err != nil {
		return err
	}
	if err := videoAnalyzer.Recover(ctx); err != nil {
		return err
	}

	liveOrigins := liveAllowedOrigins(cfg.CORSAllowedOrigin)
	if cfg.CORSAllowedOrigin != "" && len(liveOrigins) == 0 {
		logger.Warn("live websocket enforces same-origin: CORS_ALLOWED_ORIGIN has no parseable host",
			slog.String("cors_allowed_origin", cfg.CORSAllowedOrigin))
	}

	apiHandler := handler.NewMux(health, videoSvc, storedAnalysisReader, videoAnalyzer, documentSvc, documentAnalyzer, youtubeSvc, tvChannelSvc, tvRecordingSvc, tvHub, liveAnalyzer, snapshotPersister, analysisReplayer, liveOrigins, debugFactCheck, debugSearch, cfg.DemoMediaDir, authConfig, logger)
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
// over its realtime diarizing WebSocket. The locale biases the spoken language
// (empty for the default auto-detect, "fr" in French political mode); diarization
// stays on regardless, since a verdict must never blend two speakers.
func liveStream(cfg config.Transcription, locale domain.Locale, logger *slog.Logger) service.SegmentStream {
	client := transcribe.NewAssemblyAI(transcribe.AssemblyAIConfig{
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		MaxSpeakers: cfg.MaxSpeakers,
		Logger:      logger,
	})
	return transcribe.NewStreamSegmenter(client, transcribe.Options{Language: locale.LanguageCode()})
}

// buildAnalysisCache selects the analysis cache from config: a Redis/Valkey-backed
// cache when REDIS_URL is set and the server is reachable, and the no-op cache
// otherwise. It never fails startup - an empty URL, an unparseable URL, or an
// unreachable server each degrade to the no-op cache with a single warning, so the
// service behaves exactly as it does today when caching is unavailable. The
// returned close func releases the Redis client (a no-op when caching is disabled).
// REDIS_URL can carry a password, so it is never logged; only that caching is
// disabled or that the ping failed is recorded.
func buildAnalysisCache(ctx context.Context, cfg config.AnalysisCache, logger *slog.Logger) (store.AnalysisCache, func() error) {
	noop := func() (store.AnalysisCache, func() error) {
		return store.NoopCache{}, func() error { return nil }
	}
	if !cfg.Enabled() {
		logger.InfoContext(ctx, "analysis cache disabled: REDIS_URL not set, using no-op cache")
		return noop()
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.WarnContext(ctx, "analysis cache disabled: REDIS_URL is not a valid redis url, using no-op cache")
		return noop()
	}
	// Fail fast and quietly when the cache is unreachable: a single dial attempt (no
	// retry storm) bounded by a short timeout, so an unavailable cache degrades to
	// no-op promptly at startup rather than retrying for seconds.
	opts.MaxRetries = -1
	opts.DialTimeout = 3 * time.Second
	client := redis.NewClient(opts)
	// The ping timeout is derived from a fresh context, not the signal-bound ctx, so
	// a SIGINT arriving during the startup probe is not misread as an unreachable
	// cache and silently degraded - shutdown is handled by the server loop instead.
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		// The ping error can embed the host:port from REDIS_URL, so it is summarized
		// to its error type rather than logged verbatim - never leak any part of the
		// connection string, which may carry a password.
		logger.WarnContext(ctx, "analysis cache disabled: redis ping failed, using no-op cache",
			slog.String("err_type", fmt.Sprintf("%T", err)))
		_ = client.Close()
		return noop()
	}
	logger.InfoContext(ctx, "analysis cache enabled", slog.Duration("ttl", cfg.TTL))
	return store.NewRedisCache(client), client.Close
}

// buildVerifier wires the Keycloak access-token verifier over a JWKS keyfunc.
// The keyfunc fetches and caches the realm's signing keys and launches a refresh
// goroutine bound to ctx (so it stops on shutdown), refreshing on an unknown kid
// to ride key rotation. NoErrorReturnFirstHTTPReq lets the server start before
// Keycloak is reachable (the first verification fetches the set), so a slow or
// late identity provider does not block boot; tokens simply fail to validate
// until the JWKS is fetched. The verifier itself adds the issuer and
// authorized-party checks and the role extraction.
func buildVerifier(ctx context.Context, cfg config.Keycloak, logger *slog.Logger) (auth.Verifier, error) {
	allowStart := true
	kf, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{cfg.JWKSURL}, keyfunc.Override{
		RefreshInterval:           5 * time.Minute,
		RefreshUnknownKID:         rate.NewLimiter(rate.Every(time.Minute), 1),
		NoErrorReturnFirstHTTPReq: &allowStart,
		// Surface a background JWKS refresh failure so a stale key set after a
		// Keycloak blip is observable rather than silently degrading validation.
		RefreshErrorHandlerFunc: func(u string) func(context.Context, error) {
			return func(ctx context.Context, err error) {
				logger.WarnContext(ctx, "keycloak jwks refresh failed", slog.String("jwks_url", u), slog.Any("err", err))
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building keycloak jwks keyfunc: %w", err)
	}
	verifier, err := auth.NewVerifier(kf, auth.Config{Issuer: cfg.Issuer, ClientID: cfg.ClientID, AdditionalClientIDs: cfg.AdditionalClientIDs})
	if err != nil {
		return nil, fmt.Errorf("building keycloak verifier: %w", err)
	}
	logger.Info("keycloak token validation enabled", slog.String("issuer", cfg.Issuer), slog.String("jwks_url", cfg.JWKSURL))
	return verifier, nil
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
// gate pairs the claim classifier with combined corpus coverage over the same
// embedder, claim store, and wiki store the matcher uses, so a segment grounded
// by either the curated claims or the embedded wiki corpus is checked.
//
// Stage one is the deterministic heuristic by default. When the model
// check-worthiness classifier is active, the heuristic is wrapped in a cascade:
// the heuristic still rejects obvious non-claims for free, and only its
// survivors reach the model, which skips casual or personal declaratives a
// word-list cannot. An unconfigured or keyless model leaves the heuristic alone,
// exactly the prior behavior.
func buildPrechecker(cfg config.Precheck, cw config.CheckWorthiness, local config.CheckWorthinessLocal, locale domain.Locale, embedder service.QueryEmbedder, claims service.ClaimSearcher, wiki service.EvidenceSearcher, logger *slog.Logger) (service.SegmentPrechecker, io.Closer, error) {
	if !cfg.Enabled {
		return nil, nil, nil
	}
	classifier, closer, err := buildClaimClassifier(cfg, cw, local, locale, logger)
	if err != nil {
		return nil, nil, err
	}
	coverage, err := service.NewCombinedCoverage(embedder, claims, wiki, service.CoverageConfig{
		ClaimsThreshold: cfg.CoverageThreshold,
		WikiThreshold:   cfg.WikiCoverageThreshold,
		WikiEnabled:     cfg.WikiCoverageEnabled,
		EfSearch:        cfg.CoverageEfSearch,
	})
	if err != nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, nil, err
	}
	return service.NewGate(classifier, coverage), closer, nil
}

// buildClaimClassifier returns the gate's stage-one classifier: the
// deterministic heuristic alone when no model stage is active, the heuristic
// wrapped in a model cascade when the check-worthiness model is configured,
// and the three-stage banded cascade when the local scorer is also active, so
// the generative model is consulted only inside the local classifier's
// uncertainty band. A local scorer that fails to load (missing artifact,
// binary built without the localinference tag) degrades to the two-stage wiring
// with a warning, never failing the boot. The returned closer releases the
// scorer's native session on shutdown and is nil when no scorer was wired.
// API keys are never logged.
func buildClaimClassifier(cfg config.Precheck, cw config.CheckWorthiness, local config.CheckWorthinessLocal, locale domain.Locale, logger *slog.Logger) (service.ClaimClassifier, io.Closer, error) {
	heuristic := service.NewHeuristicClassifier(cfg.MinWords, locale)
	var model service.CheckWorthinessClassifier
	if cw.Active() {
		client, err := checkworthy.New(checkworthy.Config{Provider: llm.ProviderName(cw.Provider), APIKey: cw.APIKey, GeminiAPIKey: cw.GeminiAPIKey, DeepSeekAPIKey: cw.DeepSeekAPIKey, Model: cw.Model, Locale: locale})
		if err != nil {
			return nil, nil, err
		}
		logger.Info("model check-worthiness classifier enabled", slog.String("model", cw.Model))
		model = client
	}
	if local.Active() {
		scorer, err := localworthy.New(localworthy.Config{
			ModelPath:     local.ModelPath,
			TokenizerPath: local.TokenizerPath,
			LibraryPath:   local.LibraryPath,
			Timeout:       local.Timeout,
			Logger:        logger,
		})
		if err != nil {
			logger.Warn("local check-worthiness scorer unavailable; keeping the model cascade", slog.String("error", err.Error()))
		} else {
			logger.Info("local check-worthiness classifier enabled",
				slog.String("model_path", local.ModelPath),
				slog.Float64("band_low", local.BandLow),
				slog.Float64("band_high", local.BandHigh))
			return service.NewBandedCascadeClassifier(heuristic, scorer, model, local.BandLow, local.BandHigh, logger), scorer, nil
		}
	}
	if model == nil {
		return heuristic, nil, nil
	}
	return service.NewCascadeClassifier(heuristic, model, logger), nil, nil
}

// buildNLIStance wires the local NLI stance stage when it is active and its
// artifacts load. Any failure (missing artifact, binary built without the
// localinference tag) degrades to the LLM-first verify path with a warning,
// never failing the boot. The returned closer releases the scorer's native
// session on shutdown and is nil when no scorer was wired.
func buildNLIStance(cfg config.VerifyNLI, logger *slog.Logger) (*service.StanceConfig, io.Closer) {
	if !cfg.Active() {
		return nil, nil
	}
	scorer, err := nli.New(nli.Config{
		ModelPath:     cfg.ModelPath,
		TokenizerPath: cfg.TokenizerPath,
		LibraryPath:   cfg.LibraryPath,
		Temperature:   cfg.Temperature,
		Timeout:       cfg.Timeout,
		Logger:        logger,
	})
	if err != nil {
		logger.Warn("nli stance scorer unavailable; keeping the LLM-first verify path", slog.String("error", err.Error()))
		return nil, nil
	}
	logger.Info("nli stance stage enabled",
		slog.String("model_path", cfg.ModelPath),
		slog.Float64("entail_threshold", cfg.EntailThreshold),
		slog.Float64("contradict_threshold", cfg.ContradictThreshold),
		slog.Int("min_agree", cfg.MinAgree))
	return &service.StanceConfig{
		Scorer:              nliScorerAdapter{scorer},
		EntailThreshold:     cfg.EntailThreshold,
		ContradictThreshold: cfg.ContradictThreshold,
		MinAgree:            cfg.MinAgree,
		MaxPassages:         cfg.MaxPassages,
	}, scorer
}

// nliScorerAdapter maps the nli package's stance type onto the service port.
type nliScorerAdapter struct {
	scorer *nli.Scorer
}

func (a nliScorerAdapter) ScoreStances(ctx context.Context, claim string, passages []string) ([]service.StanceResult, error) {
	stances, err := a.scorer.ScoreStances(ctx, claim, passages)
	if err != nil {
		return nil, err
	}
	out := make([]service.StanceResult, len(stances))
	for i, s := range stances {
		out[i] = service.StanceResult{Entailment: s.Entailment, Neutral: s.Neutral, Contradiction: s.Contradiction}
	}
	return out, nil
}

// buildMatcherOpts wires the optional retrieval reranker: the Voyage rerank
// client behind the matcher's Reranker port when the stage is enabled and
// keyed, or no option at all, leaving retrieval byte-identical to before. The
// API key is never logged.
func buildMatcherOpts(cfg config.Rerank, logger *slog.Logger) ([]service.MatcherOption, error) {
	if !cfg.Active() {
		return nil, nil
	}
	client, err := rerank.New(rerank.Config{APIKey: cfg.APIKey, Model: cfg.Model})
	if err != nil {
		return nil, fmt.Errorf("build reranker: %w", err)
	}
	logger.Info("retrieval reranking enabled", slog.String("model", cfg.Model))
	return []service.MatcherOption{service.WithReranker(client, logger)}, nil
}

// buildStanceClassifier wires the intra-speaker consistency stance check, or
// returns a nil classifier (interface, not a typed nil) when the feature is not
// active, so the live analyzer leaves consistency off and behaves exactly as
// before. The API key is never logged.
func buildStanceClassifier(cfg config.Consistency, logger *slog.Logger) (service.StanceClassifier, error) {
	if !cfg.Active() {
		return nil, nil
	}
	client, err := stance.New(stance.Config{Provider: llm.ProviderName(cfg.Provider), APIKey: cfg.APIKey, GeminiAPIKey: cfg.GeminiAPIKey, DeepSeekAPIKey: cfg.DeepSeekAPIKey, Model: cfg.Model})
	if err != nil {
		return nil, err
	}
	logger.Info("intra-speaker consistency enabled", slog.String("model", cfg.Model))
	return client, nil
}

// buildClaimGate assembles the retrieve-then-verify path's coverage-free gate:
// the same stage-one claim classifier the legacy gate uses (heuristic, or the
// heuristic-plus-model cascade), with no coverage stage. Whether evidence exists
// is discovered by the verify path's retrieval, not pre-judged here, so a novel
// but checkable claim is no longer dropped as not_covered.
func buildClaimGate(cfg config.Precheck, cw config.CheckWorthiness, local config.CheckWorthinessLocal, locale domain.Locale, logger *slog.Logger) (service.SegmentPrechecker, io.Closer, error) {
	if !cfg.Enabled {
		return nil, nil, nil
	}
	classifier, closer, err := buildClaimClassifier(cfg, cw, local, locale, logger)
	if err != nil {
		return nil, nil, err
	}
	return service.NewClaimGate(classifier), closer, nil
}

// evidenceStore is the slice of the store the verify-path matcher needs: nearest
// neighbors over both the curated-claims and Wikipedia corpora. The concrete
// *postgres.Store satisfies it structurally.
type evidenceStore interface {
	service.ClaimSearcher
	service.EvidenceSearcher
}

// buildVerifyMatcher builds the high-recall matcher the verify path retrieves
// through. It mirrors the legacy matcher's configuration but lowers the claim and
// evidence thresholds to the verify path's retrieval floor, so the on-topic
// evidence band is pulled for the verifier to judge rather than discarded by the
// legacy borrow-by-similarity precision bar (at which the verify path retrieves
// nothing and every claim short-circuits to a no-evidence not_enough_info). When
// the path is inactive it returns the supplied fallback unchanged - buildVerifyPath
// ignores it - so the extra matcher exists only when it is used.
func buildVerifyMatcher(cfg config.VerifyPath, matchCfg config.Match, rerankCfg config.Rerank, embedder service.QueryEmbedder, store evidenceStore, fallback service.SegmentMatcher, opts []service.MatcherOption) (service.SegmentMatcher, error) {
	if !cfg.Active() {
		return fallback, nil
	}
	matcher, err := service.NewMatcher(embedder, store, store, service.MatcherConfig{
		TopK:                  matchCfg.TopK,
		ScoreThreshold:        cfg.RetrievalThreshold,
		EvidenceTopK:          matchCfg.EvidenceTopK,
		EvidenceThreshold:     cfg.RetrievalThreshold,
		MaxResults:            matchCfg.MaxResults,
		EmbedConcurrency:      matchCfg.EmbedConcurrency,
		Timeout:               matchCfg.Timeout,
		ConfidenceClusterSize: matchCfg.ConfidenceClusterSize,
		ConfidenceLeadWeight:  matchCfg.ConfidenceLeadWeight,
		ConfidenceBodyWeight:  matchCfg.ConfidenceBodyWeight,
		HybridSearch:          matchCfg.HybridSearch,
		LexicalTopK:           matchCfg.LexicalTopK,
		RRFK:                  matchCfg.RRFK,
		ClaimsEfSearch:        matchCfg.ClaimsEfSearch,
		EvidenceEfSearch:      matchCfg.EvidenceEfSearch,
		RerankCandidates:      rerankCfg.Candidates,
		RerankTimeout:         rerankCfg.Timeout,
		RecencyHalfLife:       matchCfg.RecencyHalfLife,
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("build verify matcher: %w", err)
	}
	return service.NewSegmentMatchAdapter(matcher), nil
}

// buildVerifyPath wires the retrieve-then-verify orchestration, or returns nil
// (so the analyzer runs the legacy path) when the feature is not active. The
// decomposer and verifier are Anthropic-backed adapters over the shared llm
// transport; both share the configured model. When political mode is active on top
// of the verify path (FACTCHECK_POLITICAL on), the per-claim verify stage is
// switched onto the political pipeline (classify -> route+retrieve -> two-axis
// verify) by passing the political collaborators through. The API key is never
// logged.
func buildVerifyPath(cfg config.VerifyPath, political config.Political, finalGate config.FinalGate, nliStance *service.StanceConfig, matcher service.SegmentMatcher, votingStore voting.Store, curatedClaims service.PoliticalClaimSearcher, locale domain.Locale, telemetry *service.TelemetryRecorder, logger *slog.Logger) (*service.VerifyPath, error) {
	if !cfg.Active() {
		return nil, nil
	}
	decomposer, err := claimdecomp.New(claimdecomp.Config{Provider: llm.ProviderName(cfg.Provider), APIKey: cfg.APIKey, GeminiAPIKey: cfg.GeminiAPIKey, DeepSeekAPIKey: cfg.DeepSeekAPIKey, Model: cfg.Model, MaxClaimsPerUnit: cfg.MaxClaimsPerUnit, Locale: locale})
	if err != nil {
		return nil, err
	}
	verifier, err := verify.New(verify.Config{Provider: llm.ProviderName(cfg.Provider), APIKey: cfg.APIKey, GeminiAPIKey: cfg.GeminiAPIKey, DeepSeekAPIKey: cfg.DeepSeekAPIKey, Model: cfg.Model, Locale: locale})
	if err != nil {
		return nil, err
	}
	pol, err := buildPoliticalConfig(cfg, political, votingStore, curatedClaims, logger)
	if err != nil {
		return nil, err
	}
	// The terminal gate attaches to whichever verify stage runs: the credibility path
	// re-judges a weak credibility verdict, the political path re-judges a weak
	// two-axis verdict (mapping the reasoner's credibility back onto the literal axis).
	// It is wired for both, so a political run gets the same last-resort adjudication.
	secondPassCfg, err := buildSecondPass(finalGate, locale)
	if err != nil {
		return nil, err
	}
	path, err := service.NewVerifyPath(service.VerifyPathConfig{
		Decomposer:        decomposerAdapter{decomposer},
		Matcher:           matcher,
		Verifier:          verifierAdapter{verifier},
		FastTau:           cfg.FastTau,
		VerifyConcurrency: cfg.Concurrency,
		VerifyQueueDepth:  cfg.QueueDepth,
		FastDeadline:      cfg.FastDeadline,
		VerifyDeadline:    cfg.VerifyDeadline,
		CacheTTL:          cfg.CacheTTL,
		CacheThreshold:    cfg.CacheThreshold,
		CacheMaxEntries:   cfg.CacheMaxEntries,
		Logger:            logger,
		Political:         pol,
		NLIStance:         nliStance,
		SecondPass:        secondPassCfg,
		Telemetry:         telemetry,
	})
	if err != nil {
		return nil, err
	}
	if pol != nil {
		logger.Info("political fact-check path enabled (classify -> route -> two-axis verify)", slog.String("model", cfg.Model))
	} else {
		logger.Info("retrieve-then-verify fact-check path enabled", slog.String("model", cfg.Model))
	}
	if secondPassCfg != nil {
		logger.Info("terminal reasoning gate enabled for weak verdicts", slog.String("provider", finalGate.Provider), slog.String("model", finalGate.Model))
	}
	return path, nil
}

// buildSecondPass wires the terminal reasoning gate, or returns nil (so the verify
// path runs its single fast pass unchanged) when the feature is not active. The
// reverifier is a verify client built on the gate's own provider and reasoning model
// - decoupled from the hot-path LLM_PROVIDER so the expensive reasoner can run on a
// different backend - and it shares the credibility verifier's locale so the re-judged
// rationale stays in the viewer's language. The gate is wired regardless of political
// mode: the political path routes its weak two-axis verdicts through the same reasoner.
// The API key is never logged.
func buildSecondPass(cfg config.FinalGate, locale domain.Locale) (*service.SecondPassConfig, error) {
	if !cfg.Active() {
		return nil, nil
	}
	reverifier, err := verify.New(verify.Config{Provider: llm.ProviderName(cfg.Provider), APIKey: cfg.APIKey, GeminiAPIKey: cfg.GeminiAPIKey, DeepSeekAPIKey: cfg.DeepSeekAPIKey, Model: cfg.Model, Locale: locale})
	if err != nil {
		return nil, err
	}
	return &service.SecondPassConfig{
		Reverifier:    reverifierAdapter{reverifier},
		TriggerBelow:  cfg.TriggerBelow,
		MinConfidence: cfg.MinConfidence,
		Deadline:      cfg.Deadline,
	}, nil
}

// buildPoliticalConfig assembles the political verify path's collaborators - the
// claim-type classifier, the source router, and the two-axis verifier - or returns
// nil (so the verify path runs its credibility-only stage unchanged) when political
// mode is not active on top of the verify path. The classifier and two-axis
// verifier share the verify path's model and API key; the router is built over the
// configured source packs with web search as the mandatory open-ended fallback. The
// API key is never logged.
func buildPoliticalConfig(verifyCfg config.VerifyPath, political config.Political, votingStore voting.Store, curatedClaims service.PoliticalClaimSearcher, logger *slog.Logger) (*service.PoliticalConfig, error) {
	if !political.Active(verifyCfg.Active()) {
		return nil, nil
	}
	classifier, err := claimtype.New(claimtype.Config{Provider: llm.ProviderName(verifyCfg.Provider), APIKey: verifyCfg.APIKey, GeminiAPIKey: verifyCfg.GeminiAPIKey, DeepSeekAPIKey: verifyCfg.DeepSeekAPIKey, Model: verifyCfg.Model})
	if err != nil {
		return nil, err
	}
	politicalVerifier, err := verify.New(verify.Config{Provider: llm.ProviderName(verifyCfg.Provider), APIKey: verifyCfg.APIKey, GeminiAPIKey: verifyCfg.GeminiAPIKey, DeepSeekAPIKey: verifyCfg.DeepSeekAPIKey, Model: verifyCfg.Model})
	if err != nil {
		return nil, err
	}
	router, err := buildRouter(political, votingStore, logger)
	if err != nil {
		return nil, err
	}
	return &service.PoliticalConfig{
		Classifier:    classifier,
		Retriever:     router,
		Verifier:      politicalVerifierAdapter{politicalVerifier},
		CuratedStore:  curatedClaims,
		CuratedTau:    political.CuratedTau,
		CuratedMaxAge: political.CuratedMaxAge,
	}, nil
}

// buildRouter assembles the context-aware source router over the configured source
// packs. The stats and voting packs are keyless (the voting pack reads the
// political store); the press and web-search packs join only when their own key is
// set. Web search is the open-ended fallback every unrouted claim depends on, so
// its absence is degraded loudly (a boot warning) rather than fatally: political
// mode still answers statistic and voting-record claims from the keyless packs,
// and open-ended claims retrieve no evidence instead of the server refusing to
// start. The router's language tracks the political locale.
func buildRouter(political config.Political, votingStore voting.Store, logger *slog.Logger) (*service.Router, error) {
	statsCfg, err := stats.LoadConfig()
	if err != nil {
		return nil, err
	}
	retrievers := []source.Retriever{
		stats.New(stats.Config{Timeout: statsCfg.Timeout, CacheTTL: statsCfg.CacheTTL}),
		voting.New(votingStore),
	}

	// Same contract as the press pack below: a missing key is a documented
	// degradation, while a set key with malformed tuning (e.g. a bad
	// WEBSEARCH_TIMEOUT) is a real misconfiguration and still fails fast, so a typo
	// is never swallowed as "web search absent".
	if os.Getenv("WEBSEARCH_API_KEY") != "" {
		websearchCfg, err := websearch.LoadConfig()
		if err != nil {
			return nil, err
		}
		web, err := websearch.New(websearch.Config{APIKey: websearchCfg.APIKey, Timeout: websearchCfg.Timeout})
		if err != nil {
			return nil, err
		}
		retrievers = append(retrievers, web)
	} else {
		logger.Warn("political web-search fallback disabled: WEBSEARCH_API_KEY is unset; open-ended claims will retrieve no evidence")
	}

	// The press pack is optional: it joins the registry only when its own key is
	// set, so attribution claims that would route to it fall through to web search
	// when it is absent rather than failing to wire. A set key with a malformed
	// tuning value (e.g. a bad PRESS_TIMEOUT) is a real misconfiguration and fails
	// fast - only the missing-key case is a silent skip, so a typo is never swallowed
	// as "press absent".
	if os.Getenv("PRESS_API_KEY") != "" {
		pressCfg, err := press.LoadConfig()
		if err != nil {
			return nil, err
		}
		pressPack, err := press.New(press.Config{APIKey: pressCfg.APIKey, Timeout: pressCfg.Timeout})
		if err != nil {
			return nil, err
		}
		retrievers = append(retrievers, pressPack)
		logger.Info("political press source enabled")
	}

	return service.NewRouter(retrievers, service.RouterConfig{
		MinResults: political.RouterMinResults,
		Lang:       political.RouterLang(),
	})
}

// videoAudioStreamer adapts the ffmpeg extractor to the pre-analysis job's
// AudioStreamer port: it presigns the video's stored object against the media
// store, then starts the paced PCM extraction over the presigned URL, so the
// job knows nothing about ffmpeg or presigning.
type videoAudioStreamer struct {
	extractor *audioextract.Extractor
	media     audioextract.MediaPresigner
}

// internalDownloadPresigner exposes the media store's internal-endpoint
// download presign under the audioextract.MediaPresigner shape. The ffmpeg
// fetch runs inside the backend's own network horizon, where the
// browser-facing public endpoint may be unreachable (local dev's
// localhost:9000 is the container's loopback), so the pre-analysis source is
// signed against the internal endpoint the backend already uses for
// server-side storage operations.
type internalDownloadPresigner struct {
	store *storage.S3Store
}

func (p internalDownloadPresigner) PresignDownload(ctx context.Context, key string) (domain.PresignedRequest, error) {
	return p.store.PresignInternalDownload(ctx, key)
}

func (s videoAudioStreamer) Stream(ctx context.Context, video domain.Video) (service.AudioStream, error) {
	src, err := audioextract.PresignedSource(ctx, s.media, video.ObjectKey)
	if err != nil {
		return nil, err
	}
	return s.extractor.Extract(ctx, src)
}

// preanalysisEngine assembles the engine fingerprint stamped on every stored
// pre-analysis: what transcribed, what verified, and the retrieval posture at
// run time, so the operator can judge whether a stored result predates a
// relevant configuration change before re-analysing. The verify fields are
// recorded only when the verify path is active; otherwise the run's verdicts
// come from the legacy borrow-by-similarity path and the fields stay empty.
func preanalysisEngine(transcription config.Transcription, pre config.Preanalysis, verifyCfg config.VerifyPath, political config.Political, gate config.FinalGate, match config.Match) service.EngineMetadata {
	engine := service.EngineMetadata{
		TranscriberModel: transcription.Model,
		PacingFactor:     pre.PacingFactor,
		HybridSearch:     match.HybridSearch,
	}
	if !verifyCfg.Active() {
		return engine
	}
	engine.VerifyProvider = resolvedProvider(verifyCfg.Provider)
	engine.VerifyModel = verifyCfg.Model
	engine.RetrievalThreshold = verifyCfg.RetrievalThreshold
	engine.Political = political.Active(true)
	if gate.Active() {
		engine.SecondPassModel = gate.Model
	}
	return engine
}

// resolvedProvider names the LLM backend an empty LLM_PROVIDER resolves to, so
// the stored fingerprint records the effective provider, not the raw setting.
func resolvedProvider(provider string) string {
	if provider == "" {
		return string(llm.ProviderDeepSeek)
	}
	return provider
}

// politicalVerifierAdapter adapts the verify client's two-axis VerifyPolitical to
// the service PoliticalVerifier port: it maps the service's evidence passages onto
// the verifier's, and the verifier's two-axis result (already citation-guarded)
// back onto the service two-axis verdict.
type politicalVerifierAdapter struct {
	client *verify.Client
}

func (v politicalVerifierAdapter) VerifyPolitical(ctx context.Context, claim string, passages []service.EvidencePassage) (service.PoliticalVerdict, error) {
	in := make([]verify.Passage, len(passages))
	for i, p := range passages {
		in[i] = verify.Passage{ID: p.ID, Text: p.Text, Date: p.Date}
	}
	res, err := v.client.VerifyPolitical(ctx, claim, in)
	if err != nil {
		return service.PoliticalVerdict{}, err
	}
	citations := make([]service.EvidenceCitation, len(res.Citations))
	for i, c := range res.Citations {
		citations[i] = service.EvidenceCitation{EvidenceID: c.EvidenceID, QuotedSpan: c.QuotedSpan}
	}
	return service.PoliticalVerdict{
		Literal:    res.Literal,
		Basis:      res.Basis,
		Flags:      res.Flags,
		Confidence: res.Confidence,
		Citations:  citations,
		Rationale:  res.Rationale,
	}, nil
}

// decomposerAdapter adapts the claimdecomp client to the service ClaimDecomposer
// port: it maps the port's positional arguments onto the client's Input struct.
type decomposerAdapter struct {
	client *claimdecomp.Client
}

func (d decomposerAdapter) Decompose(ctx context.Context, text, speaker, recentContext string) []service.DecomposedClaim {
	decomposed := d.client.Decompose(ctx, claimdecomp.Input{Text: text, Speaker: speaker, Context: recentContext})
	claims := make([]service.DecomposedClaim, len(decomposed))
	for i, c := range decomposed {
		claims[i] = service.DecomposedClaim{Text: c.Text, Quote: c.Quote}
	}
	return claims
}

// verifierAdapter adapts the verify client to the service ClaimVerifier port: it
// maps the service's evidence passages onto the verifier's, and the verifier's
// grounded result (already citation-guarded) back onto the service verdict.
type verifierAdapter struct {
	client *verify.Client
}

func (v verifierAdapter) Verify(ctx context.Context, claim string, passages []service.EvidencePassage) (service.ClaimVerdict, error) {
	in := make([]verify.Passage, len(passages))
	for i, p := range passages {
		in[i] = verify.Passage{ID: p.ID, Text: p.Text, Date: p.Date}
	}
	res, err := v.client.Verify(ctx, claim, in)
	if err != nil {
		return service.ClaimVerdict{}, err
	}
	citations := make([]service.EvidenceCitation, len(res.Citations))
	for i, c := range res.Citations {
		citations[i] = service.EvidenceCitation{EvidenceID: c.EvidenceID, QuotedSpan: c.QuotedSpan}
	}
	return service.ClaimVerdict{
		Verdict:    res.Verdict,
		Basis:      res.Basis,
		Confidence: res.Confidence,
		Citations:  citations,
		Rationale:  res.Rationale,
	}, nil
}

// reverifierAdapter adapts the verify client's reasoning Reverify to the service
// ClaimReverifier port: it maps the service's evidence passages onto the
// verifier's, and the verifier's deeper grounded result (already citation-guarded
// and cap-enforced) back onto the service verdict. It is the second-pass twin of
// verifierAdapter, differing only in calling Reverify (the thinking-enabled path)
// rather than Verify.
type reverifierAdapter struct {
	client *verify.Client
}

func (v reverifierAdapter) Reverify(ctx context.Context, claim string, passages []service.EvidencePassage) (service.ClaimVerdict, error) {
	in := make([]verify.Passage, len(passages))
	for i, p := range passages {
		in[i] = verify.Passage{ID: p.ID, Text: p.Text, Date: p.Date}
	}
	res, err := v.client.Reverify(ctx, claim, in)
	if err != nil {
		return service.ClaimVerdict{}, err
	}
	citations := make([]service.EvidenceCitation, len(res.Citations))
	for i, c := range res.Citations {
		citations[i] = service.EvidenceCitation{EvidenceID: c.EvidenceID, QuotedSpan: c.QuotedSpan}
	}
	return service.ClaimVerdict{
		Verdict:    res.Verdict,
		Basis:      res.Basis,
		Confidence: res.Confidence,
		Citations:  citations,
		Rationale:  res.Rationale,
	}, nil
}
