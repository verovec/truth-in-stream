// Package legifrance ingests the French law corpus from the DILA Legifrance API
// (served through the PISTE gateway) into the fact-check corpus through the
// source-connector framework. It starts narrow: a configured list of code
// articles relevant to recurring political claims (immigration, labor,
// security). Each article's consolidated text renders into an attributed French
// evidence passage published as the generic connector.EvidenceJob, drained by the
// generic evidence worker (cmd/evidenceworker) into evidence_chunks, so a "the law
// actually says X" claim is checked against the primary text.
//
// # Authentication and graceful skip
//
// The API is gated by PISTE OAuth2 (client-credentials). The client id and secret
// come from env/secrets only (on-host from Secrets Manager in the cloud), never
// forwarded through the operator command. When the credentials are absent the
// producer degrades to a clean skip (a finished run publishing nothing, surfaced
// through the shared run alert) rather than failing the fleet - so the source can
// be wired and enabled before its credentials are provisioned.
//
// # Verified format, gated capture
//
// The OAuth2 token flow, the endpoints, and the getArticle request/response shape
// are verified against the official PISTE Legifrance API documentation (the DILA
// Swagger on piste.gouv.fr) and community-published examples. Because the endpoint
// is OAuth2-gated, a live sample cannot be captured without operator credentials;
// testdata/get_article.json therefore matches the documented response shape, and a
// real-capture validation against the live API is the operator's activation step
// (it holds the credentials). Strict quota pacing bounds the request rate. See
// docs/fact-check-sources.md.
package legifrance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Default PISTE endpoints. TokenURL is the OAuth2 token endpoint; APIBaseURL is
// the Legifrance engine base the consult endpoints hang off. Both are overridable
// for tests and for the PISTE sandbox.
const (
	DefaultTokenURL   = "https://oauth.piste.gouv.fr/api/oauth/token"
	DefaultAPIBaseURL = "https://api.piste.gouv.fr/dila/legifrance/lf-engine-app"
	// oauthScope is the scope the client-credentials grant requests.
	oauthScope = "openid"
	// tokenExpirySkew is subtracted from a token's lifetime so it is refreshed
	// before it actually expires, never mid-request.
	tokenExpirySkew = 30 * time.Second
)

// Credentials are the PISTE OAuth2 client-credentials. Both must be set for the
// producer to authenticate; an empty pair is the graceful-skip signal.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// Present reports whether both halves of the credential are set.
func (c Credentials) Present() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

// Partial reports whether exactly one half of the credential is set, the
// signature of a config typo (as opposed to a fully-unprovisioned source).
func (c Credentials) Partial() bool {
	return (c.ClientID == "") != (c.ClientSecret == "")
}

// tokenResponse is the OAuth2 token endpoint response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// TokenSource fetches and caches a PISTE OAuth2 access token, refreshing it
// before expiry. It is safe for concurrent use; the producer is single-goroutine
// but the cache guard keeps the type reusable.
type TokenSource struct {
	tokenURL   string
	creds      Credentials
	httpClient *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewTokenSource builds a token source. An empty tokenURL defaults to the PISTE
// production endpoint.
func NewTokenSource(tokenURL string, creds Credentials, httpClient *http.Client) *TokenSource {
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &TokenSource{tokenURL: tokenURL, creds: creds, httpClient: httpClient}
}

// Token returns a valid access token, fetching a new one when the cache is empty
// or about to expire.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.expiry) {
		return t.token, nil
	}
	tok, ttl, err := t.fetch(ctx)
	if err != nil {
		return "", err
	}
	t.token = tok
	t.expiry = time.Now().Add(ttl - tokenExpirySkew)
	return tok, nil
}

// fetch performs the client-credentials grant.
func (t *TokenSource) fetch(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", t.creds.ClientID)
	form.Set("client_secret", t.creds.ClientSecret)
	form.Set("scope", oauthScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("legifrance: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("legifrance: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("legifrance: token request: unexpected status %s", resp.Status)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf("legifrance: decode token: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("legifrance: token response carried no access token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= tokenExpirySkew {
		ttl = tokenExpirySkew + time.Second
	}
	return tr.AccessToken, ttl, nil
}
