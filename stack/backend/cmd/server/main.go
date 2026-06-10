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

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/handler"
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
	processor := service.NewProcessor(service.ProcessorConfig{
		Transcriber: pendingTranscriber{},
		Matcher:     pendingMatcher{},
		Store:       store,
		Logger:      logger,
	})
	processorDone := make(chan struct{})
	go func() {
		defer close(processorDone)
		processor.Run(ctx)
	}()

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: handler.NewMux(health, scribe, processor, logger),
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

// pendingTranscriber stands in for the VER-7 transcription service until it
// merges; jobs fail fast with a clear message instead of hanging. Replace
// with the real Transcriber implementation at wiring time.
type pendingTranscriber struct{}

func (pendingTranscriber) Transcribe(context.Context, string) ([]domain.Segment, error) {
	return nil, errors.New("transcriber not wired yet (pending VER-7)")
}

// pendingMatcher stands in for the VER-9 embed-and-match service until it
// merges. Replace with the real SegmentMatcher implementation at wiring time.
type pendingMatcher struct{}

func (pendingMatcher) Match(context.Context, string) ([]domain.SegmentMatch, error) {
	return nil, errors.New("segment matcher not wired yet (pending VER-9)")
}
