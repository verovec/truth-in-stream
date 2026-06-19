package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/auth"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"golang.org/x/time/rate"
)

// Bearer tokens the stub verifier recognizes, letting handler tests exercise the
// role gate without a live Keycloak: "admin" yields an admin identity, "guest" a
// guest, and anything else fails validation (an invalid token).
const (
	testAdminToken = "admin-token"
	testGuestToken = "guest-token"
)

// stubVerifier maps a known Bearer token to a canned identity and rejects the
// rest, standing in for the Keycloak JWKS verifier in handler tests.
type stubVerifier struct{}

func (stubVerifier) Verify(_ context.Context, raw string) (auth.Identity, error) {
	switch raw {
	case testAdminToken:
		return auth.Identity{Subject: "admin-sub", Username: "admin", Roles: []string{"admin", "guest"}}, nil
	case testGuestToken:
		return auth.Identity{Subject: "guest-sub", Username: "guest", Roles: []string{"guest"}}, nil
	default:
		return auth.Identity{}, auth.ErrInvalidToken
	}
}

// bearer sets the Authorization header to a stub-recognized token.
func bearer(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

const (
	testOperatorEmail    = "op@example.com"
	testOperatorPassword = "correct-password"
	testSessionSecret    = "0123456789abcdef0123456789abcdef"
)

// testOperatorHash is testOperatorPassword hashed once with
// service.OperatorHashParams (via cmd/genhash); a precomputed constant spares
// every handler test run the deliberately expensive argon2id work, and the
// CreateHash-to-Verify round trip is covered in the service and genhash
// packages.
const testOperatorHash = "$argon2id$v=19$m=19456,t=2,p=1$aGdkLE3VqGjWP5xfYazDsg$Sko+9LlAZBrVksQAJiGx3lFVCzuUWbMwRYgJjx8kzi0"

// testSessions is shared by every test server built from globalTestAuth, so
// one issued token is valid against any of them.
var testSessions = service.NewSessions(testSessionSecret, time.Hour)

// globalTestAuth is the default auth fixture: Keycloak-only, with the legacy
// password-session login retired (its routes are not registered). The /api
// subtree is gated solely by the stub Keycloak verifier. Tests that need a
// variant copy the value and swap fields.
var globalTestAuth = AuthConfig{
	Verifier: stubVerifier{},
}

// legacyTestAuth re-enables the retired password-session login so the legacy
// login/logout flow can still be exercised. It carries the password
// collaborators the flag-on path wires.
var legacyTestAuth = func() AuthConfig {
	creds, err := service.NewCredentials(testOperatorEmail, testOperatorHash)
	if err != nil {
		panic(err)
	}
	return AuthConfig{
		Credentials:         creds,
		Sessions:            testSessions,
		SecureCookie:        true,
		LoginLimiter:        middleware.NewRateLimiter(rate.Every(time.Millisecond), 1000),
		Verifier:            stubVerifier{},
		LegacyPasswordLogin: true,
	}
}()

// authCookie issues a session cookie directly, letting non-auth handler tests
// pass the auth gate without running the login flow each time.
func authCookie(t *testing.T) *http.Cookie {
	t.Helper()
	token, err := testSessions.Issue(time.Now())
	if err != nil {
		t.Fatalf("issuing test session: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: token}
}

func loginBody(password string) *strings.Reader {
	b, _ := json.Marshal(map[string]string{"email": testOperatorEmail, "password": password})
	return strings.NewReader(string(b))
}

func loginSessionCookie(t *testing.T, srv http.Handler) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", loginBody(testOperatorPassword)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login = %d, want %d", rec.Code, http.StatusNoContent)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("login response did not set a session cookie")
	return nil
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "correct credentials log in",
			body:     `{"email":"op@example.com","password":"correct-password"}`,
			wantCode: http.StatusNoContent,
		},
		{
			name:     "wrong password rejected",
			body:     `{"email":"op@example.com","password":"wrong"}`,
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "wrong email rejected",
			body:     `{"email":"other@example.com","password":"correct-password"}`,
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "malformed json rejected",
			body:     `{"email":`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "empty body rejected",
			body:     ``,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "oversized body rejected with 413",
			body:     `{"email":"` + strings.Repeat("a", 8<<10) + `"}`,
			wantCode: http.StatusRequestEntityTooLarge,
		},
	}
	srv := newAuthedTestServer(legacyTestAuth, nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", strings.NewReader(tc.body)))
			if rec.Code != tc.wantCode {
				t.Fatalf("POST /api/login = %d, want %d", rec.Code, tc.wantCode)
			}
			if rec.Code == http.StatusNoContent {
				return
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == sessionCookieName {
					t.Fatal("failed login must not set a session cookie")
				}
			}
		})
	}
}

