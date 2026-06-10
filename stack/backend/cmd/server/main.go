// Command server is the truth-in-stream backend API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
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

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	health := service.NewHealthChecker(store)

	scribe := transcribe.New(transcribe.Config{
		APIKey: transcription.APIKey,
		Model:  transcription.Model,
	})
	transcriber := transcribe.NewSourceTranscriber(scribe, cfg.DemoMediaDir)

	embedder := embed.New(embed.Config{APIKey: embedding.APIKey, Model: embedding.Model, Dim: embedding.Dim})
	matcher, err := service.NewMatcher(embedder, store, service.MatcherConfig{
		TopK:             matchCfg.TopK,
		ScoreThreshold:   matchCfg.ScoreThreshold,
		EmbedConcurrency: matchCfg.EmbedConcurrency,
		Timeout:          matchCfg.Timeout,
	})
	if err != nil {
		return err
	}

	processor := service.NewProcessor(service.ProcessorConfig{
		Transcriber: transcriber,
		Matcher:     service.NewSegmentMatchAdapter(matcher),
		Store:       store,
		Logger:      logger,
	})
	processorDone := make(chan struct{})
	go func() {
		defer close(processorDone)
		processor.Run(ctx)
	}()

	apiHandler := handler.NewMux(health, scribe, processor, cfg.DemoMediaDir, auth, logger)
	if cfg.CORSAllowedOrigin != "" {
		apiHandler = middleware.CORS(cfg.CORSAllowedOrigin)(apiHandler)
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: apiHandler,
		// Tight server-wide bounds; the transcript route extends its own
		// deadlines per request via http.ResponseController.
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
		<-processorDone
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		<-processorDone
		return err
	}
}
