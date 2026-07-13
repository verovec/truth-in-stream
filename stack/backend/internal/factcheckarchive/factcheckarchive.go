// Package factcheckarchive is the DB-free fact-check-archive producer. It reads
// already-checked French claims from the Google Fact Check Tools API
// (claims:search, languageCode=fr), the standardized aggregator of schema.org
// ClaimReview data published by Les Decodeurs, AFP Factuel, and franceinfo Vrai
// ou Fake, maps each outlet's verdict onto the literal accuracy axis, and
// publishes one self-contained curated-claim job per reviewed claim to the
// fact-check queue. It never touches the database: every field a political_claims
// row requires travels in the message, so the worker fleet (internal/factcheckjob
// via cmd/factcheckcrawl's worker) embeds and upserts independently. Reading the
// API rather than scraping article HTML keeps ingestion within each outlet's ToS
// (the API is a sanctioned machine-readable feed, not a crawl of paywalled pages).
//
// It mirrors the wiki crawl producer (internal/wiki/crawlproduce.go): a DB-free
// producer behind a tiny Publisher port, self-contained jobs, idempotent because
// the stable claim ID (the fact-check URL) makes a re-run rewrite the same rows.
package factcheckarchive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// defaultBaseURL is the Google Fact Check Tools claims:search endpoint.
const defaultBaseURL = "https://factchecktools.googleapis.com/v1alpha1/claims:search"

// apiPageSize is the per-request claim cap. The API caps pageSize at its own
// ceiling; requesting the documented maximum minimizes round trips while
// nextPageToken still drives the full walk.
const apiPageSize = 50

// reviewPriority is the queue priority every claim job is published at. All
// curated claims are equally valuable to the fast-path matcher, so there is no
// per-claim band; the producer publishes at the queue ceiling.
func reviewPriority(maxPriority uint8) uint8 { return maxPriority }

// Publisher enqueues a marshaled claim job at a priority. The broker client
// adapter at the cmd layer satisfies it, so this package never imports the
// transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config configures a Client. APIKey is the Google Fact Check Tools API key
// (from configuration only, never logged); LanguageCode filters claims by
// language (fr); MaxPriority is the queue's priority ceiling; BaseURL and
// HTTPClient override the endpoint and transport for tests. A nil HTTPClient
// falls back to a bounded default.
type Config struct {
	APIKey       string
	LanguageCode string
	MaxPriority  uint8
	BaseURL      string
	// HTTPClient overrides the base HTTP client; it is wrapped in a retrying doer.
	HTTPClient *http.Client
	// Retry tunes the retry/backoff wrapper; a zero value uses the httpx defaults.
	Retry httpx.RetryConfig
}

// defaultHTTPTimeout bounds each claims:search request so a slow endpoint fails
// the request rather than stalling the whole ingest.
const defaultHTTPTimeout = 30 * time.Second

// Client reads fact-check archives from the Fact Check Tools API and publishes
// curated-claim jobs.
type Client struct {
	apiKey      string
	language    string
	maxPriority uint8
	baseURL     string
	httpClient  httpx.Doer
}

// New builds a Client, failing fast on missing configuration. The HTTP client is
// wrapped in a retrying doer so a 429/5xx from the Fact Check Tools API is backed
// off and retried (honoring Retry-After) rather than failing the run; the URL
// (which carries the API key in its query) is still redacted from any surfaced
// error by the caller of Do.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("factcheckarchive: api key is required")
	}
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("factcheckarchive: max priority must be positive, got %d", cfg.MaxPriority)
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		apiKey:      cfg.APIKey,
		language:    cfg.LanguageCode,
		maxPriority: cfg.MaxPriority,
		baseURL:     baseURL,
		httpClient:  httpx.NewRetryClient(base, cfg.Retry),
	}, nil
}

// RunConfig tunes one ingest run. Query is the claims:search query string (a
// broad topical term such as a politician's name or a policy area); MaxPages caps
// how many result pages are followed (0 = follow every page to the end).
type RunConfig struct {
	Query    string
	MaxPages int
}

// Stats summarizes a completed ingest run. Published is how many reviewed claims
// became self-contained jobs; Skipped is how many were dropped for lacking a
// mappable verdict, claim text, or source url. Their sum is the number of claims
// the API returned across every followed page.
type Stats struct {
	Published int
	Skipped   int
}

// searchResponse is the claims:search response envelope. The field names and
// shape match the Google Fact Check Tools API (and its google-api-go-client
// structs) exactly, so the fixtures match the real wire format.
type searchResponse struct {
	Claims        []claim `json:"claims"`
	NextPageToken string  `json:"nextPageToken"`
}

type claim struct {
	Text        string        `json:"text"`
	Claimant    string        `json:"claimant"`
	ClaimDate   string        `json:"claimDate"`
	ClaimReview []claimReview `json:"claimReview"`
}

