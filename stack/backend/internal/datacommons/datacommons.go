// Package datacommons is the DB-free DataCommons ClaimReview feed producer. It
// reads the daily ClaimReview data feed DataCommons publishes (the aggregated,
// schema.org-standardized markup created through the Google Fact Check Markup
// Tool and the ClaimReview Read/Write API), keeps only records from an allowlist
// of vetted French fact-check outlets, maps each outlet's rating onto the literal
// accuracy axis, and publishes one self-contained curated-claim job per record to
// the fact-check queue. It never touches the database: every field a
// political_claims row requires travels in the message, so the existing
// fact-check worker fleet (internal/factcheckjob via cmd/factcheckworker) embeds
// and upserts each claim independently.
//
// It is the redundant, non-API path onto the same corpus the Google Fact Check
// Tools producer (internal/factcheckarchive) fills: both publish
// factcheckjob.ClaimJob bodies keyed on the review URL to the same factcheck.claims
// queue, so a claim reviewed by an allowlisted outlet dedupes to one
// political_claims row whichever path ingested it first (the worker's upsert is
// keyed on that URL). Reading the aggregated feed (compilation licensed CC BY,
// per-record licence in each markup's sdLicense) rather than crawling an outlet's
// site keeps ingestion clear of the EU sui generis database right.
//
// It mirrors internal/factcheckarchive: a DB-free producer behind a tiny Publisher
// port, self-contained jobs, idempotent because the stable claim ID (the review
// URL) makes a re-run rewrite the same rows. Unlike the API path it filters by
// outlet allowlist (the feed carries no per-record language tag) and, per the
// card's conservative policy, stores a record whose rating does not map as
// unverifiable rather than skipping it.
package datacommons

import (
	"bufio"
	"compress/gzip"
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

	"github.com/verovec/truth-in-stream/backend/internal/claimnorm"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// defaultFeedURL is the daily ClaimReview data feed DataCommons refreshes each
// day. It is a public, keyless object, so the producer needs no Secrets Manager
// entry (unlike the Google Fact Check Tools API path).
const defaultFeedURL = "https://storage.googleapis.com/datacommons-feeds/claimreview/latest/data.json"

// defaultHTTPTimeout bounds the feed download. The feed is large (hundreds of MB),
// so the timeout is generous; it still fails a hung transfer rather than stalling
// the run forever.
const defaultHTTPTimeout = 10 * time.Minute

// reviewPriority is the queue priority every claim job is published at. All
// curated claims are equally valuable to the fast-path matcher, so the producer
// publishes at the queue ceiling, exactly like the API path.
func reviewPriority(maxPriority uint8) uint8 { return maxPriority }

// Publisher enqueues a marshaled claim job at a priority. The broker client
// adapter at the cmd layer satisfies it, so this package never imports the
// transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config configures a Client. FeedURL is the DataFeed JSON endpoint (the daily
// feed by default, or a historical dump served in the same DataFeed format for a
// one-shot backfill); OutletAllowlist is the set of host substrings a record's
// author URL must contain to be ingested (empty ingests every outlet);
// MaxItems caps how many feed records are examined (0 = the whole feed);
// MaxPriority is the queue's priority ceiling; HTTPClient and Retry override the
// transport for tests.
type Config struct {
	FeedURL         string
	OutletAllowlist []string
	MaxItems        int
	// Format is "datafeed" (schema.org DataFeed JSON, the daily feed) or "ndjson"
	// (one ClaimReview object per line, the one-shot historical dump). Empty defaults
	// to datafeed.
	Format      string
	MaxPriority uint8
	HTTPClient  *http.Client
	Retry       httpx.RetryConfig
}

// Client reads the DataCommons ClaimReview feed and publishes curated-claim jobs.
type Client struct {
	feedURL     string
	allowlist   []string
	maxItems    int
	format      string
	maxPriority uint8
	httpClient  httpx.Doer
}

// New builds a Client, failing fast on missing configuration. The HTTP client is
// wrapped in a retrying doer so a 429/5xx from the feed host is backed off and
// retried rather than failing the run. The allowlist is lower-cased once so
// matching a record's author URL is a case-insensitive substring test.
func New(cfg Config) (*Client, error) {
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("datacommons: max priority must be positive, got %d", cfg.MaxPriority)
	}
	feedURL := cfg.FeedURL
	if feedURL == "" {
		feedURL = defaultFeedURL
	}
	if _, err := url.Parse(feedURL); err != nil {
		return nil, fmt.Errorf("datacommons: parse feed url: %w", err)
	}
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: defaultHTTPTimeout}
	}
	allow := make([]string, 0, len(cfg.OutletAllowlist))
	for _, a := range cfg.OutletAllowlist {
		if trimmed := strings.ToLower(strings.TrimSpace(a)); trimmed != "" {
			allow = append(allow, trimmed)
		}
	}
	format := cfg.Format
	if format == "" {
		format = "datafeed"
	}
	if format != "datafeed" && format != "ndjson" {
		return nil, fmt.Errorf("datacommons: unknown feed format %q", format)
	}
	return &Client{
		feedURL:     feedURL,
		allowlist:   allow,
		maxItems:    cfg.MaxItems,
		format:      format,
		maxPriority: cfg.MaxPriority,
		httpClient:  httpx.NewRetryClient(base, cfg.Retry),
	}, nil
}

