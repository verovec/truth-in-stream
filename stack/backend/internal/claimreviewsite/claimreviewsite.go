// Package claimreviewsite is the DB-free ClaimReview JSON-LD outlet reader. It
// discovers fact-check article URLs from an allowlist of vetted French outlets via
// their sitemaps, fetches each page, extracts ONLY the schema.org ClaimReview
// fields embedded as JSON-LD (claim text, rating, review URL, date, outlet), and
// publishes one self-contained curated-claim job per record to the fact-check
// queue for the existing worker to embed and upsert into political_claims.
//
// It is the direct-read companion to the API and feed paths (internal/factcheckarchive,
// internal/datacommons), used only for outlets whose reviews the API and feed miss.
// Because repeated systematic extraction from one outlet's site risks the EU sui
// generis database right, it is deliberately conservative: it is allowlist-driven
// (EFCSN/IFCN-derived, config-curated), sitemap-based (no link spidering),
// robots.txt-respecting, per-outlet-paced, capped per outlet, and it stores ONLY
// the categorical ClaimReview fields — never the article body prose. A record's
// sdLicense is honored where present. Dedup with the other paths is by review URL,
// the stable claim ID, so the worker's upsert collapses duplicates to one row.
package claimreviewsite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/claimnorm"
	"github.com/verovec/truth-in-stream/backend/internal/claimrating"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// Outlet is one allowlisted fact-check outlet: a display Name, the Host its reviews
// live on (the outlet tag and robots authority), and a Sitemap URL to discover its
// article pages from.
type Outlet struct {
	Name    string
	Host    string
	Sitemap string
}

// Publisher enqueues a marshaled claim job at a priority; the cmd-layer broker
// adapter satisfies it, so this package never imports the transport.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config configures a Client. Outlets is the allowlist; UserAgent identifies the
// bot to robots.txt and each request; MinDelay is the per-outlet pacing floor
// (raised by a robots Crawl-delay); MaxURLsPerOutlet caps pages fetched per outlet;
// MaxPriority is the queue ceiling; RestrictiveLicenses overrides the sdLicense
// denylist; HTTPClient and Retry override the transport for tests.
type Config struct {
	Outlets             []Outlet
	UserAgent           string
	MinDelay            time.Duration
	MaxURLsPerOutlet    int
	MaxPriority         uint8
	RestrictiveLicenses []string
	HTTPClient          *http.Client
	Retry               httpx.RetryConfig
}

const (
	defaultUserAgent   = "truth-in-stream-factcheck-bot"
	defaultMinDelay    = 2 * time.Second
	defaultMaxURLs     = 200
	defaultHTTPTimeout = 30 * time.Second
	defaultMaxSitemaps = 20
)

// Client reads ClaimReview JSON-LD from the allowlisted outlets.
type Client struct {
	outlets     []Outlet
	userAgent   string
	minDelay    time.Duration
	maxURLs     int
	maxPriority uint8
	restrictive []string
	httpClient  httpx.Doer
	sleep       func(context.Context, time.Duration) error
}

// New builds a Client, failing fast on missing configuration and defaulting the
// pacing, cap, user-agent, and licence denylist.
func New(cfg Config) (*Client, error) {
	if cfg.MaxPriority < 1 {
		return nil, fmt.Errorf("claimreviewsite: max priority must be positive, got %d", cfg.MaxPriority)
	}
	if len(cfg.Outlets) == 0 {
		return nil, fmt.Errorf("claimreviewsite: at least one outlet is required")
	}
	for _, o := range cfg.Outlets {
		if o.Host == "" || o.Sitemap == "" {
			return nil, fmt.Errorf("claimreviewsite: outlet %q needs a host and a sitemap url", o.Name)
		}
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	delay := cfg.MinDelay
	if delay <= 0 {
		delay = defaultMinDelay
	}
	maxURLs := cfg.MaxURLsPerOutlet
	if maxURLs <= 0 {
		maxURLs = defaultMaxURLs
	}
	restrictive := cfg.RestrictiveLicenses
	if restrictive == nil {
		restrictive = claimnorm.DefaultRestrictiveLicenses
	}
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		outlets:     cfg.Outlets,
		userAgent:   ua,
		minDelay:    delay,
		maxURLs:     maxURLs,
		maxPriority: cfg.MaxPriority,
		restrictive: restrictive,
		httpClient:  httpx.NewRetryClient(base, cfg.Retry),
		sleep:       sleepCtx,
	}, nil
}

