// Package sdmx is a generic SDMX 2.1 REST client for the statistical-office
// dissemination APIs that expose SDMX-CSV: one protocol client, many
// institutions. It builds an endpoint's data-query URL, fetches SDMX-CSV, and
// maps each observation to a source-agnostic domain.Datapoint the stats
// foundation (internal/stats) renders, embeds, and stores - the same
// bulk-into-live path the Eurostat and INSEE adapters use, so a new institution
// is endpoint configuration plus a curated series list, never a new pipeline.
//
// # Why SDMX-CSV
//
// SDMX-CSV is a flat RFC 4180 table whose columns differ per dataset and per
// institution, but which invariantly carries a TIME_PERIOD and an OBS_VALUE
// column. Resolving those two by header name (never by position) means one parser
// serves every SDMX-CSV endpoint; the series-distinguishing dimension labels are
// supplied by the curated Spec in French, so the parser needs no per-dataset DSD
// knowledge. This mirrors internal/stats/eurostat, generalised to an arbitrary
// base URL, format token, optional gateway auth header, and per-endpoint rate
// limit.
//
// # Endpoints verified July 2026
//
//   - ECB Data Portal: base https://data-api.ecb.europa.eu/service, data query
//     GET /data/{flowRef}/{key}?startPeriod=&endPeriod=&format=csvdata. Anonymous.
//   - OECD SDMX API: base https://sdmx.oecd.org/public/rest, data query
//     GET /data/{flowRef}/{key}?startPeriod=&endPeriod=&format=csvfilewithlabels,
//     where {flowRef} is the "{agency},{dataflow},{version}" triple. Anonymous,
//     documented 60 requests/hour, so the client throttles between requests.
//
// Both are keyless; the optional ClientIDHeader/ClientID plumbing exists for a
// gateway-fronted endpoint (e.g. Banque de France Webstat behind IBM API
// Connect) whose value, when configured, is read from the environment only and
// never logged. Standard library only (net/http + encoding/csv): SDMX-CSV is flat
// CSV, so no third-party SDMX dependency is warranted.
package sdmx

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

const (
	defaultTimeout = 60 * time.Second
	// defaultFormat is the SDMX-CSV format token used when an endpoint sets none.
	defaultFormat = "csvdata"
)

// Endpoint declares one institution's SDMX-CSV dissemination API. Every field but
// BaseURL and SourceName has a sane default, so a keyless anonymous endpoint is a
// two-field declaration.
type Endpoint struct {
	// SourceName is the human-readable publisher stamped on every passage's
	// citation, e.g. "Banque centrale européenne (BCE)".
	SourceName string
	// BaseURL is the SDMX 2.1 data-resource root without a trailing slash - the
	// part before "/data" (e.g. "https://data-api.ecb.europa.eu/service"). A test
	// points it at a local server.
	BaseURL string
	// Format is the value of the format query parameter selecting SDMX-CSV
	// (e.g. "csvdata" for ECB, "csvfilewithlabels" for OECD). Empty uses
	// defaultFormat.
	Format string
	// ExtraQuery are static query parameters appended to every request (optional).
	ExtraQuery url.Values
	// ClientIDHeader and ClientID authenticate a gateway-fronted endpoint. When
	// both are set the header is sent on every request; ClientID is read from the
	// environment only and never logged. Empty means an anonymous client.
	ClientIDHeader string
	ClientID       string
	// MinInterval is the minimum spacing between requests, enforcing a documented
	// rate limit (e.g. OECD's 60/hour). Zero means no client-side throttle.
	MinInterval time.Duration
	// Timeout bounds each HTTP request; zero uses defaultTimeout.
	Timeout time.Duration
	// Retry tunes the retry/backoff wrapper; a zero value uses the httpx defaults.
	Retry httpx.RetryConfig
}