type claimReview struct {
	Publisher     publisher `json:"publisher"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	ReviewDate    string    `json:"reviewDate"`
	TextualRating string    `json:"textualRating"`
	LanguageCode  string    `json:"languageCode"`
}

type publisher struct {
	Name string `json:"name"`
	Site string `json:"site"`
}

// Run pages through the API for the configured query, following nextPageToken to
// the end (or MaxPages), maps each reviewed claim onto a curated-claim job, and
// publishes it. Pagination MUST be followed or most archived claims are silently
// dropped; the loop stops only when the API returns no nextPageToken. A claim
// whose verdict cannot be mapped, or that carries no review or claim text, is
// skipped and counted rather than published with an empty verdict. A nil logger
// falls back to slog.Default.
func (c *Client) Run(ctx context.Context, logger *slog.Logger, pub Publisher, cfg RunConfig) (Stats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var stats Stats
	pageToken := ""
	for page := 0; cfg.MaxPages <= 0 || page < cfg.MaxPages; page++ {
		resp, err := c.fetch(ctx, cfg.Query, pageToken)
		if err != nil {
			return stats, err
		}
		for _, cl := range resp.Claims {
			job, ok := c.toJob(cl)
			if !ok {
				stats.Skipped++
				continue
			}
			body, err := json.Marshal(job)
			if err != nil {
				return stats, fmt.Errorf("factcheckarchive: encode claim job %q: %w", job.ID, err)
			}
			if err := pub.Publish(ctx, body, reviewPriority(c.maxPriority)); err != nil {
				return stats, fmt.Errorf("factcheckarchive: publish claim job %q: %w", job.ID, err)
			}
			stats.Published++
		}
		// Stop at the end of the result set. The empty-claims guard defends
		// against a degenerate response that returns a continuation token with no
		// claims, which would otherwise spin forever under MaxPages=0.
		if resp.NextPageToken == "" || len(resp.Claims) == 0 {
			break
		}
		pageToken = resp.NextPageToken
	}
	logger.InfoContext(ctx, "fact-check archive ingest finished",
		slog.String("query", cfg.Query),
		slog.Int("published", stats.Published),
		slog.Int("skipped", stats.Skipped))
	return stats, nil
}

// toJob converts one API claim into a self-contained job, choosing its first
// review whose verdict maps onto the literal axis. It returns ok=false when the
// claim has no usable review, no mappable verdict, no claim text, or no source
// url, so the caller skips it instead of publishing an unstorable job.
func (c *Client) toJob(cl claim) (factcheckjob.ClaimJob, bool) {
	if cl.Text == "" {
		return factcheckjob.ClaimJob{}, false
	}
	for _, r := range cl.ClaimReview {
		verdict, ok := mapVerdict(r.TextualRating)
		if !ok || r.URL == "" || outletOf(r.Publisher) == "" {
			continue
		}
		return factcheckjob.ClaimJob{
			ID:             r.URL,
			Text:           cl.Text,
			LiteralVerdict: string(verdict),
			SourceName:     sourceNameOf(r.Publisher),
			SourceURL:      r.URL,
			QuotedSpan:     cl.Text,
			Outlet:         outletOf(r.Publisher),
			CheckedAt:      normalizeReviewDate(r.ReviewDate),
		}, true
	}
	return factcheckjob.ClaimJob{}, false
}

// outletOf prefers the publisher's site (a stable host like factuel.afp.com) as
// the outlet tag, falling back to its display name when the API omits the site.
func outletOf(p publisher) string {
	if p.Site != "" {
		return p.Site
	}
	return p.Name
}

// sourceNameOf returns a non-empty display name for the source. The
// political_claims.source_name column is NOT NULL, and the API does not
// guarantee publisher.name is present, so it falls back to the site host when
// the name is absent.
func sourceNameOf(p publisher) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Site
}

// normalizeReviewDate returns an RFC3339 string for the worker, or "" when the
// date is absent or unparseable (the worker stores the zero time as SQL NULL).
// The API emits reviewDate as RFC3339 already, but some publishers surface a
// bare date; both are normalized to a stable RFC3339 string so a re-run produces
// byte-identical job bodies (idempotency).
func normalizeReviewDate(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// fetch performs one claims:search request and decodes the response. A non-2xx
// status is an error so the caller can fail the run rather than silently ingest
// a partial archive.
func (c *Client) fetch(ctx context.Context, query, pageToken string) (searchResponse, error) {
	endpoint, err := url.Parse(c.baseURL)
	if err != nil {
		return searchResponse{}, fmt.Errorf("factcheckarchive: parse base url: %w", err)
	}
	q := endpoint.Query()
	q.Set("key", c.apiKey)
	q.Set("query", query)
	q.Set("pageSize", strconv.Itoa(apiPageSize))
	if c.language != "" {
		q.Set("languageCode", c.language)
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return searchResponse{}, fmt.Errorf("factcheckarchive: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// A transport failure is wrapped by net/http as *url.Error, whose Error()
		// embeds the full request URL - including the API key in the query string.
		// Strip the URL so the key never reaches a log line, keeping only the
		// underlying cause.
		return searchResponse{}, fmt.Errorf("factcheckarchive: claims search: %w", redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return searchResponse{}, fmt.Errorf("factcheckarchive: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return searchResponse{}, fmt.Errorf("factcheckarchive: claims search returned %d: %s", resp.StatusCode, truncate(body, 256))
	}
	var out searchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return searchResponse{}, fmt.Errorf("factcheckarchive: decode response: %w", err)
	}
	return out, nil
}

// truncate bounds an error-message body so a large response cannot bloat a log
// line; the API key never appears in the body, only in the query string.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// redactURLError returns an error whose message never carries the request URL.
// net/http wraps a transport failure as *url.Error, and *url.Error.Error()
// includes the full URL (with the API key in the query string), so propagating
// it verbatim would leak the secret into logs. The underlying cause (DNS,
// timeout, TLS, connection refused) is preserved; only the op and URL are
// dropped. A non-url.Error is returned unchanged.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