// Stats summarizes a run. Published is jobs emitted; Unverifiable is the subset
// stored as unverifiable because their rating did not map; Skipped counts records
// dropped for missing fields or a restrictive licence, plus pages skipped by robots.
type Stats struct {
	Published    int
	Unverifiable int
	Skipped      int
}

// Run walks every outlet: it loads the outlet's robots.txt, discovers article URLs
// from its sitemap, and fetches each allowed page at the paced rate, extracting and
// publishing its ClaimReview records. A per-page fetch or parse error is logged and
// skipped so one bad page never aborts the outlet; a robots or sitemap failure logs
// and moves to the next outlet. A nil logger falls back to slog.Default.
func (c *Client) Run(ctx context.Context, logger *slog.Logger, pub Publisher) (Stats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var stats Stats
	for _, outlet := range c.outlets {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if err := c.runOutlet(ctx, logger, pub, outlet, &stats); err != nil {
			if ctx.Err() != nil {
				return stats, ctx.Err()
			}
			logger.WarnContext(ctx, "skipping outlet after error", slog.String("outlet", outlet.Host), slog.Any("err", err))
		}
	}
	logger.InfoContext(ctx, "claimreview outlet ingest finished",
		slog.Int("published", stats.Published),
		slog.Int("unverifiable", stats.Unverifiable),
		slog.Int("skipped", stats.Skipped))
	return stats, nil
}

func (c *Client) runOutlet(ctx context.Context, logger *slog.Logger, pub Publisher, outlet Outlet, stats *Stats) error {
	rules := c.loadRobots(ctx, logger, outlet)
	delay := c.minDelay
	if rules.crawlDelay > delay {
		delay = rules.crawlDelay
	}

	urls, err := c.discoverURLs(ctx, outlet)
	if err != nil {
		return fmt.Errorf("discover urls: %w", err)
	}

	first := true
	for _, pageURL := range urls {
		if err := ctx.Err(); err != nil {
			return err
		}
		u, err := url.Parse(pageURL)
		if err != nil || !strings.EqualFold(u.Host, outlet.Host) {
			continue
		}
		if !rules.allowed(u.Path) {
			stats.Skipped++
			continue
		}
		// Pace between fetches (not before the first) so an outlet is never hammered.
		if !first {
			if err := c.sleep(ctx, delay); err != nil {
				return err
			}
		}
		first = false

		reviews, err := c.fetchReviews(ctx, pageURL)
		if err != nil {
			logger.WarnContext(ctx, "skipping page after fetch error", slog.String("url", pageURL), slog.Any("err", err))
			continue
		}
		for _, cr := range reviews {
			c.publishReview(ctx, logger, pub, outlet, cr, stats)
		}
	}
	return nil
}

// publishReview normalises one ClaimReview into a job and publishes it, updating
// stats. It drops a record missing claim text or a review URL, or carrying a
// restrictive sdLicense.
func (c *Client) publishReview(ctx context.Context, logger *slog.Logger, pub Publisher, outlet Outlet, cr claimReview, stats *Stats) {
	text := strings.TrimSpace(cr.ClaimReviewed)
	if text == "" || strings.TrimSpace(cr.URL) == "" {
		stats.Skipped++
		return
	}
	if claimnorm.LicenseRestricted(cr.SdLicense.url, c.restrictive) {
		logger.InfoContext(ctx, "skipping record under a restrictive sdLicense",
			slog.String("url", cr.URL), slog.String("sd_license", cr.SdLicense.url))
		stats.Skipped++
		return
	}
	// The canonical review URL is the cross-path dedup key.
	reviewURL := claimnorm.CanonicalURL(cr.URL)
	verdict, mapped := claimrating.Normalize(cr.ReviewRating.AlternateName, claimrating.NumericRating{
		Value: cr.ReviewRating.RatingValue.val, ValueSet: cr.ReviewRating.RatingValue.set,
		Best: cr.ReviewRating.BestRating.val, BestSet: cr.ReviewRating.BestRating.set,
		Worst: cr.ReviewRating.WorstRating.val, WorstSet: cr.ReviewRating.WorstRating.set,
	})
	job := factcheckjob.ClaimJob{
		ID:             reviewURL,
		Text:           text,
		LiteralVerdict: string(verdict),
		SourceName:     outletSourceName(outlet, cr),
		SourceURL:      reviewURL,
		QuotedSpan:     text,
		Outlet:         outlet.Host,
		CheckedAt:      normalizeDate(cr.DatePublished),
	}
	body, err := marshalJob(job)
	if err != nil {
		logger.ErrorContext(ctx, "encode claim job", slog.String("url", reviewURL), slog.Any("err", err))
		stats.Skipped++
		return
	}
	if err := pub.Publish(ctx, body, c.maxPriority); err != nil {
		logger.ErrorContext(ctx, "publish claim job", slog.String("url", reviewURL), slog.Any("err", err))
		stats.Skipped++
		return
	}
	stats.Published++
	if !mapped {
		stats.Unverifiable++
	}
}