// Format returns the decoder the client will use ("datafeed" or "ndjson"), so an
// entrypoint test can assert DATACOMMONS_FEED_FORMAT was actually threaded through.
func (c *Client) Format() string { return c.format }

// Stats summarizes a completed ingest run. Published is how many records became
// self-contained jobs; Unverifiable is the subset of those whose rating did not
// map onto accurate/inaccurate and were stored as unverifiable rather than
// guessed; Skipped is how many records were dropped for being off the outlet
// allowlist or lacking claim text or a review URL. Published+Skipped is how many
// feed records were examined.
type Stats struct {
	Published    int
	Unverifiable int
	Skipped      int
}

// feedItem is one DataFeedItem envelope. Each carries one or more ClaimReview
// records under item; the field names match the schema.org DataFeed the feed
// serves, so the fixtures match the real wire format.
type feedItem struct {
	Item []claimReview `json:"item"`
}

type claimReview struct {
	ClaimReviewed string     `json:"claimReviewed"`
	DatePublished string     `json:"datePublished"`
	URL           string     `json:"url"`
	Author        feedOrg    `json:"author"`
	ReviewRating  feedRating `json:"reviewRating"`
	SdLicense     sdLicense  `json:"sdLicense"`
}

// sdLicense decodes a schema.org sdLicense, which may be a bare URL string or a
// CreativeWork/URL object carrying @id or url. Only the licence URL is kept, so the
// per-record licence can be honored.
type sdLicense struct {
	url string
}

