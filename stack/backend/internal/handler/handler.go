// Package handler holds HTTP controllers and the router constructor.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
)

// NewMux builds the application router with middleware applied.
func NewMux(health *service.HealthChecker, transcriber transcribe.Transcriber, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(health))
	mux.HandleFunc("POST /api/transcripts", transcriptHandler(transcriber, logger))

	var h http.Handler = mux
	h = middleware.Logging(logger)(h)
	h = middleware.Recover(logger)(h)
	h = middleware.RequestID(h)
	return h
}
