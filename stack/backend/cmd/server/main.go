// Command server is the truth-in-stream backend API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/verovec/truth-in-stream/backend/internal/checkworthy"
	"github.com/verovec/truth-in-stream/backend/internal/claimdecomp"
	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/handler"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/stance"
	"github.com/verovec/truth-in-stream/backend/internal/storage"
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
	verifyPathCfg, err := config.LoadVerifyPath()
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
	})
	if err != nil {
		return err
	}

	// The retrieve-then-verify path replaces the coverage gate with a check-worthy
	// claim gate (coverage is discovered by retrieval, not pre-judged), so when it
	// is active the prechecker is the coverage-free ClaimGate. When it is off the
	// legacy two-stage gate (claim + coverage) is used, unchanged.
	var prechecker service.SegmentPrechecker
	if verifyPathCfg.Active() {
		prechecker, err = buildClaimGate(precheckCfg, checkWorthinessCfg, logger)
	} else {
		prechecker, err = buildPrechecker(precheckCfg, checkWorthinessCfg, embedder, store, store, logger)
	}
	if err != nil {
		return err
	}

	debugSearch, err := buildDebugSearch(debugSearchCfg, embedder, store)
	if err != nil {
		return err
	}

	stanceClassifier, err := buildStanceClassifier(consistencyCfg, logger)
	if err != nil {
		return err
	}

	segmentMatcher := service.NewSegmentMatchAdapter(matcher)
	verifyMatcher, err := buildVerifyMatcher(verifyPathCfg, matchCfg, embedder, store, segmentMatcher)
	if err != nil {
		return err
	}
	verifyPath, err := buildVerifyPath(verifyPathCfg, verifyMatcher, logger)
	if err != nil {
		return err
	}
	liveAnalyzer, err := service.NewLiveAnalyzer(service.LiveAnalyzerConfig{
		Stream:           liveStream(transcription, logger),
		Matcher:          segmentMatcher,
		Prechecker:       prechecker,
		Logger:           logger,
		Concurrency:      liveCfg.Concurrency,
		QueueDepth:       liveCfg.QueueDepth,
		Stance:           stanceClassifier,
		ConsistencyTopK:  consistencyCfg.TopK,
		ConsistencyFloor: consistencyCfg.SimilarityFloor,
		Verify:           verifyPath,
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
func buildPrechecker(cfg config.Precheck, cw config.CheckWorthiness, embedder service.QueryEmbedder, claims service.ClaimSearcher, wiki service.EvidenceSearcher, logger *slog.Logger) (service.SegmentPrechecker, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	classifier, err := buildClaimClassifier(cfg, cw, logger)
	if err != nil {
		return nil, err
	}
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

// buildClaimClassifier returns the gate's stage-one classifier: the
// deterministic heuristic alone when the model is inactive, or the heuristic
// wrapped in a model cascade when the check-worthiness model is configured. The
// API key is never logged.
func buildClaimClassifier(cfg config.Precheck, cw config.CheckWorthiness, logger *slog.Logger) (service.ClaimClassifier, error) {
	heuristic := service.NewHeuristicClassifier(cfg.MinWords)
	if !cw.Active() {
		return heuristic, nil
	}
	model, err := checkworthy.New(checkworthy.Config{APIKey: cw.APIKey, Model: cw.Model})
	if err != nil {
		return nil, err
	}
	logger.Info("model check-worthiness classifier enabled", slog.String("model", cw.Model))
	return service.NewCascadeClassifier(heuristic, model, logger), nil
}

// buildStanceClassifier wires the intra-speaker consistency stance check, or
// returns a nil classifier (interface, not a typed nil) when the feature is not
// active, so the live analyzer leaves consistency off and behaves exactly as
// before. The API key is never logged.
func buildStanceClassifier(cfg config.Consistency, logger *slog.Logger) (service.StanceClassifier, error) {
	if !cfg.Active() {
		return nil, nil
	}
	client, err := stance.New(stance.Config{APIKey: cfg.APIKey, Model: cfg.Model})
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
func buildClaimGate(cfg config.Precheck, cw config.CheckWorthiness, logger *slog.Logger) (service.SegmentPrechecker, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	classifier, err := buildClaimClassifier(cfg, cw, logger)
	if err != nil {
		return nil, err
	}
	return service.NewClaimGate(classifier), nil
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
func buildVerifyMatcher(cfg config.VerifyPath, matchCfg config.Match, embedder service.QueryEmbedder, store evidenceStore, fallback service.SegmentMatcher) (service.SegmentMatcher, error) {
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
	})
	if err != nil {
		return nil, fmt.Errorf("build verify matcher: %w", err)
	}
	return service.NewSegmentMatchAdapter(matcher), nil
}

// buildVerifyPath wires the retrieve-then-verify orchestration, or returns nil
// (so the analyzer runs the legacy path) when the feature is not active. The
// decomposer and verifier are Anthropic-backed adapters over the shared llm
// transport; both share the configured model. The API key is never logged.
func buildVerifyPath(cfg config.VerifyPath, matcher service.SegmentMatcher, logger *slog.Logger) (*service.VerifyPath, error) {
	if !cfg.Active() {
		return nil, nil
	}
	decomposer, err := claimdecomp.New(claimdecomp.Config{APIKey: cfg.APIKey, Model: cfg.Model, MaxClaimsPerUnit: cfg.MaxClaimsPerUnit})
	if err != nil {
		return nil, err
	}
	verifier, err := verify.New(verify.Config{APIKey: cfg.APIKey, Model: cfg.Model})
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
		Logger:            logger,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("retrieve-then-verify fact-check path enabled", slog.String("model", cfg.Model))
	return path, nil
}

// decomposerAdapter adapts the claimdecomp client to the service ClaimDecomposer
// port: it maps the port's positional arguments onto the client's Input struct.
type decomposerAdapter struct {
	client *claimdecomp.Client
}

func (d decomposerAdapter) Decompose(ctx context.Context, text, speaker, recentContext string) []string {
	return d.client.Decompose(ctx, claimdecomp.Input{Text: text, Speaker: speaker, Context: recentContext})
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
		in[i] = verify.Passage{ID: p.ID, Text: p.Text}
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
		Confidence: res.Confidence,
		Citations:  citations,
		Rationale:  res.Rationale,
	}, nil
}