func (l *sdLicense) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err == nil {
			l.url = s
		}
		return nil
	}
	var obj struct {
		ID  string `json:"@id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &obj); err == nil {
		if obj.URL != "" {
			l.url = obj.URL
		} else {
			l.url = obj.ID
		}
	}
	return nil
}

type feedOrg struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// feedRating is the schema.org Rating. alternateName is the outlet's textual
// verdict ("Faux", "Plutôt vrai"); ratingValue/bestRating/worstRating are the
// optional numeric scale (lower value = more false in the ClaimReview convention).
// The numeric fields are flexible because publishers emit them as either JSON
// numbers or quoted strings.
type feedRating struct {
	AlternateName string     `json:"alternateName"`
	RatingValue   feedNumber `json:"ratingValue"`
	BestRating    feedNumber `json:"bestRating"`
	WorstRating   feedNumber `json:"worstRating"`
}

// feedNumber decodes a schema.org numeric field that a publisher may serialize as
// a JSON number or a quoted string. An absent, null, or unparseable value leaves
// it unset rather than failing the whole feed decode, so one malformed rating
// cannot abort the run.
type feedNumber struct {
	set bool
	val float64
}

func (n *feedNumber) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	n.val, n.set = f, true
	return nil
}

// Run downloads the feed, streams its DataFeedItem array, and publishes a
// curated-claim job for each allowlisted record. It decodes item-by-item so the
// hundreds-of-MB feed never lands in memory at once. A record off the allowlist,
// or missing claim text or a review URL, is skipped and counted; a record whose
// rating does not map is published as unverifiable (not skipped). A nil logger
// falls back to slog.Default.
func (c *Client) Run(ctx context.Context, logger *slog.Logger, pub Publisher) (Stats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	body, err := c.fetch(ctx)
	if err != nil {
		return Stats{}, err
	}
	defer func() { _ = body.Close() }()

	var stats Stats
	emit := func(cr claimReview) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.maxItems > 0 && stats.Published+stats.Skipped >= c.maxItems {
			return errStopDecode
		}
		job, unverifiable, ok := c.toJob(cr)
		if !ok {
			stats.Skipped++
			return nil
		}
		encoded, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("datacommons: encode claim job %q: %w", job.ID, err)
		}
		if err := pub.Publish(ctx, encoded, reviewPriority(c.maxPriority)); err != nil {
			return fmt.Errorf("datacommons: publish claim job %q: %w", job.ID, err)
		}
		stats.Published++
		if unverifiable {
			stats.Unverifiable++
		}
		return nil
	}

	switch c.format {
	case "ndjson":
		err = decodeNDJSON(body, emit)
	default:
		err = decodeFeed(body, emit)
	}
	if err != nil && !errors.Is(err, errStopDecode) {
		return stats, err
	}

	logger.InfoContext(ctx, "datacommons claimreview feed ingest finished",
		slog.String("feed", c.feedURL),
		slog.String("format", c.format),
		slog.Int("published", stats.Published),
		slog.Int("unverifiable", stats.Unverifiable),
		slog.Int("skipped", stats.Skipped))
	return stats, nil
}

// errStopDecode unwinds the streaming decode when the MaxItems cap is reached; it
// is not a run failure.
var errStopDecode = errors.New("datacommons: item cap reached")

// toJob converts one feed ClaimReview into a self-contained job. It returns
// ok=false when the record is off the outlet allowlist or lacks claim text, a
// review URL, or a resolvable outlet, so the caller skips it. The bool result
// reports whether the record's rating did not map and was stored as unverifiable,
// so the run can count the conservative fallbacks.
func (c *Client) toJob(cr claimReview) (job factcheckjob.ClaimJob, unverifiable bool, ok bool) {
	text := strings.TrimSpace(cr.ClaimReviewed)
	if text == "" || strings.TrimSpace(cr.URL) == "" {
		return factcheckjob.ClaimJob{}, false, false
	}
	if !c.allowed(cr.Author.URL) {
		return factcheckjob.ClaimJob{}, false, false
	}
	// Honor a per-record sdLicense that forbids reuse, exactly like the outlet reader.
	if claimnorm.LicenseRestricted(cr.SdLicense.url, nil) {
		return factcheckjob.ClaimJob{}, false, false
	}
	outlet := outletOf(cr.Author)
	if outlet == "" {
		return factcheckjob.ClaimJob{}, false, false
	}
	// The canonical review URL is the cross-path dedup key.
	reviewURL := claimnorm.CanonicalURL(cr.URL)
	verdict, mapped := normalizeRating(cr.ReviewRating)
	return factcheckjob.ClaimJob{
		ID:             reviewURL,
		Text:           text,
		LiteralVerdict: string(verdict),
		SourceName:     sourceNameOf(cr.Author),
		SourceURL:      reviewURL,
		QuotedSpan:     text,
		Outlet:         outlet,
		CheckedAt:      normalizeDate(cr.DatePublished),
	}, !mapped, true
}

// allowed reports whether an author URL passes the outlet allowlist. An empty
// allowlist admits every outlet (the operator opted into an unfiltered ingest);
// otherwise the lower-cased author URL must contain one of the allowlisted host
// substrings. The feed carries no per-record language tag, so this outlet
// allowlist is how the French subset is selected.
func (c *Client) allowed(authorURL string) bool {
	if len(c.allowlist) == 0 {
		return true
	}
	u := strings.ToLower(authorURL)
	for _, host := range c.allowlist {
		if strings.Contains(u, host) {
			return true
		}
	}
	return false
}

// outletOf prefers the author URL's host (a stable tag like factuel.afp.com) as
// the outlet, falling back to the display name when the URL is absent or
// unparseable.
func outletOf(a feedOrg) string {
	if host := hostOf(a.URL); host != "" {
		return host
	}
	return strings.TrimSpace(a.Name)
}

// sourceNameOf returns a non-empty display name: political_claims.source_name is
// NOT NULL and the feed does not guarantee the author name, so it falls back to
// the host when the name is absent.
func sourceNameOf(a feedOrg) string {
	if name := strings.TrimSpace(a.Name); name != "" {
		return name
	}
	return hostOf(a.URL)
}

// hostOf returns the lower-cased host of a URL, or "" when it is empty or
// unparseable.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// normalizeDate returns an RFC3339 string for the worker, or "" when the date is
// absent or unparseable (the worker stores the zero time as SQL NULL). The feed
// emits datePublished as either a bare date or RFC3339; both normalize to a stable
// RFC3339 string so a re-run produces byte-identical job bodies (idempotency).
func normalizeDate(raw string) string {
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

// fetch performs the feed GET and returns the response body for streaming. A
// non-2xx status is an error so the caller fails the run rather than decoding an
// error page. The body is the caller's to close.
func (c *Client) fetch(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("datacommons: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("datacommons: fetch feed: %w", redactURLError(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("datacommons: feed returned %d: %s", resp.StatusCode, snippet)
	}
	return c.maybeGunzip(resp)
}

// maybeGunzip transparently decompresses the historical dump when it is served as
// raw gzip. Detection is by SNIFFING the gzip magic bytes (0x1f 0x8b), not the URL
// suffix or headers: net/http.Transport already auto-decompresses a
// Content-Encoding: gzip response and strips the header, so trusting the .gz URL
// would double-decompress such a body. Sniffing the actual bytes decompresses only
// when the body really is gzip (a .gz served without Content-Encoding) and passes an
// already-decompressed body straight through. The returned ReadCloser closes the
// gzip reader (when used) and the underlying body together.
func (c *Client) maybeGunzip(resp *http.Response) (io.ReadCloser, error) {
	br := bufio.NewReader(resp.Body)
	magic, _ := br.Peek(2)
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("datacommons: open gzip: %w", err)
		}
		return bodyReadCloser{r: zr, closers: []io.Closer{zr, resp.Body}}, nil
	}
	return bodyReadCloser{r: br, closers: []io.Closer{resp.Body}}, nil
}

// bodyReadCloser reads from r and closes every underlying closer (the gzip reader,
// when present, and the response body) together.
type bodyReadCloser struct {
	r       io.Reader
	closers []io.Closer
}

func (b bodyReadCloser) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b bodyReadCloser) Close() error {
	var err error
	for _, c := range b.closers {
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	}
	return err
}

// decodeNDJSON streams a newline-delimited (or whitespace-separated) sequence of
// ClaimReview JSON objects — the shape of the DataCommons historical dump — invoking
// emit for each. Like the DataFeed decoder it processes one record at a time so a
// large dump never lands in memory at once.
func decodeNDJSON(r io.Reader, emit func(claimReview) error) error {
	dec := json.NewDecoder(r)
	for dec.More() {
		var cr claimReview
		if err := dec.Decode(&cr); err != nil {
			return fmt.Errorf("datacommons: decode ndjson record: %w", err)
		}
		if err := emit(cr); err != nil {
			return err
		}
	}
	return nil
}

// redactURLError drops the request URL from a transport error so a feed URL that
// ever carried a query token cannot leak into logs; the underlying cause is kept.
func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
