// Package handler holds HTTP controllers and the router constructor.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// NewMux builds the application router with middleware applied. The whole
// /api subtree is behind the session gate by construction - a new route added
// to the api mux is protected without remembering anything - and so is the
// demo media route, which serves application content. When demoMediaDir is
// non-empty, bundled demo media in that directory is served under /demo/ so
// the browser can play and live-analyze the bundled sample. The only public
// routes are the explicit registrations on the outer mux: /healthz for load
// balancer checks, login, and logout (reachable without a valid session so an
// expired one can still clear its cookie).
func NewMux(health *service.HealthChecker, videos VideoService, youtube YouTubeService, live LiveAnalyzer, liveAllowedOrigins []string, debugSearch WikiSearcher, demoMediaDir string, auth AuthConfig, logger *slog.Logger) http.Handler {
	api := http.NewServeMux()
	// Video records and uploads (id is the record UUID). See videos.go.
	api.HandleFunc("POST /api/videos/uploads", requestUploadHandler(videos))
	api.HandleFunc("POST /api/videos/youtube", ingestYouTubeHandler(youtube))
	api.HandleFunc("POST /api/videos/{id}/confirm", confirmVideoHandler(videos))
	api.HandleFunc("GET /api/videos", listVideosHandler(videos))
	api.HandleFunc("GET /api/videos/{id}", getVideoHandler(videos))
	// Live fact-check stream (WebSocket). See live.go.
	api.HandleFunc("GET /api/videos/{id}/live", liveHandler(live, liveAllowedOrigins, logger))
	// Developer wiki-search probe (WebSocket), dev only. Registered solely when a
	// searcher is supplied (the debug flag is on), so the route does not exist in
	// production. It shares the live socket's origin allow-list. See debug_search.go.
	if debugSearch != nil {
		api.HandleFunc("GET /api/debug/wiki-search", debugSearchHandler(debugSearch, liveAllowedOrigins, logger))
	}

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
