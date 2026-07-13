package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/auth"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// identityKey is the unexported context key the validated identity is stored
// under. An unexported key type cannot collide with any other package's context
// values.
type identityKey struct{}

// Identity validates a Bearer access token and attaches the caller identity to
// the request context. A request with no Authorization header is an anonymous
// guest and passes through with the guest identity, so the broad session gate
// and the role gate stay independent: presenting no token is allowed (you are a
// guest), but presenting a token that does not validate is rejected with 401, so
// an invalid, expired, or wrong-issuer token never silently downgrades to guest.
// Handlers and the RequireAdmin gate read the attached identity; they can never
// be told a caller is admin by anything but a verified token.
func Identity(verifier auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				if hasAuthorization(r) {
					httpx.Error(w, http.StatusUnauthorized, "invalid authorization")
					return
				}
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), auth.GuestIdentity())))
				return
			}
			id, err := verifier.Verify(r.Context(), raw)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "invalid authorization")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

// RequireIdentity validates a Keycloak access token and attaches the verified
// caller identity to the request context, rejecting any request that does not
// present a valid token with 401. It is the broad gate for the /api subtree
// after the legacy password-session login was retired: presenting no credential
// is no longer an allowed anonymous guest here (that is 401), and presenting an
// invalid, expired, or wrong-issuer token is 401 too, so /api data is reachable
// only by a verified Keycloak identity. The token is taken from the Authorization
// Bearer header, or - for the browser WebSocket upgrade, which cannot set that
// header - from the access_token query parameter (RFC 6750 section 2.3). Downstream
// handlers and RequireAdmin then read the verified role from the context exactly
// as before; the only change from Identity is that the anonymous case fails closed
// with 401 instead of degrading to guest.
func RequireIdentity(verifier auth.Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := accessToken(r)
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "authentication required")
				return
			}
			id, err := verifier.Verify(r.Context(), raw)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "authentication required")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

// RequireAdmin rejects a request whose attached identity is not a verified admin
// with 403, before the protected handler runs. It reads only the identity the
// Identity middleware placed on the context, so the admin gate is enforced
// server-side and no client-supplied flag can satisfy it.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := auth.RequireAdmin(IdentityFrom(r.Context())); err != nil {
			httpx.Error(w, http.StatusForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCaptureService rejects a request whose attached identity is neither a
// verified admin nor the tvcapture service account (the tv-capture role) with
// 403, before the protected handler runs. It gates the TV capture write-path so
// the worker's credential is scoped to capture rather than blanket admin.
func RequireCaptureService(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := auth.RequireCaptureService(IdentityFrom(r.Context())); err != nil {
			httpx.Error(w, http.StatusForbidden, "capture service role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WithIdentity returns a copy of ctx carrying the validated identity.
func WithIdentity(ctx context.Context, id auth.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity attached to ctx, defaulting to an anonymous
// guest when none is present. The default is guest, never admin, so a code path
// that forgets to run the Identity middleware fails closed.
func IdentityFrom(ctx context.Context) auth.Identity {
	id, ok := ctx.Value(identityKey{}).(auth.Identity)
	if !ok {
		return auth.GuestIdentity()
	}
	return id
}

// accessTokenParam is the query-parameter name a WebSocket upgrade carries its
// access token in. The browser WebSocket API cannot set request headers, so the
// Authorization header is unavailable on the handshake; access_token is the name
// RFC 6750 section 2.3 defines for this case. It is honored only on a WebSocket
// upgrade (see accessToken), and the access-log middleware logs only the request
// path, never the query string, so the token is not persisted to logs.
const accessTokenParam = "access_token"

// accessToken extracts the raw access token from the Authorization Bearer header,
// falling back to the access_token query parameter only on a WebSocket upgrade
// request. The header is the only path for an ordinary /api request, so a regular
// REST client's token can never land in the URL (and thence a proxy/ALB access
// log); the query parameter is admitted solely for the browser WebSocket upgrade,
// which cannot set the header. It reports whether a non-empty token was found.
func accessToken(r *http.Request) (string, bool) {
	if raw, ok := bearerToken(r); ok {
		return raw, true
	}
	if isWebSocketUpgrade(r) {
		if raw := strings.TrimSpace(r.URL.Query().Get(accessTokenParam)); raw != "" {
			return raw, true
		}
	}
	return "", false
}

// isWebSocketUpgrade reports whether the request is a WebSocket handshake, the
// only request shape allowed to carry its access token as a query parameter.
// Both header checks are token-list, case-insensitive matches per RFC 6455: the
// Connection header is a comma-separated list that must contain "upgrade", and
// the Upgrade header names the "websocket" protocol.
func isWebSocketUpgrade(r *http.Request) bool {
	return tokenListContains(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

// tokenListContains reports whether a comma-separated header value contains the
// given token, matched case-insensitively with surrounding whitespace trimmed.
func tokenListContains(header, want string) bool {
	for token := range strings.SplitSeq(header, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

// bearerToken extracts the raw token from an "Authorization: Bearer <token>"
// header, reporting whether a non-empty bearer token was present. The scheme
// match is case-insensitive per RFC 7235, so a client that lowercases "bearer"
// is not rejected as malformed.
func bearerToken(r *http.Request) (string, bool) {
	scheme, rest, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

// hasAuthorization reports whether the request carries any Authorization header,
// used to tell a missing header (anonymous guest, allowed) from a malformed or
// non-Bearer one (rejected).
func hasAuthorization(r *http.Request) bool {
	return r.Header.Get("Authorization") != ""
}