// Spec is one curated series to ingest from an endpoint: the dataflow reference
// and dimension key that select it, a period window, and the French labels the
// rendered passage carries. A spec maps a single, fully-resolved series so the
// query stays small and the rendered sentence is unambiguous.
type Spec struct {
	// FlowRef is the {flowRef} path segment identifying the dataflow. For ECB it
	// is a bare dataflow id ("ICP"); for OECD it is the "{agency},{dataflow},
	// {version}" triple ("OECD.SDD.TPS,DSD_LFS@DF_IALFS_UNE_M,1.0").
	FlowRef string
	// Key is the dot-notation dimension key selecting the series within the
	// dataflow; an empty segment is a wildcard.
	Key string
	// StartPeriod and EndPeriod bound the query (inclusive), e.g. "2015".."2025".
	StartPeriod string
	EndPeriod   string
	// Dataset is the stable dataset code recorded as the passage's provenance
	// (domain.Datapoint.Dataset). Empty falls back to FlowRef.
	Dataset string
	// Title is the French series label used as the passage title.
	Title string
	// GeographyLabel is the French geography woven into the passage.
	GeographyLabel string
	// Dimensions are the French breakdown labels the rendered sentence weaves in,
	// in a stable order.
	Dimensions []string
	// Unit is the French unit label, e.g. "%" or "points".
	Unit string
}

// Client fetches and parses one endpoint's SDMX-CSV data. It is not safe for
// concurrent use: throttle mutates lastRequest without synchronization, and a
// Source fetches its specs sequentially precisely so successive requests are
// spaced by the rate limit.
type Client struct {
	httpClient httpx.Doer
	endpoint   Endpoint
	baseURL    string
	format     string
	timeout    time.Duration

	minInterval time.Duration
	lastRequest time.Time
}

// New builds a Client for endpoint, applying defaults for the unset fields. The
// HTTP client is wrapped in a retrying doer so a 429/5xx is backed off and
// retried (honoring Retry-After) rather than failing the run.
func New(endpoint Endpoint) *Client {
	timeout := endpoint.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	format := endpoint.Format
	if format == "" {
		format = defaultFormat
	}
	return &Client{
		httpClient:  httpx.NewRetryClient(&http.Client{Timeout: timeout}, endpoint.Retry),
		endpoint:    endpoint,
		baseURL:     strings.TrimRight(endpoint.BaseURL, "/"),
		format:      format,
		timeout:     timeout,
		minInterval: endpoint.MinInterval,
	}
}

// SourceName is the endpoint's human-readable publisher, exposed so a Source can
// stamp it on every citation.
func (c *Client) SourceName() string { return c.endpoint.SourceName }

// APIError is a non-2xx response from an SDMX endpoint; callers match it with
// errors.As to distinguish a provider failure from a parse failure.
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sdmx: api status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// Fetch retrieves spec's series as SDMX-CSV and returns one datapoint per
// non-suppressed observation. Non-200 responses and schema drift fail loudly with
// wrapped errors.
func (c *Client) Fetch(ctx context.Context, spec Spec) ([]domain.Datapoint, error) {
	rawURL := c.queryURL(spec)
	body, err := c.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return c.parseCSV(spec, rawURL, body)
}

// queryURL builds the SDMX-CSV data query for spec. The {flowRef}/{key} data path
// is uniform across SDMX 2.1 REST endpoints; only the base, format token, and any
// static extra parameters differ per institution.
func (c *Client) queryURL(spec Spec) string {
	q := url.Values{}
	for k, vs := range c.endpoint.ExtraQuery {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	q.Set("format", c.format)
	if spec.StartPeriod != "" {
		q.Set("startPeriod", spec.StartPeriod)
	}
	if spec.EndPeriod != "" {
		q.Set("endPeriod", spec.EndPeriod)
	}
	// The flow ref and key are path segments; escape each so an OECD flow ref's
	// commas/@ and a wildcard key round-trip intact.
	path := fmt.Sprintf("%s/data/%s/%s", c.baseURL, pathEscape(spec.FlowRef), pathEscape(spec.Key))
	return path + "?" + q.Encode()
}

// pathEscape percent-escapes a path segment while preserving the SDMX key and
// flow-ref punctuation (dots, commas, @, +, colons) the servers expect verbatim,
// so only truly unsafe characters (spaces, control bytes) are encoded.
func pathEscape(seg string) string {
	esc := url.PathEscape(seg)
	// url.PathEscape leaves "+" as-is but escapes nothing SDMX needs; it does
	// escape "@" to "%40" in some Go versions of QueryEscape but PathEscape keeps
	// it. Restore the few sub-delims SDMX flow refs rely on if a version escaped
	// them, so the request path matches the documented shape.
	replacer := strings.NewReplacer("%40", "@", "%2C", ",", "%2B", "+", "%3A", ":")
	return replacer.Replace(esc)
}

// get issues a GET and returns the body, mapping a non-2xx status to *APIError.
// It requests an identity encoding and defensively gunzips a gzip body that slips
// through without a Content-Encoding header (as some dissemination endpoints
// return). An optional gateway client-id header authenticates the request.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	if err := c.throttle(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("sdmx: build request: %w", err)
	}
	req.Header.Set("Accept-Encoding", "identity")
	if c.endpoint.ClientIDHeader != "" && c.endpoint.ClientID != "" {
		req.Header.Set(c.endpoint.ClientIDHeader, c.endpoint.ClientID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sdmx: do request %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sdmx: read body %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return nil, &APIError{StatusCode: resp.StatusCode, URL: rawURL, Body: snippet}
	}

	body, err = decompressGzip(body)
	if err != nil {
		return nil, fmt.Errorf("sdmx: decompress %s: %w", rawURL, err)
	}
	return body, nil
}

