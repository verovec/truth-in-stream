package handler

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// sessionCookieName is the session cookie shared by the login handler, the
// logout handler, the auth middleware wiring in NewMux, and the frontend
// proxy gate (stack/frontend/src/proxy.ts); rename all four together.
const sessionCookieName = "session"

// maxLoginBodyBytes bounds the login payload; an email and a password never
// need more than a few hundred bytes.
const maxLoginBodyBytes = 4 << 10

// AuthConfig carries the wired authentication collaborators into NewMux.
type AuthConfig struct {
	Credentials  *service.Credentials
	Sessions     *service.Sessions
	SecureCookie bool
	LoginLimiter *middleware.RateLimiter
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func loginHandler(auth AuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.LoginLimiter.Allow(clientIP(r)) {
			httpx.Error(w, http.StatusTooManyRequests, "too many login attempts")
			return
		}
		var req loginRequest
		if !decodeJSONBody(w, r, maxLoginBodyBytes, &req) {
			return
		}
		if !auth.Credentials.Verify(req.Email, req.Password) {
			httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		token, err := auth.Sessions.Issue(time.Now())
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		http.SetCookie(w, sessionCookie(auth.SecureCookie, token, int(auth.Sessions.TTL()/time.Second)))
		w.WriteHeader(http.StatusNoContent)
	}
}

func logoutHandler(secureCookie bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only clear when the request carries the cookie. A cross-site
		// forced-logout POST cannot (SameSite=Strict keeps the cookie at
		// home), so it degrades to a no-op; a same-origin logout always
		// carries it, even expired, and still gets the cookie cleared.
		if _, err := r.Cookie(sessionCookieName); err == nil {
			http.SetCookie(w, sessionCookie(secureCookie, "", -1))
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// sessionCookie builds both the issuing and the clearing variant of the
// session cookie; browsers only delete a cookie when name and path match the
// original, so the attributes must come from one place.
func sessionCookie(secure bool, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// clientIP keys the login rate limit. X-Forwarded-For is only honored when
// the direct peer is a private or loopback address - i.e. our own load
// balancer, which appends the real client as the rightmost entry. A public
// peer talking to the server directly chose every entry itself, so its own
// address is the only trustworthy key; anything else would let a brute-forcer
// mint a fresh rate bucket per request.
func clientIP(r *http.Request) string {
	peer := peerHost(r.RemoteAddr)
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" || !isPrivateHost(peer) {
		return peer
	}
	entries := strings.Split(xff, ",")
	forwarded := strings.TrimSpace(entries[len(entries)-1])
	// A trusted proxy appends a real address; anything that does not parse
	// as one (empty entry, garbage from a forwarding-but-not-appending hop)
	// falls back to the peer rather than handing the client its own key.
	if _, err := netip.ParseAddr(forwarded); err == nil {
		return forwarded
	}
	return peer
}

func peerHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// isPrivateHost reports whether host can only be our own infrastructure: the
// load balancer in private subnets, a link-local hop, or local dev. Note an
// IPv6 dual-stack target group would hand tasks global-unicast peers; if the
// stack ever moves off IPv4 targets this gate must learn the VPC's prefix.
func isPrivateHost(host string) bool {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}
