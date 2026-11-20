// Package ods is the OpenDataSoft Explore API v2.1 adapter: it reads the curated
// political-relevance datasets published by three French institutional portals
// that share the identical Explore API - DREES (health/social policy), DARES
// (labor market), and URSSAF (private-sector employment by territory) - and maps
// each record to a source-agnostic domain.Datapoint the stats foundation renders,
// embeds, and stores. One client covers the three institutions as configuration
// (a portal is a host plus a dataset allowlist); each writes its own corpus so a
// retrieved passage's publisher is identifiable.
//
// Wire format (Explore API v2.1, verified 2026-07 against the portals):
//   - Records endpoint: GET {base}/api/explore/v2.1/catalog/datasets/{dataset}/records
//     with select / where / order_by / limit / offset query params.
//   - Response envelope: {"total_count": N, "results": [ {field: value, ...} ]} -
//     in v2.1 each result is a FLAT field->value object (no v2.0 record.fields
//     nesting). A field value is a JSON number, string, or null.
//   - Pagination: limit caps at 100 rows per page and offset+limit must stay at or
//     below 10000, so the client pages in 100-row windows up to that ceiling. The
//     curated datasets are aggregated national/regional series that fit inside it.
//   - Public datasets are open under the Etalab Licence Ouverte 2.0; no API key is
//     required (anonymous access is rate-limited, which the retry wrapper backs
//     off on a 429).
//
// Standard library only (net/http + encoding/json): the payload is flat JSON, so
// no third-party dependency is warranted.
package ods

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
	// pageLimit is the Explore API v2.1 per-page row cap; a request for more is
	// rejected, so the client pages in this window.
	pageLimit = 100
	// windowCeiling is the API's hard offset+limit ceiling for the records
	// endpoint: a request whose offset+limit exceeds it is rejected. The curated
	// aggregated datasets fit inside it; the client stops paging at the ceiling so
	// a mistakenly broad dataset fails loudly rather than looping past the window.
	windowCeiling = 10000
)

// Config configures a Client. Every field is optional; the zero Config targets the
// public portals with sane defaults.
type Config struct {
	// HTTPClient overrides the base HTTP client (timeout, transport); it is wrapped
	// in a retrying doer so a 429/5xx from a portal is backed off, not fatal.
	HTTPClient *http.Client
	// Retry tunes the retry/backoff wrapper; a zero value uses the httpx defaults.
	Retry httpx.RetryConfig
}

// Client reads records from OpenDataSoft Explore API v2.1 portals.
type Client struct {
	httpClient httpx.Doer
}

// New builds a Client from cfg, applying a default HTTP client when unset and
// wrapping it in a retrying doer so a rate-limited portal is retried with backoff
// (honoring Retry-After) rather than failing the run.
func New(cfg Config) *Client {
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{httpClient: httpx.NewRetryClient(base, cfg.Retry)}
}

// APIError is a non-2xx response from a portal; callers match it with errors.As to
// distinguish a fetch failure from a parse failure.
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ods: request status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// recordsResponse is the Explore API v2.1 records envelope: a total row count and
// the page of flat field->value records.
type recordsResponse struct {
	TotalCount int              `json:"total_count"`
	Results    []map[string]any `json:"results"`
}

// Fetch reads every record of spec's dataset from the portal's Explore API and
// maps each to a datapoint. It pages the records endpoint in 100-row windows up to
// the API ceiling, ordering by the spec's stable key so paging is deterministic. A
// non-2xx status, a missing configured field, or an unparseable figure fails
// loudly with a wrapped error so schema drift never silently drops the corpus; a
// record whose value field is null or empty is skipped (a suppressed observation),
// not a parse error.
func (c *Client) Fetch(ctx context.Context, portal Portal, spec Spec) ([]domain.Datapoint, error) {
	if err := spec.validate(); err != nil {
		return nil, fmt.Errorf("ods: %s: %w", portal.SourceName, err)
	}
	base := portal.baseURL()
	var out []domain.Datapoint
	for offset := 0; offset < windowCeiling; offset += pageLimit {
		limit := pageLimit
		if offset+limit > windowCeiling {
			limit = windowCeiling - offset
		}
		page, total, reqURL, err := c.fetchPage(ctx, base, spec, offset, limit)
		if err != nil {
			return nil, err
		}
		// A dataset larger than the records-window ceiling cannot be fully paged
		// through this endpoint, so returning the first window would silently drop
		// the tail. Fail loudly instead - the total is known from the first page, so
		// this bails before any wasted paging. Narrow the spec with a Where filter
		// (or split it) to bring it inside the window.
		if total > windowCeiling {
			return nil, fmt.Errorf("ods: dataset %q reports %d rows, exceeding the records-window ceiling of %d; narrow it with a Where filter so it is not silently truncated", spec.Dataset, total, windowCeiling)
		}
		dps, err := mapRecords(portal, spec, page, reqURL)
		if err != nil {
			return nil, err
		}
		out = append(out, dps...)
		if offset+len(page) >= total || len(page) == 0 {
			break
		}
	}
	return out, nil
}

// fetchPage requests one records page and returns the decoded rows, the dataset's
// total row count, and the request URL (for provenance and error context).
func (c *Client) fetchPage(ctx context.Context, base string, spec Spec, offset, limit int) ([]map[string]any, int, string, error) {
	reqURL := recordsURL(base, spec, offset, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, reqURL, fmt.Errorf("ods: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, reqURL, fmt.Errorf("ods: do request %s: %w", reqURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := readSnippet(resp)
		return nil, 0, reqURL, &APIError{StatusCode: resp.StatusCode, URL: reqURL, Body: snippet}
	}
	var decoded recordsResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, 0, reqURL, fmt.Errorf("ods: decode records for %s: %w", spec.Dataset, err)
	}
	return decoded.Results, decoded.TotalCount, reqURL, nil
}

