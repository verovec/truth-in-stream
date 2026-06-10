package middleware

import (
	"net/http"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// SessionVerifier checks a session token at a given instant.
type SessionVerifier interface {
	Verify(token string, now time.Time) error
}

// Auth rejects requests that do not carry a valid session cookie with a 401.
// The error body never says why the session was rejected; missing, tampered,
// and expired all look identical to the client.
func Auth(sessions SessionVerifier, cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cookieName)
			if err != nil || sessions.Verify(cookie.Value, time.Now()) != nil {
				httpx.Error(w, http.StatusUnauthorized, "authentication required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