func TestLoginErrorIsGeneric(t *testing.T) {
	srv := newAuthedTestServer(legacyTestAuth, nil)
	bodyFor := func(body string) string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", strings.NewReader(body)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("login = %d, want 401", rec.Code)
		}
		return rec.Body.String()
	}
	wrongPassword := bodyFor(`{"email":"op@example.com","password":"wrong"}`)
	wrongEmail := bodyFor(`{"email":"other@example.com","password":"correct-password"}`)
	if wrongPassword != wrongEmail {
		t.Fatalf("error bodies differ between wrong password (%q) and wrong email (%q); they must not reveal which field was wrong", wrongPassword, wrongEmail)
	}
	if !strings.Contains(wrongPassword, "invalid credentials") {
		t.Fatalf("error body %q should carry the generic invalid credentials message", wrongPassword)
	}
}

func TestLoginCookieAttributes(t *testing.T) {
	srv := newAuthedTestServer(legacyTestAuth, nil)
	cookie := loginSessionCookie(t, srv)

	if cookie.Value == "" {
		t.Fatal("session cookie has no value")
	}
	if !cookie.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
	if !cookie.Secure {
		t.Fatal("session cookie must be Secure by default")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Fatalf("session cookie Path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge != int(time.Hour/time.Second) {
		t.Fatalf("session cookie MaxAge = %d, want %d", cookie.MaxAge, int(time.Hour/time.Second))
	}
}

func TestLoginInsecureCookieFlag(t *testing.T) {
	auth := legacyTestAuth
	auth.SecureCookie = false
	srv := newAuthedTestServer(auth, nil)

	cookie := loginSessionCookie(t, srv)
	if cookie.Secure {
		t.Fatal("session cookie must not be Secure when the insecure dev flag is set")
	}
}

func TestLoginRateLimited(t *testing.T) {
	auth := legacyTestAuth
	auth.LoginLimiter = middleware.NewRateLimiter(rate.Every(10*time.Minute), 2)
	srv := newAuthedTestServer(auth, nil)

	codes := make([]int, 0, 3)
	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", loginBody("wrong"))
		req.RemoteAddr = "10.0.0.1:12345"
		srv.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	want := []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusTooManyRequests}
	for i, code := range codes {
		if code != want[i] {
			t.Fatalf("attempt %d = %d, want %d (all codes: %v)", i+1, code, want[i], codes)
		}
	}
}