// outletSourceName prefers the configured outlet name, falling back to the record's
// author/publisher name and finally the host, so source_name (NOT NULL) is set.
func outletSourceName(outlet Outlet, cr claimReview) string {
	if outlet.Name != "" {
		return outlet.Name
	}
	if cr.Author.Name != "" {
		return cr.Author.Name
	}
	if cr.Publisher.Name != "" {
		return cr.Publisher.Name
	}
	return outlet.Host
}

// discoverURLs fetches the outlet's sitemap and, when it is a sitemap index,
// follows its child sitemaps (bounded) to collect page URLs up to the per-outlet
// cap.
func (c *Client) discoverURLs(ctx context.Context, outlet Outlet) ([]string, error) {
	pages, children, err := c.fetchSitemap(ctx, outlet.Sitemap)
	if err != nil {
		return nil, err
	}
	urls := pages
	for i, child := range children {
		if i >= defaultMaxSitemaps || len(urls) >= c.maxURLs {
			break
		}
		childPages, _, err := c.fetchSitemap(ctx, child)
		if err != nil {
			continue
		}
		urls = append(urls, childPages...)
	}
	if len(urls) > c.maxURLs {
		urls = urls[:c.maxURLs]
	}
	return urls, nil
}

func (c *Client) fetchSitemap(ctx context.Context, sitemapURL string) ([]string, []string, error) {
	body, err := c.get(ctx, sitemapURL)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = body.Close() }()
	return parseSitemap(body)
}

func (c *Client) fetchReviews(ctx context.Context, pageURL string) ([]claimReview, error) {
	body, err := c.get(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	// Bound the page read so a giant page cannot exhaust memory; the JSON-LD is in
	// the head/body markup and never approaches this cap for a fact-check article.
	return extractClaimReviews(io.LimitReader(body, 8<<20)), nil
}

// loadRobots fetches and parses the outlet's robots.txt from the same scheme its
// sitemap uses. A missing (4xx) or unreachable robots.txt yields an empty allow-all
// policy, but any Disallow or Crawl-delay it declares is honored.
func (c *Client) loadRobots(ctx context.Context, logger *slog.Logger, outlet Outlet) robots {
	scheme := "https"
	if u, err := url.Parse(outlet.Sitemap); err == nil && u.Scheme != "" {
		scheme = u.Scheme
	}
	body, err := c.get(ctx, scheme+"://"+outlet.Host+"/robots.txt")
	if err != nil {
		logger.InfoContext(ctx, "no usable robots.txt, proceeding allow-all", slog.String("host", outlet.Host), slog.Any("err", err))
		return robots{}
	}
	defer func() { _ = body.Close() }()
	return parseRobots(io.LimitReader(body, 1<<20), c.userAgent)
}

// get performs a bounded GET with the bot user-agent and returns the body on a 2xx.
func (c *Client) get(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", rawURL, redactURLError(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("get %s: status %d", rawURL, resp.StatusCode)
	}
	return resp.Body, nil
}

// sleepCtx sleeps for d unless ctx is canceled first, in which case it returns the
// context error so a canceled run stops promptly instead of finishing the wait.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func redactURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
