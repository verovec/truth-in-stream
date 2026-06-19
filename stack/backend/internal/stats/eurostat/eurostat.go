// Package eurostat is the EU statistical office (Eurostat) SDMX 2.1 adapter: it
// fetches the open dissemination REST API, parses SDMX-CSV, and maps each
// observation to a source-agnostic domain.Datapoint the stats foundation
// renders, embeds, and stores. The API needs no key.
//
// API verified 2026-06-19 against the Eurostat user guides (data-query,
// asynchronous-api, introduction):
//   - Data query: GET {base}/data/{DATASET}/{key}?format=SDMX-CSV&startPeriod=&endPeriod=
//     {key} is a dot-notation series key in DSD dimension order; an empty
//     segment is a wildcard. No auth.
//   - SDMX-CSV columns: DATAFLOW, LAST UPDATE, <one column per DSD dimension>,
//     TIME_PERIOD, OBS_VALUE, OBS_FLAG, CONF_STATUS. Parse by header name, not
//     position, because the dimension columns differ per dataset.
//   - A request over the ~500k-cell synchronous limit returns a 200 whose body
//     is a bare extraction UUID; poll {asyncBase}/async/status/{uuid} until
//     AVAILABLE, then fetch {asyncBase}/async/data/{uuid}.
//
// Standard library only (net/http + encoding/csv): SDMX-CSV is a flat RFC 4180
// CSV, so no third-party SDMX dependency is warranted.
package eurostat

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
)

const (
	defaultBaseURL      = "https://ec.europa.eu/eurostat/api/dissemination/sdmx/2.1"
	defaultAsyncBaseURL = "https://ec.europa.eu/eurostat/api/dissemination/1.0"
	defaultTimeout      = 60 * time.Second
	defaultPollInterval = 3 * time.Second
	maxAsyncPolls       = 40

	sourceName = "Eurostat"
)

// Config configures a Client. Every field is optional; the zero Config targets
// the public Eurostat API with sane defaults.
type Config struct {
	// BaseURL overrides the SDMX 2.1 dissemination base (without trailing
	// slash). Used by tests to point at a local server.
	BaseURL string
	// AsyncBaseURL overrides the asynchronous extraction base. Defaults to the
	// same host as BaseURL when BaseURL is set (so a test server serves both),
	// else the public async base.
	AsyncBaseURL string
	// HTTPClient overrides the HTTP client (timeout, transport).
	HTTPClient *http.Client
}

// Client fetches and parses Eurostat SDMX data.
type Client struct {
	httpClient   *http.Client
	baseURL      string
	asyncBaseURL string
	pollInterval time.Duration
}

// New builds a Client from cfg, applying defaults for the unset fields.
func New(cfg Config) *Client {
	c := &Client{
		httpClient:   cfg.HTTPClient,
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		asyncBaseURL: strings.TrimRight(cfg.AsyncBaseURL, "/"),
		pollInterval: defaultPollInterval,
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.asyncBaseURL == "" {
		if cfg.BaseURL != "" {
			// A test server serves the async endpoints on the same host.
			c.asyncBaseURL = strings.TrimRight(cfg.BaseURL, "/")
		} else {
			c.asyncBaseURL = defaultAsyncBaseURL
		}
	}
	return c
}

// APIError is a non-2xx response from the Eurostat API; callers match it with
// errors.As to distinguish a provider failure from a parse or store failure.
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("eurostat: api status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// Fetch retrieves spec's series, transparently following the asynchronous
// extraction path when the synchronous limit is exceeded, and returns one
// datapoint per non-suppressed observation. Non-200 responses and schema drift
// fail loudly with wrapped errors.
func (c *Client) Fetch(ctx context.Context, spec Spec) ([]domain.Datapoint, error) {
	body, err := c.get(ctx, c.queryURL(spec))
	if err != nil {
		return nil, err
	}

	if uuid, ok := asyncUUID(body); ok {
		body, err = c.awaitAsync(ctx, uuid)
		if err != nil {
			return nil, err
		}
	}

	return c.parseCSV(spec, body)
}

// queryURL builds the synchronous SDMX-CSV data query for spec.
func (c *Client) queryURL(spec Spec) string {
	q := url.Values{}
	q.Set("format", "SDMX-CSV")
	q.Set("startPeriod", spec.StartPeriod)
	q.Set("endPeriod", spec.EndPeriod)
	return fmt.Sprintf("%s/data/%s/%s?%s", c.baseURL, spec.Dataset, spec.Key, q.Encode())
}

// get issues a GET and returns the body, mapping a non-2xx status to *APIError.
// It requests an identity (uncompressed) encoding because Eurostat otherwise
// returns a gzip body without the Content-Encoding header the Go transport
// needs to decompress it transparently; a gzip body that slips through anyway
// is decompressed defensively by its magic bytes.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("eurostat: build request: %w", err)
	}
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eurostat: do request %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("eurostat: read body %s: %w", rawURL, err)
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
		return nil, fmt.Errorf("eurostat: decompress %s: %w", rawURL, err)
	}
	return body, nil
}

