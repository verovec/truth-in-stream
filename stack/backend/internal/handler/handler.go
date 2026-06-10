// Package handler holds HTTP controllers and the router constructor.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
)

// NewMux builds the application router with middleware applied. The whole
// /api subtree is behind the session gate by construction - a new route added
// to the api mux is protected without remembering anything - and so is the
// demo media route, which serves application content. When demoMediaDir is
// non-empty, bundled demo media in that directory is served under /demo/ so
// the browser can play the same source the pipeline transcribes. The only
// public routes are the explicit registrations on the outer mux: /healthz for
// load balancer checks, login, and logout (reachable without a valid session
// so an expired one can still clear its cookie).
func NewMux(health *service.HealthChecker, transcriber transcribe.Transcriber, processing ProcessingService, demoMediaDir string, auth AuthConfig, logger *slog.Logger) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("POST /api/transcripts", transcriptHandler(transcriber, logger))
	api.HandleFunc("POST /api/videos", submitVideoHandler(processing))
	api.HandleFunc("GET /api/videos/{id}/status", videoStatusHandler(processing))
	api.HandleFunc("GET /api/videos/{id}/results", videoResultsHandler(processing))

	guard := middleware.Auth(auth.Sessions, sessionCookieName)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(health))
	mux.HandleFunc("POST /api/login", loginHandler(auth))
	mux.HandleFunc("POST /api/logout", logoutHandler(auth.SecureCookie))
	mux.Handle("/api/", guard(api))
	if demoMediaDir != "" {
		mux.Handle("GET /demo/{name}", guard(demoMediaHandler(demoMediaDir)))
	}

	var h http.Handler = mux
	h = middleware.Logging(logger)(h)
	h = middleware.Recover(logger)(h)
	h = middleware.RequestID(h)
	return h
}
