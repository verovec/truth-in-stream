//go:build keycloak_smoke

// Package keycloaksmoke holds the local-Keycloak login smoke test. It is gated
// behind the keycloak_smoke build tag so it never compiles into the normal
// `go test ./...` run (which boots only Postgres) and only ever runs in the
// dedicated CI job that brings up the full docker-compose stack. The test
// asserts the three legs of the local sign-in chain end to end, without a
// browser:
//
//  1. A direct-access (password) grant of the seeded admin dev user at the
//     internal token endpoint returns an access token. This proves Keycloak is
//     up, realm.json imported, and the back-channel face (keycloak:8081) is
//     reachable from inside the compose network.
//  2. A representative /api request is rejected (401) with no token and
//     accepted (not 401) with that bearer. This proves the backend's
//     KEYCLOAK_JWKS_URL resolves over keycloak:8081 and the verifier validates
//     the public-issuer token.
//  3. GET http://localhost:3000/auth/login returns a 307 whose Location host is
//     localhost:8081, never keycloak:8081. This proves the browser-facing
//     redirect uses the public face, so a real browser can complete the flow.
//
// Endpoints default to the docker-compose published addresses and are
// overridable via env so the same test runs against any equivalent stack.
package keycloaksmoke

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	// defaultTokenURL is the Keycloak token endpoint reached over the published
	// localhost:8081 port. The smoke test runs on the docker host; inside the
	// compose network a service would use keycloak:8081. Both resolve to the same
	// realm because KC_HOSTNAME_BACKCHANNEL_DYNAMIC serves the back-channel face
	// per request host. The endpoint accepts the seeded admin password grant.
	defaultTokenURL = "http://localhost:8081/realms/truth-in-stream/protocol/openid-connect/token"
	// defaultAPIURL is a representative /api endpoint behind the identity gate.
	// GET /api/videos lists videos and is rejected without a verified token.
	defaultAPIURL = "http://localhost:8080/api/videos"
	// defaultLoginURL is the frontend's browser-facing login route, which must
	// redirect to the public Keycloak authorize endpoint.
	defaultLoginURL = "http://localhost:3000/auth/login"

	// publicHost is the browser face: the only host an authorize/logout redirect
	// may carry. internalHost is the back-channel face and must never leak into a
	// browser redirect.
	publicHost   = "localhost:8081"
	internalHost = "keycloak:8081"

	clientID = "truth-in-stream-web"
	devUser  = "admin"
	devPass  = "test1234"
)

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// tokenEndpoint exchanges the seeded admin user's password for an access token
// via the OIDC direct-access grant. It returns the access token or fails the
// test with the response body so a broken realm import or unreachable
// back-channel host is diagnosable.
func tokenEndpoint(ctx context.Context, t *testing.T, tokenURL string) string {
	t.Helper()

	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", clientID)
	form.Set("username", devUser)
	form.Set("password", devPass)
	form.Set("scope", "openid")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token request to %s failed (is Keycloak up and the realm imported?): %v", tokenURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if payload.AccessToken == "" {
		t.Fatal("token response carried no access_token")
	}
	return payload.AccessToken
}

// TestLoginChain asserts the three legs of the local sign-in chain against the
// booted stack.
func TestLoginChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tokenURL := envOr("KEYCLOAK_SMOKE_TOKEN_URL", defaultTokenURL)
	apiURL := envOr("KEYCLOAK_SMOKE_API_URL", defaultAPIURL)
	loginURL := envOr("KEYCLOAK_SMOKE_LOGIN_URL", defaultLoginURL)

	// Leg 1: password grant of the seeded admin user.
	accessToken := tokenEndpoint(ctx, t, tokenURL)

	// Leg 2a: the /api endpoint rejects an unauthenticated request, proving the
	// identity gate is actually wired (a 200 here would mean the gate is open and
	// leg 2b proves nothing).
	t.Run("api rejects request with no bearer", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			t.Fatalf("build unauthenticated request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("unauthenticated request to %s failed (is the backend up?): %v", apiURL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 without a bearer, got %d", resp.StatusCode)
		}
	})

	// Leg 2b: the same endpoint serves the request with the bearer, proving the
	// backend's KEYCLOAK_JWKS_URL resolves over keycloak:8081 and the verifier
	// validates a public-issuer token. GET /api/videos returns 200 for a valid
	// identity; a 401 is the original JWKS-refresh failure and any other status
	// (5xx, 403) means the bearer did not cleanly pass the gate, so assert the
	// exact success code rather than merely "not 401".
	t.Run("api accepts the keycloak bearer", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			t.Fatalf("build authenticated request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("authenticated request to %s failed: %v", apiURL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatal("backend rejected the Keycloak bearer (401): JWKS refresh or token validation failed")
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 from %s with a valid bearer, got %d", apiURL, resp.StatusCode)
		}
	})

	// Leg 3: the browser-facing login redirect must point at the public face.
	t.Run("login redirects to the public authorize host", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
		if err != nil {
			t.Fatalf("build login request: %v", err)
		}
		// Do not follow the redirect; assert on the Location it returns.
		client := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("login request to %s failed (is the frontend up?): %v", loginURL, err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)

		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected 307 from /auth/login, got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if location == "" {
			t.Fatal("/auth/login 307 carried no Location header")
		}
		loc, err := url.Parse(location)
		if err != nil {
			t.Fatalf("parse Location %q: %v", location, err)
		}
		if loc.Host == internalHost {
			t.Fatalf("/auth/login leaked the back-channel host into the browser redirect: %s", location)
		}
		if loc.Host != publicHost {
			t.Fatalf("/auth/login redirected to %q, want host %s", location, publicHost)
		}
	})
}
