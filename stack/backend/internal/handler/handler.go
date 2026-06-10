// Package handler holds HTTP controllers and the router constructor.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
)

// NewMux builds the application router with middleware applied. When
// demoMediaDir is non-empty, bundled demo media in that directory is served
// under /demo/ so the browser can play the same source the pipeline transcribes.
func NewMux(health *service.HealthChecker, transcriber transcribe.Transcriber, processing ProcessingService, demoMediaDir string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(health))
	mux.HandleFunc("POST /api/transcripts", transcriptHandler(transcriber, logger))
	mux.HandleFunc("POST /api/videos", submitVideoHandler(processing))
	mux.HandleFunc("GET /api/videos/{id}/status", videoStatusHandler(processing))
	mux.HandleFunc("GET /api/videos/{id}/results", videoResultsHandler(processing))
	if demoMediaDir != "" {
		mux.HandleFunc("GET /demo/{name}", demoMediaHandler(demoMediaDir))
	}

	var h http.Handler = mux
	h = middleware.Logging(logger)(h)
	h = middleware.Recover(logger)(h)
	h = middleware.RequestID(h)
	return h
}
