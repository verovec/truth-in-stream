package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MediaWiki Action API limits and etiquette (verified 2026-06 against
// mediawiki.org/wiki/API:RecentChanges, API:Extracts and the Wikimedia
// User-Agent policy):
//   - rcPageLimit: RecentChanges returns at most 500 entries per page for
//     anonymous clients; the rest follow via the continuation token.
//   - extractsBatchMax: TextExtracts caps exlimit at 20 even with exintro, so a
//     batch of titles larger than 20 would leave the tail without extracts;
//     titles are fetched 20 at a time so every page gets its lead in one shot.
//   - maxlagSeconds: ask the API to shed our load (HTTP 503 + Retry-After) when
//     replication lag exceeds this, rather than execute under load.
const (
	rcPageLimit      = 500
	extractsBatchMax = 20
	maxlagSeconds    = 5
)

// Transient-throttling backoff bounds. The API answers an overloaded read with
// HTTP 429 or a maxlag 503 carrying a Retry-After; absent that header we wait a
// base delay, and never longer than the cap.
const (
	apiMaxRetries   = 5
	apiRetryBase    = 1 * time.Second
	apiMaxRetryWait = 60 * time.Second
)

// Change is one main-namespace change RecentChanges reported. Deleted marks a
// hard page deletion - the API reports pageid 0 for these, so a deletion is
// identified by Title - while every other Change is an edit or new page
// carrying its PageID and the new RevisionID.
type Change struct {
	PageID     int64
	Title      string
	RevisionID int64
	Timestamp  time.Time
	Deleted    bool
}

// Extract is the current plain-text lead and revision of one page, fetched by
// title. Missing marks a title the API no longer has a live page for (deleted
// between surfacing in RecentChanges and the refetch).
type Extract struct {
	PageID     int64
	Title      string
	RevisionID int64
	Text       string
	Missing    bool
}

// APIClient calls a Wikipedia MediaWiki Action API endpoint. Corpus selects the
// language project (e.g. simplewiki -> simple.wikipedia.org); HTTPClient and
// BaseURL are optional - BaseURL overrides the derived endpoint for tests.
type APIClient struct {
	Corpus     string
	BaseURL    string
	HTTPClient *http.Client

	// retryBase overrides the throttling backoff base, for tests; zero uses
	// apiRetryBase.
	retryBase time.Duration
}

func (c *APIClient) endpoint() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	lang := strings.TrimSuffix(c.Corpus, "wiki")
	return fmt.Sprintf("https://%s.wikipedia.org/w/api.php", lang)
}

func (c *APIClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// RecentChanges returns every main-namespace edit, new page, and hard deletion
// since the given checkpoint, oldest first, following the continuation token
// across pages. Non-deletion log entries (moves, protections, revision hides)
// are not content changes and are dropped.
func (c *APIClient) RecentChanges(ctx context.Context, since time.Time) ([]Change, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("list", "recentchanges")
	params.Set("rcnamespace", "0")
	params.Set("rclimit", strconv.Itoa(rcPageLimit))
	params.Set("rcprop", "ids|title|timestamp|loginfo")
	params.Set("rctype", "edit|new|log")
	params.Set("rcdir", "newer")
	params.Set("rcstart", since.UTC().Format(time.RFC3339))

	var changes []Change
	for {
		var resp rcResponse
		if err := c.getJSON(ctx, params, &resp); err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("wiki: recentchanges api error %s: %s", resp.Error.Code, resp.Error.Info)
		}
		for _, rc := range resp.Query.RecentChanges {
			if ch, ok := rc.toChange(); ok {
				changes = append(changes, ch)
			}
		}
		if len(resp.Continue) == 0 {
			return changes, nil
		}
		for k, v := range resp.Continue {
			params.Set(k, v)
		}
	}
}