func TestLoginRateLimitIgnoresForgedForwardedFor(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        func(i int) string
	}{
		{
			// Behind the load balancer (a private peer), the rightmost entry
			// is appended by the LB itself; rotating the leading entries must
			// not mint fresh buckets.
			name:       "private peer with rotating forged leading entries",
			remoteAddr: "10.0.0.1:12345",
			xff:        func(i int) string { return string(rune('a'+i)) + ".forged.example, 203.0.113.7" },
		},
		{
			// A public peer talking to the server directly chose the whole
			// header, so it is ignored outright and the peer address keys
			// the bucket.
			name:       "public peer with fully forged rotating header",
			remoteAddr: "198.51.100.9:443",
			xff:        func(i int) string { return string(rune('a'+i)) + ".forged.example" },
		},
		{
			// A private hop that forwards the client's header without
			// appending leaves a client-chosen rightmost entry; when it is
			// not an address it must not become the key.
			name:       "private peer with rotating non-address rightmost entries",
			remoteAddr: "10.0.0.1:12345",
			xff:        func(i int) string { return "203.0.113.7, bucket-" + string(rune('a'+i)) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth := legacyTestAuth
			auth.LoginLimiter = middleware.NewRateLimiter(rate.Every(10*time.Minute), 2)
			srv := newAuthedTestServer(auth, nil)

			codes := make([]int, 0, 3)
			for i := range 3 {
				rec := httptest.NewRecorder()
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", loginBody("wrong"))
				req.RemoteAddr = tc.remoteAddr
				req.Header.Set("X-Forwarded-For", tc.xff(i))
				srv.ServeHTTP(rec, req)
				codes = append(codes, rec.Code)
			}
			want := []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusTooManyRequests}
			for i, code := range codes {
				if code != want[i] {
					t.Fatalf("attempt %d = %d, want %d (all codes: %v)", i+1, code, want[i], codes)
				}
			}
		})
	}
}

func TestLogout(t *testing.T) {
	srv := newAuthedTestServer(legacyTestAuth, nil)
	cookie := loginSessionCookie(t, srv)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/logout", nil)
	req.AddCookie(cookie)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/logout = %d, want %d", rec.Code, http.StatusNoContent)
	}
	var cleared *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatal("logout did not set a clearing session cookie")
	}
	if cleared.Value != "" {
		t.Fatalf("logout cookie value = %q, want empty", cleared.Value)
	}
	if cleared.MaxAge >= 0 {
		t.Fatalf("logout cookie MaxAge = %d, want negative to delete", cleared.MaxAge)
	}
}

func TestLogoutWithoutCookieIsNoOp(t *testing.T) {
	srv := newAuthedTestServer(legacyTestAuth, nil)

	// A cross-site forced-logout POST never carries the SameSite=Strict
	// cookie; it must not receive a clearing Set-Cookie either.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/logout", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/logout = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("logout without a session cookie set %d cookie(s); a cookie-less request must be a no-op", len(cookies))
	}
}

// TestAPIIsGatedOnKeycloakIdentity is the cutover's core acceptance check: every
// /api data route is gated on a verified Keycloak identity. A valid Keycloak
// login (admin or guest Bearer token, no legacy password session) reaches the
// route; an anonymous request, an invalid token, and an expired token are each
// rejected with 401. The cases mirror what the Identity verifier reports:
// stubVerifier maps testAdminToken/testGuestToken to identities and rejects
// everything else (an invalid or expired token both surface as ErrInvalidToken).
func TestAPIIsGatedOnKeycloakIdentity(t *testing.T) {
	srv := newTestServer(nil)

	// Real registered data routes (see NewMux), so an admitted caller reaches the
	// handler and a blocked one is stopped at the gate; a non-existent route would
	// 404 after passing the gate and make the "admitted" assertion vacuous.
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/videos"},
		{http.MethodGet, "/api/videos/v1"},
	}
	credentials := []struct {
		name        string
		bearer      string
		wantBlocked bool
	}{
		{name: "valid admin token reaches the route", bearer: testAdminToken, wantBlocked: false},
		{name: "valid guest token reaches the route", bearer: testGuestToken, wantBlocked: false},
		{name: "anonymous is rejected 401", bearer: "", wantBlocked: true},
		{name: "invalid token is rejected 401", bearer: "garbage", wantBlocked: true},
		{name: "expired token is rejected 401", bearer: "expired", wantBlocked: true},
	}
	for _, rt := range routes {
		for _, cred := range credentials {
			t.Run(rt.method+" "+rt.path+"/"+cred.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequestWithContext(t.Context(), rt.method, rt.path, nil)
				if cred.bearer != "" {
					bearer(req, cred.bearer)
				}
				srv.ServeHTTP(rec, req)
				if cred.wantBlocked {
					if rec.Code != http.StatusUnauthorized {
						t.Fatalf("%s = %d, want 401", cred.name, rec.Code)
					}
					return
				}
				// The gate let the verified caller through to a real handler: it is
				// neither rejected by the gate (401) nor missing (404).
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusNotFound {
					t.Fatalf("%s = %d: a verified identity must reach the data route", cred.name, rec.Code)
				}
			})
		}
	}
}

