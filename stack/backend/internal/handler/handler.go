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
// /api subtree is behind the Keycloak identity gate by construction - a new
// route added to the api mux is protected without remembering anything - and so
// is the demo media route, which serves application content. The gate validates
// the Keycloak Bearer token (or, for the browser WebSocket upgrade, the
// access_token query parameter) and rejects any caller without a verified
// identity with 401, so /api data is reachable only by a signed-in Keycloak
// caller. The legacy password-session login has been retired: when
// auth.LegacyPasswordLogin is set (a documented opt-in for an environment without
// a Keycloak yet) its routes are registered and the gate widens to also admit a
// valid session cookie, but the admin role still rides a verified Keycloak claim
// only. When demoMediaDir is non-empty, bundled demo media in that directory is
// served under /demo/ so the browser can play and live-analyze the bundled
// sample. The only public route is /healthz for load balancer checks; the legacy
// login and logout routes exist only when the legacy flag is on.
func NewMux(health *service.HealthChecker, videos VideoService, documents DocumentService, documentAnalyzer DocumentAnalyzerService, youtube YouTubeService, live LiveAnalyzer, recorder AnalysisRecorder, replayer AnalysisReplayer, liveAllowedOrigins []string, debugFactCheck bool, debugSearch WikiSearcher, demoMediaDir string, auth AuthConfig, logger *slog.Logger) http.Handler {
	api := http.NewServeMux()
	// Video records and uploads (id is the record UUID). See videos.go.
	api.HandleFunc("POST /api/videos/uploads", requestUploadHandler(videos))
	api.HandleFunc("POST /api/videos/youtube", ingestYouTubeHandler(youtube))
	api.HandleFunc("POST /api/videos/{id}/confirm", confirmVideoHandler(videos))
	api.HandleFunc("GET /api/videos", listVideosHandler(videos))
	api.HandleFunc("GET /api/videos/{id}", getVideoHandler(videos))
	// PDF document records and uploads (id is the record UUID). Reads serve any
	// authenticated user; creating and deleting documents is admin-only, so the
	// mutating routes carry the RequireAdmin gate. See documents.go.
	api.Handle("POST /api/documents/uploads", middleware.RequireAdmin(requestDocumentUploadHandler(documents)))
	api.Handle("POST /api/documents/{id}/extraction", middleware.RequireAdmin(documentExtractionHandler(documents)))
	api.Handle("POST /api/documents/{id}/reanalyse", middleware.RequireAdmin(reanalyseDocumentHandler(documentAnalyzer)))
	api.HandleFunc("GET /api/documents", listDocumentsHandler(documents))
	api.HandleFunc("GET /api/documents/{id}", getDocumentHandler(documents))
	api.HandleFunc("GET /api/documents/{id}/claims", documentClaimsHandler(documents))
	api.Handle("DELETE /api/documents/{id}", middleware.RequireAdmin(deleteDocumentHandler(documents)))
	// Live fact-check stream (WebSocket). See live.go.
	api.HandleFunc("GET /api/videos/{id}/live", liveHandler(live, recorder, replayer, liveAllowedOrigins, debugFactCheck, logger))
	// Admin-only exports of a completed video's cached analysis: an SRT transcript
	// and a CSV decision trace. They read the snapshot the live route persists via
	// the same replayer and never run transcription or an LLM. RequireAdmin gates
	// them on a verified admin claim with 403, mirroring the debug surface. See
	// export.go.
	api.Handle("GET /api/videos/{id}/export/transcript.srt", middleware.RequireAdmin(exportTranscriptHandler(videos, replayer)))
	api.Handle("GET /api/videos/{id}/export/claims.csv", middleware.RequireAdmin(exportClaimsHandler(videos, replayer)))
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

	// The /api gate validates a Keycloak access token and attaches the caller's
	// verified role to the request context for every /api route, rejecting any
	// caller without a valid token with 401. Handlers and RequireAdmin read a
	// verified role rather than any client-supplied flag; a request with an
	// invalid, expired, or wrong-issuer token, or no token at all, never reaches
	// them. A missing verifier fails closed to a deny-all verifier (every caller
	// rejected), never a nil-deref panic or skipped validation.
	verifier := auth.Verifier
	if verifier == nil {
		verifier = authpkg.DenyVerifier{}
	}
	// By default the gate is the Keycloak identity alone. When the retired
	// password login is opted back in (and its session verifier is actually wired),
	// it widens to admit a valid legacy session cookie as well, so an environment
	// without a Keycloak can still sign in; the Keycloak claim remains the only
	// source of the admin role. If the flag is set without a session verifier (a
	// misconfiguration), the gate fails closed to Keycloak-only rather than wiring a
	// nil verifier that would panic on the first cookie-bearing request.
	legacyLogin := auth.LegacyPasswordLogin && auth.Sessions != nil
	gate := middleware.RequireIdentity(verifier)
	if legacyLogin {
		gate = middleware.SessionOrIdentity(auth.Sessions, sessionCookieName, verifier)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(health))
	// The legacy password-session login is retired by default. Its routes are
	// registered only when explicitly opted in and its session verifier is wired
	// (an environment with no Keycloak yet); when off, the session collaborators
	// are nil and /api is gated solely by the Keycloak identity above.
	if legacyLogin {
		mux.HandleFunc("POST /api/login", loginHandler(auth))
		mux.HandleFunc("POST /api/logout", logoutHandler(auth.SecureCookie))
	}
	mux.Handle("/api/", gate(api))
	if demoMediaDir != "" {
		mux.Handle("GET /demo/{name}", gate(demoMediaHandler(demoMediaDir)))
	}

	var h http.Handler = mux
	h = middleware.Logging(logger)(h)
	h = middleware.Recover(logger)(h)
	h = middleware.RequestID(h)
	return h
}