// throttle blocks until at least minInterval has elapsed since the last request,
// honoring context cancellation, so the source respects a documented rate limit
// rather than hammering the API.
func (c *Client) throttle(ctx context.Context) error {
	if c.minInterval <= 0 {
		return nil
	}
	if !c.lastRequest.IsZero() {
		if wait := c.minInterval - time.Since(c.lastRequest); wait > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("sdmx: throttle: %w", ctx.Err())
			case <-time.After(wait):
			}
		}
	}
	c.lastRequest = time.Now()
	return nil
}

// gzipMagic is the two-byte prefix of every gzip stream (RFC 1952).
var gzipMagic = []byte{0x1f, 0x8b}

// decompressGzip returns the gunzipped body when it begins with the gzip magic,
// otherwise the body unchanged, so a gzip response without a Content-Encoding
// header the transport cannot transparently decode is still handled.
func decompressGzip(body []byte) ([]byte, error) {
	if len(body) < 2 || !bytes.Equal(body[:2], gzipMagic) {
		return body, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = r.Close() }()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gzip read: %w", err)
	}
	return out, nil
}

// parseCSV maps an SDMX-CSV body to datapoints, resolving the TIME_PERIOD and
// OBS_VALUE columns by header name (never position, since dimension columns differ
// per dataset and institution). A missing OBS_VALUE or TIME_PERIOD column is
// schema drift and fails loudly; a row with an empty or "NaN" OBS_VALUE is a
// suppressed observation and is skipped.
func (c *Client) parseCSV(spec Spec, queryURL string, body []byte) ([]domain.Datapoint, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1 // tolerate ragged rows; fields are resolved by name
	header, err := r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // an empty body is a legitimately empty series
		}
		return nil, fmt.Errorf("sdmx: read csv header for %s/%s: %w", spec.FlowRef, spec.Key, err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	valueIdx, ok := col["OBS_VALUE"]
	if !ok {
		return nil, fmt.Errorf("sdmx: %s/%s csv schema drift: no OBS_VALUE column in %v", spec.FlowRef, spec.Key, header)
	}
	periodIdx, ok := col["TIME_PERIOD"]
	if !ok {
		return nil, fmt.Errorf("sdmx: %s/%s csv schema drift: no TIME_PERIOD column in %v", spec.FlowRef, spec.Key, header)
	}

	dataset := spec.Dataset
	if dataset == "" {
		dataset = spec.FlowRef
	}

	var out []domain.Datapoint
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("sdmx: read csv row for %s/%s: %w", spec.FlowRef, spec.Key, err)
		}

		rawValue := strings.TrimSpace(field(rec, valueIdx))
		if rawValue == "" || strings.EqualFold(rawValue, "NaN") {
			continue // suppressed observation
		}
		figure, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			return nil, fmt.Errorf("sdmx: %s/%s OBS_VALUE %q: %w", spec.FlowRef, spec.Key, rawValue, err)
		}

		out = append(out, domain.Datapoint{
			SourceName: c.endpoint.SourceName,
			SourceURL:  queryURL,
			Dataset:    dataset,
			SeriesKey:  spec.Key,
			Title:      spec.Title,
			Geography:  spec.GeographyLabel,
			Dimensions: spec.Dimensions,
			Period:     strings.TrimSpace(field(rec, periodIdx)),
			Figure:     figure,
			Unit:       spec.Unit,
		})
	}
	return out, nil
}

// field returns the i-th record value, or "" when the record is shorter than the
// header (a defensive guard against a ragged row).
func field(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}