// TestLegacySessionCookieDoesNotGateAPI proves the password-session login is
// retired by default: a backend `session` cookie no longer satisfies the /api
// gate, so an environment that did not opt the legacy flag back in accepts only
// a verified Keycloak token.
func TestLegacySessionCookieDoesNotGateAPI(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	req.AddCookie(authCookie(t))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy session cookie = %d, want 401 (the password session no longer gates /api)", rec.Code)
	}
}

// TestLegacyLoginRoutesRetiredByDefault proves the password login and logout
// routes are not registered when the legacy flag is off: a POST to either is
// caught by the /api identity gate (401), not served as a login endpoint.
func TestLegacyLoginRoutesRetiredByDefault(t *testing.T) {
	srv := newTestServer(nil)
	for _, path := range []string{"/api/login", "/api/logout"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, loginBody(testOperatorPassword)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s with legacy login off = %d, want 401 (route retired)", path, rec.Code)
		}
	}
}

// TestLegacyFlagWidensAPIGateToSessionCookie proves the opt-in: with
// LEGACY_PASSWORD_LOGIN on, a freshly minted password session reaches /api data
// (the gate widens to admit the session cookie), and a Keycloak token still works
// unchanged, while a request with neither credential is still rejected with 401.
func TestLegacyFlagWidensAPIGateToSessionCookie(t *testing.T) {
	srv := newAuthedTestServer(legacyTestAuth, nil)
	cookie := loginSessionCookie(t, srv)

	withSession := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	withSession.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, withSession)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("legacy session cookie = 401, want the gate to admit it when the flag is on")
	}

	withToken := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	bearer(withToken, testGuestToken)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, withToken)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("Keycloak token = 401, want it to keep working with the flag on")
	}

	anon := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, anon)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credential with the flag on = %d, want 401", rec.Code)
	}
}

func TestUnregisteredAPIRoutesAreDeniedByDefault(t *testing.T) {
	srv := newTestServer(nil)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/__unregistered__", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated unregistered route = %d, want 401: the /api subtree must be protected by construction, not per route", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/__unregistered__", nil)
	bearer(req, testGuestToken)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("authenticated unregistered route = %d, want 404", rec.Code)
	}
}

// TestSessionCookieNameMatchesFrontendProxy pins the cross-stack contract:
// the frontend's optimistic gate reads the cookie the backend sets. The
// frontend file is part of the same repository, so a rename that misses one
// side fails here instead of in a deployed environment.
func TestSessionCookieNameMatchesFrontendProxy(t *testing.T) {
	t.Parallel()
	proxySource, err := os.ReadFile("../../../frontend/src/proxy.ts")
	if os.IsNotExist(err) {
		// Backend-only environments (the compose dev container mounts just
		// stack/backend) cannot see the frontend tree; CI and host runs can
		// and keep the contract enforced.
		t.Skipf("frontend tree not present: %v", err)
	}
	if err != nil {
		t.Fatalf("reading frontend proxy source: %v", err)
	}
	want := `const SESSION_COOKIE = "` + sessionCookieName + `"`
	if !strings.Contains(string(proxySource), want) {
		t.Fatalf("frontend proxy.ts does not declare %s; the session cookie name must match on both stacks", want)
	}
}

func TestHealthzIsNotProtected(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz without cookie = %d, want 200", rec.Code)
	}
}
