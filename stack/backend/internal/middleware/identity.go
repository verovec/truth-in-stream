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
