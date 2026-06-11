package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"golang.org/x/time/rate"
)

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

// globalTestAuth is the default auth fixture; tests that need a variant copy
// the value and swap fields.
var globalTestAuth = func() AuthConfig {
	creds, err := service.NewCredentials(testOperatorEmail, testOperatorHash)
	if err != nil {
		panic(err)
	}
	return AuthConfig{
		Credentials:  creds,
		Sessions:     testSessions,
		SecureCookie: true,
		LoginLimiter: middleware.NewRateLimiter(rate.Every(time.Millisecond), 1000),
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

func loginBody(email, password string) *strings.Reader {
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return strings.NewReader(string(b))
}

func loginSessionCookie(t *testing.T, srv http.Handler) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", loginBody(testOperatorEmail, testOperatorPassword)))
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
	srv := newTestServer(nil)
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
	srv := newTestServer(nil)
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
	srv := newTestServer(nil)
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
	auth := globalTestAuth
	auth.SecureCookie = false
	srv := newAuthedTestServer(auth, nil)

	cookie := loginSessionCookie(t, srv)
	if cookie.Secure {
		t.Fatal("session cookie must not be Secure when the insecure dev flag is set")
	}
}

func TestLoginRateLimited(t *testing.T) {
	auth := globalTestAuth
	auth.LoginLimiter = middleware.NewRateLimiter(rate.Every(10*time.Minute), 2)
	srv := newAuthedTestServer(auth, nil)

	codes := make([]int, 0, 3)
	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", loginBody(testOperatorEmail, "wrong"))
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
			auth := globalTestAuth
			auth.LoginLimiter = middleware.NewRateLimiter(rate.Every(10*time.Minute), 2)
			srv := newAuthedTestServer(auth, nil)

			codes := make([]int, 0, 3)
			for i := range 3 {
				rec := httptest.NewRecorder()
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/login", loginBody(testOperatorEmail, "wrong"))
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
	srv := newTestServer(nil)
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
	srv := newTestServer(nil)

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

func TestProtectedRoutes(t *testing.T) {
	srv := newTestServer(nil)
	valid := loginSessionCookie(t, srv)

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/transcripts"},
		{http.MethodPost, "/api/videos"},
		{http.MethodGet, "/api/videos/abc/status"},
		{http.MethodGet, "/api/videos/abc/results"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), rt.method, rt.path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("without cookie = %d, want 401", rec.Code)
			}

			rec = httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), rt.method, rt.path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "garbage"})
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("with garbage cookie = %d, want 401", rec.Code)
			}

			rec = httptest.NewRecorder()
			req = httptest.NewRequestWithContext(t.Context(), rt.method, rt.path, nil)
			req.AddCookie(valid)
			srv.ServeHTTP(rec, req)
			if rec.Code == http.StatusUnauthorized {
				t.Fatal("with valid cookie the route must not reject as unauthenticated")
			}
		})
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
	req.AddCookie(loginSessionCookie(t, srv))
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
