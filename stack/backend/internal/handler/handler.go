// Package handler holds HTTP controllers and the router constructor.
package handler

import (
	"log/slog"
	"net/http"

	authpkg "github.com/verovec/truth-in-stream/backend/internal/auth"
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
func NewMux(health *service.HealthChecker, videos VideoService, youtube YouTubeService, live LiveAnalyzer, recorder AnalysisRecorder, liveAllowedOrigins []string, debugFactCheck bool, debugSearch WikiSearcher, demoMediaDir string, auth AuthConfig, logger *slog.Logger) http.Handler {
	api := http.NewServeMux()
	// Video records and uploads (id is the record UUID). See videos.go.
	api.HandleFunc("POST /api/videos/uploads", requestUploadHandler(videos))
	api.HandleFunc("POST /api/videos/youtube", ingestYouTubeHandler(youtube))
	api.HandleFunc("POST /api/videos/{id}/confirm", confirmVideoHandler(videos))
	api.HandleFunc("GET /api/videos", listVideosHandler(videos))
	api.HandleFunc("GET /api/videos/{id}", getVideoHandler(videos))
	// Live fact-check stream (WebSocket). See live.go.
	api.HandleFunc("GET /api/videos/{id}/live", liveHandler(live, recorder, liveAllowedOrigins, debugFactCheck, logger))
	// Developer wiki-search probe (WebSocket), dev only. Registered solely when a
	// searcher is supplied (the debug flag is on), so the route does not exist in
	// production. It shares the live socket's origin allow-list. See debug_search.go.
	// Debug endpoints are admin-only by construction: RequireAdmin rejects any
	// caller without a verified admin claim with 403, before the handler runs.
	// The route still only exists when the debug flag is on, so production never
	// even registers it; the admin gate is the second, claim-based lock.
	if debugSearch != nil {
		api.Handle("GET /api/debug/wiki-search", middleware.RequireAdmin(debugSearchHandler(debugSearch, liveAllowedOrigins, logger)))
	}

	guard := middleware.Auth(auth.Sessions, sessionCookieName)
	// The Identity middleware validates a Keycloak Bearer token and attaches the
	// caller's role to the request context for every /api route behind the
	// session gate, so handlers and RequireAdmin read a verified role rather than
	// any client-supplied flag. A request with no token is an anonymous guest; a
	// request with an invalid, expired, or wrong-issuer token is rejected 401. A
	// missing verifier fails closed to a deny-all verifier (every caller a guest),
	// never a nil-deref panic or skipped validation.
	verifier := auth.Verifier
	if verifier == nil {
		verifier = authpkg.DenyVerifier{}
	}
	identity := middleware.Identity(verifier)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(health))
	mux.HandleFunc("POST /api/login", loginHandler(auth))
	mux.HandleFunc("POST /api/logout", logoutHandler(auth.SecureCookie))
	mux.Handle("/api/", guard(identity(api)))
	if demoMediaDir != "" {
		mux.Handle("GET /demo/{name}", guard(identity(demoMediaHandler(demoMediaDir))))
	}

	var h http.Handler = mux
	h = middleware.Logging(logger)(h)
	h = middleware.Recover(logger)(h)
	h = middleware.RequestID(h)
	return h
}