// recordsURL builds the Explore API v2.1 records URL for one page. It requests only
// the fields the spec maps (a narrow select), applies the optional where filter,
// and orders by the key field so a page window is stable across requests.
func recordsURL(base string, spec Spec, offset, limit int) string {
	q := url.Values{}
	q.Set("select", spec.selectClause())
	if spec.Where != "" {
		q.Set("where", spec.Where)
	}
	q.Set("order_by", spec.KeyField)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	return fmt.Sprintf("%s/api/explore/v2.1/catalog/datasets/%s/records?%s",
		base, url.PathEscape(spec.Dataset), q.Encode())
}

// datasetURL is the human-resolvable dataset page on the portal, stored as the
// passage's citation so a figure round-trips to its source.
func datasetURL(base, dataset string) string {
	return fmt.Sprintf("%s/explore/dataset/%s/", base, dataset)
}

// mapRecords maps one page of records to datapoints, resolving every value by field
// name so a field reorder cannot silently misread the figure. A record missing a
// configured field is schema drift and fails loudly; a record whose value is null
// or blank is skipped.
func mapRecords(portal Portal, spec Spec, records []map[string]any, citeURL string) ([]domain.Datapoint, error) {
	source := datasetURL(portal.baseURL(), spec.Dataset)
	out := make([]domain.Datapoint, 0, len(records))
	for _, rec := range records {
		rawValue, ok := rec[spec.ValueField]
		if !ok {
			return nil, fmt.Errorf("ods: %s schema drift: no %q field in %s", spec.Dataset, spec.ValueField, citeURL)
		}
		figure, ok, err := toFloat(rawValue)
		if err != nil {
			return nil, fmt.Errorf("ods: %s field %q value %v: %w", spec.Dataset, spec.ValueField, rawValue, err)
		}
		if !ok {
			continue // suppressed / null observation
		}

		key, err := requireString(rec, spec.KeyField, spec.Dataset, citeURL)
		if err != nil {
			return nil, err
		}
		if key == "" {
			continue
		}

		period := spec.Year
		if spec.PeriodField != "" {
			period, err = requireString(rec, spec.PeriodField, spec.Dataset, citeURL)
			if err != nil {
				return nil, err
			}
			// A quarterly dataset composes "YYYY-Qn" from the year and quarter
			// fields, so the four quarters of a year occupy distinct provenance rows
			// rather than colliding on the annual period.
			if spec.QuarterField != "" {
				quarter, err := requireString(rec, spec.QuarterField, spec.Dataset, citeURL)
				if err != nil {
					return nil, err
				}
				if quarter != "" {
					period = period + "-Q" + quarter
				}
			}
		}

		geography := spec.Geography
		if spec.GeographyField != "" {
			geography, err = requireString(rec, spec.GeographyField, spec.Dataset, citeURL)
			if err != nil {
				return nil, err
			}
		}

		dims := make([]string, 0, len(spec.DimensionFields))
		seriesKey := strings.Builder{}
		seriesKey.WriteString(key)
		for _, f := range spec.DimensionFields {
			v, err := requireString(rec, f, spec.Dataset, citeURL)
			if err != nil {
				return nil, err
			}
			if v != "" {
				dims = append(dims, v)
			}
			// Fold every dimension into the series key so two rows that share the
			// key field but differ on a breakdown occupy distinct provenance rows.
			seriesKey.WriteByte('\x1f')
			seriesKey.WriteString(v)
		}

		out = append(out, domain.Datapoint{
			SourceName: portal.SourceName,
			SourceURL:  source,
			Dataset:    spec.Dataset,
			SeriesKey:  seriesKey.String(),
			Title:      spec.Title,
			Geography:  geography,
			Dimensions: dims,
			Period:     period,
			Figure:     figure,
			Unit:       spec.Unit,
		})
	}
	return out, nil
}

// requireString resolves a field to a formatted string, failing loudly when the
// field is absent (schema drift). A null value yields the empty string.
func requireString(rec map[string]any, field, dataset, citeURL string) (string, error) {
	v, ok := rec[field]
	if !ok {
		return "", fmt.Errorf("ods: %s schema drift: no %q field in %s", dataset, field, citeURL)
	}
	return toString(v), nil
}

// toFloat coerces a decoded JSON value to a float. It returns ok=false for a null
// or blank value (a suppressed observation the caller skips) and an error for a
// value that is neither a number nor a numeric string. The decoder does not use
// json.Number, so a JSON number always arrives as a float64.
func toFloat(v any) (float64, bool, error) {
	switch t := v.(type) {
	case nil:
		return 0, false, nil
	case float64:
		return t, true, nil
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false, err
		}
		return f, true, nil
	default:
		return 0, false, fmt.Errorf("unexpected type %T", v)
	}
}

// toString formats a JSON value as a stable string, rendering an integral number
// without a trailing ".0" so an integer code (a year, an INSEE geo code) reads as
// itself rather than "2023.0".
func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// readSnippet reads a bounded prefix of an error response body for context,
// truncating an over-long body rather than reading it whole.
func readSnippet(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return string(body)
}
