// Command tvcapture is the always-on TV capture worker. It polls the backend's
// TV channel registry and, per enabled channel, runs a single ffmpeg pipeline
// that streams 16 kHz mono PCM to the live analyzer over a WebSocket and (when
// the channel archives) segments the source into MPEG-TS chunks it remuxes to
// MP4 and uploads through the backend's presigned recording API. It restarts a
// dead capture with backoff, prunes recordings past retention daily, and idles
// (rather than exiting) when capture is disabled so restart: unless-stopped does
// not crash-loop it.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/tvcapture"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("tvcapture exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadTVCapture()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !cfg.Active() {
		logger.InfoContext(ctx, "tvcapture idle: capture disabled, waiting for signal")
		<-ctx.Done()
		return nil
	}

	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return err
	}

	notifier := crawlnotify.NewNotifier(cfg.SlackWebhookURL)
	manager := tvcapture.NewWorker(cfg, notifier, logger)

	logger.InfoContext(ctx, "tvcapture started",
		slog.String("backend", cfg.BackendBaseURL),
		slog.Duration("poll", cfg.PollInterval),
		slog.Int("retention_days", cfg.RetentionDays),
		slog.Duration("segment", cfg.SegmentDuration))

	manager.Run(ctx)

	logger.InfoContext(ctx, "tvcapture stopped")
	return nil
}