// Extracts fetches the plain-text lead section and current revision id of each
// title, in batches of at most the extracts limit. A title with no live page
// comes back flagged Missing.
func (c *APIClient) Extracts(ctx context.Context, titles []string) ([]Extract, error) {
	out := make([]Extract, 0, len(titles))
	for start := 0; start < len(titles); start += extractsBatchMax {
		end := min(start+extractsBatchMax, len(titles))
		batch, err := c.extractsBatch(ctx, titles[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (c *APIClient) extractsBatch(ctx context.Context, titles []string) ([]Extract, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("prop", "extracts|revisions")
	params.Set("exintro", "1")
	params.Set("explaintext", "1")
	params.Set("exlimit", strconv.Itoa(extractsBatchMax))
	params.Set("rvprop", "ids")
	params.Set("titles", strings.Join(titles, "|"))

	var resp exResponse
	if err := c.getJSON(ctx, params, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("wiki: extracts api error %s: %s", resp.Error.Code, resp.Error.Info)
	}
	out := make([]Extract, 0, len(resp.Query.Pages))
	for _, p := range resp.Query.Pages {
		if p.Missing != nil {
			out = append(out, Extract{Title: p.Title, Missing: true})
			continue
		}
		ex := Extract{PageID: p.PageID, Title: p.Title, Text: p.Extract}
		if len(p.Revisions) > 0 {
			ex.RevisionID = p.Revisions[0].RevID
		}
		out = append(out, ex)
	}
	return out, nil
}

// getJSON issues one API GET, decoding the body into out. format and maxlag are
// always set. A 429 or maxlag 503 is retried up to apiMaxRetries times,
// honoring Retry-After.
func (c *APIClient) getJSON(ctx context.Context, params url.Values, out any) error {
	params.Set("format", "json")
	params.Set("maxlag", strconv.Itoa(maxlagSeconds))
	target := c.endpoint() + "?" + params.Encode()

	for attempt := 0; ; attempt++ {
		body, wait, err := c.fetch(ctx, target)
		if err == nil {
			if jerr := json.Unmarshal(body, out); jerr != nil {
				return fmt.Errorf("wiki: decode api response: %w", jerr)
			}
			return nil
		}
		if wait == 0 {
			return err
		}
		if attempt >= apiMaxRetries {
			return fmt.Errorf("wiki: api gave up after %d retries: %w", apiMaxRetries, err)
		}
		if werr := sleepCtx(ctx, wait); werr != nil {
			return werr
		}
	}
}

// fetch performs one GET. On a throttling response it returns a positive wait
// duration alongside the error, signaling the caller to retry; any other
// non-200 returns a zero wait (not retryable).
func (c *APIClient) fetch(ctx context.Context, target string) ([]byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("wiki: build api request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("wiki: api request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("wiki: read api response: %w", err)
		}
		return body, 0, nil
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return nil, c.retryAfter(resp.Header), fmt.Errorf("wiki: api throttled: %s", resp.Status)
	default:
		return nil, 0, fmt.Errorf("wiki: api status %s", resp.Status)
	}
}

// retryAfter reads the Retry-After header (whole seconds), falling back to the
// backoff base when it is absent or unparseable, and never exceeds the cap.
func (c *APIClient) retryAfter(h http.Header) time.Duration {
	d := c.retryBase
	if d == 0 {
		d = apiRetryBase
	}
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			d = time.Duration(secs) * time.Second
		}
	}
	return min(d, apiMaxRetryWait)
}

// sleepCtx waits for d or until ctx is canceled.
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

// rcResponse mirrors the RecentChanges Action API response.
type rcResponse struct {
	Continue map[string]string `json:"continue"`
	Query    struct {
		RecentChanges []rcEntry `json:"recentchanges"`
	} `json:"query"`
	Error *apiErr `json:"error"`
}

// rcEntry is one RecentChanges row.
type rcEntry struct {
	Type      string `json:"type"`
	PageID    int64  `json:"pageid"`
	RevID     int64  `json:"revid"`
	Title     string `json:"title"`
	Timestamp string `json:"timestamp"`
	LogType   string `json:"logtype"`
	LogAction string `json:"logaction"`
}

// toChange maps a RecentChanges row to a Change, reporting ok=false for rows to
// ignore: unparseable timestamps and log events that are neither a deletion nor
// a restore. A hard deletion removes the page; a restore (undeletion) makes it
// live again and is treated as a change to refetch, so a delete-then-restore in
// one window does not leave the page deleted.
func (e rcEntry) toChange() (Change, bool) {
	ts, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		return Change{}, false
	}
	if e.Type == "log" {
		switch {
		case e.LogType == "delete" && e.LogAction == "delete":
			return Change{Title: e.Title, Timestamp: ts, Deleted: true}, true
		case e.LogType == "delete" && e.LogAction == "restore":
			return Change{PageID: e.PageID, Title: e.Title, RevisionID: e.RevID, Timestamp: ts}, true
		default:
			return Change{}, false
		}
	}
	return Change{PageID: e.PageID, Title: e.Title, RevisionID: e.RevID, Timestamp: ts}, true
}

// exResponse mirrors the extracts+revisions Action API response. Pages is keyed
// by stringified page id (negative for missing pages).
type exResponse struct {
	Query struct {
		Pages map[string]exPage `json:"pages"`
	} `json:"query"`
	Error *apiErr `json:"error"`
}

// exPage is one page in an extracts response. Missing is a non-nil empty string
// when the API has no live page for the requested title.
type exPage struct {
	PageID    int64   `json:"pageid"`
	Title     string  `json:"title"`
	Extract   string  `json:"extract"`
	Missing   *string `json:"missing"`
	Revisions []struct {
		RevID int64 `json:"revid"`
	} `json:"revisions"`
}

// apiErr is the Action API's structured error envelope.
type apiErr struct {
	Code string `json:"code"`
	Info string `json:"info"`
}