// gzipMagic is the two-byte prefix of every gzip stream (RFC 1952).
var gzipMagic = []byte{0x1f, 0x8b}

// decompressGzip returns the gunzipped body when it begins with the gzip magic,
// otherwise the body unchanged. Eurostat sometimes returns a gzip body without
// a Content-Encoding header, which the transport then cannot transparently
// decode, so the adapter detects it by content.
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

// awaitAsync polls the extraction status until AVAILABLE, then fetches the
// produced data. A terminal non-AVAILABLE status (ERROR, EXPIRED,
// UNKNOWN_REQUEST) fails loudly.
func (c *Client) awaitAsync(ctx context.Context, uuid string) ([]byte, error) {
	statusURL := fmt.Sprintf("%s/async/status/%s", c.asyncBaseURL, uuid)
	for poll := 0; poll < maxAsyncPolls; poll++ {
		body, err := c.get(ctx, statusURL)
		if err != nil {
			return nil, err
		}
		switch status := strings.TrimSpace(string(body)); status {
		case "AVAILABLE":
			return c.get(ctx, fmt.Sprintf("%s/async/data/%s", c.asyncBaseURL, uuid))
		case "SUBMITTED", "PROCESSING":
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("eurostat: async %s: %w", uuid, ctx.Err())
			case <-time.After(c.pollInterval):
			}
		default:
			return nil, fmt.Errorf("eurostat: async extraction %s status %q", uuid, status)
		}
	}
	return nil, fmt.Errorf("eurostat: async extraction %s not ready after %d polls", uuid, maxAsyncPolls)
}

// asyncUUID reports whether body is a bare extraction UUID (the async trigger)
// rather than CSV. SDMX-CSV always starts with the "DATAFLOW" header and
// contains commas and newlines; a UUID has neither.
func asyncUUID(body []byte) (string, bool) {
	s := strings.TrimSpace(string(body))
	if len(s) != 36 || strings.ContainsAny(s, ",\n") {
		return "", false
	}
	// 8-4-4-4-12 hex with dashes.
	if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return "", false
	}
	for _, r := range s {
		if r == '-' {
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return "", false
		}
	}
	return s, true
}

// parseCSV maps an SDMX-CSV body to datapoints using spec to label dimensions
// in French. Columns are resolved by header name. A missing OBS_VALUE or
// TIME_PERIOD column is schema drift and fails loudly; a row with an empty
// OBS_VALUE is a suppressed observation and is skipped.
func (c *Client) parseCSV(spec Spec, body []byte) ([]domain.Datapoint, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("eurostat: read csv header for %s: %w", spec.Dataset, err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	valueIdx, ok := col["OBS_VALUE"]
	if !ok {
		return nil, fmt.Errorf("eurostat: %s csv schema drift: no OBS_VALUE column in %v", spec.Dataset, header)
	}
	periodIdx, ok := col["TIME_PERIOD"]
	if !ok {
		return nil, fmt.Errorf("eurostat: %s csv schema drift: no TIME_PERIOD column in %v", spec.Dataset, header)
	}
	geoIdx, hasGeo := col["geo"]

	queryURL := c.queryURL(spec)
	var out []domain.Datapoint
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("eurostat: read csv row for %s: %w", spec.Dataset, err)
		}

		rawValue := strings.TrimSpace(field(rec, valueIdx))
		if rawValue == "" {
			continue // suppressed observation
		}
		figure, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			return nil, fmt.Errorf("eurostat: %s OBS_VALUE %q: %w", spec.Dataset, rawValue, err)
		}
		period := strings.TrimSpace(field(rec, periodIdx))

		geography := spec.GeographyLabel
		if hasGeo {
			if label, ok := geoLabels[field(rec, geoIdx)]; ok {
				geography = label
			}
		}

		out = append(out, domain.Datapoint{
			SourceName: sourceName,
			SourceURL:  queryURL,
			Dataset:    spec.Dataset,
			SeriesKey:  spec.Key,
			Title:      spec.Title,
			Geography:  geography,
			Dimensions: spec.Dimensions,
			Period:     period,
			Figure:     figure,
			Unit:       spec.Unit,
		})
	}
	return out, nil
}

// field returns the i-th record value, or "" when the record is shorter than
// the header (a defensive guard against a ragged row).
func field(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}
