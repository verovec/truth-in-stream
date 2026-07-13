// Package tvcapture is the always-on TV capture worker. It reconciles the set of
// enabled TV channels from the backend, and per channel runs a single ffmpeg
// pipeline that streams 16 kHz mono PCM to the live analyzer over a WebSocket and
// (when archiving is enabled) segments the source into MPEG-TS chunks that are
// remuxed to MP4 and uploaded through the backend's presigned recording API.
//
// Every side effect - process exec, HTTP, WebSocket, filesystem, clock - sits
// behind an interface so the supervisor and manager are unit-testable with fakes
// and no real network, subprocess, or wall-clock sleep.
package tvcapture

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenRefreshSkew refetches the client-credentials token this long before it
// actually expires, so an in-flight request never rides an about-to-die token.
const tokenRefreshSkew = 30 * time.Second

// defaultTokenTTL is the assumed lifetime when the token endpoint omits or
// reports a non-positive expires_in. It must exceed tokenRefreshSkew, or the
// cached token would be treated as already expired and refetched on every call.
// 5 minutes matches Keycloak's default access-token lifespan.
const defaultTokenTTL = 5 * time.Minute

// tokenSource caches a Keycloak client-credentials access token and refreshes it
// when it nears expiry. It is safe for concurrent use; the client secret it holds
// is never included in a returned error.
type tokenSource struct {
	httpClient   *http.Client
	tokenURL     string
	clientID     string
	clientSecret string

	// now is the clock, injected so tests can drive expiry deterministically.
	now func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// newTokenSource builds a token source for the given client-credentials client.
func newTokenSource(httpClient *http.Client, tokenURL, clientID, clientSecret string) *tokenSource {
	return &tokenSource{
		httpClient:   httpClient,
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		now:          time.Now,
	}
}

// Token returns a valid access token, fetching a fresh one when the cache is
// empty or within tokenRefreshSkew of expiry. Concurrent callers share one fetch
// via the mutex.
func (s *tokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Before(s.expiresAt.Add(-tokenRefreshSkew)) {
		return s.token, nil
	}

	tok, ttl, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token = tok
	s.expiresAt = s.now().Add(ttl)
	return tok, nil
}

// Invalidate drops the cached token so the next Token call refetches. The
// backend client calls it when a request is rejected as unauthorized, so a token
// invalidated before its computed expiry (Keycloak key rotation, a restart, or
// clock skew) recovers on the next request instead of failing for the whole
// cache window.
func (s *tokenSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.expiresAt = time.Time{}
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (s *tokenSource) fetch(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		// A malformed token URL yields a *url.Error that may embed the request
		// body (the secret); return a clean error instead.
		return "", 0, errors.New("tvcapture: build token request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", 0, errors.New("tvcapture: token request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("tvcapture: token endpoint returned status %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := decodeJSON(resp.Body, &tr); err != nil {
		return "", 0, fmt.Errorf("tvcapture: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, errors.New("tvcapture: token endpoint returned empty access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		// Only a missing or non-positive expires_in falls back to the default. A
		// genuinely short (sub-skew) TTL is honored as-is: Token simply refetches
		// each call, which is correct for a token that really does expire that
		// fast, rather than caching it past its real expiry.
		ttl = defaultTokenTTL
	}
	return tr.AccessToken, ttl, nil
}
