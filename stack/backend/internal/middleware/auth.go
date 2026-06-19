package middleware

import (
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/auth"
)

// SessionVerifier checks a legacy password-session token at a given instant.
type SessionVerifier interface {
	Verify(token string, now time.Time) error
}

// SessionOrIdentity gates the /api subtree when the retired password-session
// login is opted back in (LEGACY_PASSWORD_LOGIN): a caller is admitted by EITHER
// a valid legacy session cookie OR a valid Keycloak identity, so an environment
// with no Keycloak yet can still sign in with the password flow while a Keycloak
// caller works unchanged. The Keycloak path is the authoritative source of role:
// a session-cookie caller is attached the guest identity (the legacy login knows
// no roles), so it can never satisfy an admin gate - admin behavior always rides
// a verified Keycloak admin claim. A caller that presents neither credential, or
// a Keycloak token that fails validation with no valid session cookie, is
// rejected with 401 by the wrapped RequireIdentity. The default /api gate is
// RequireIdentity alone (Keycloak only); this composition is wired only behind
// the legacy flag.
func SessionOrIdentity(sessions SessionVerifier, cookieName string, verifier auth.Verifier) func(http.Handler) http.Handler {
	requireIdentity := RequireIdentity(verifier)
	return func(next http.Handler) http.Handler {
		identityGate := requireIdentity(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cookie, err := r.Cookie(cookieName); err == nil && sessions.Verify(cookie.Value, time.Now()) == nil {
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), auth.GuestIdentity())))
				return
			}
			identityGate.ServeHTTP(w, r)
		})
	}
}
