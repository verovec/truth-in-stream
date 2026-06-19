// Package insee is the French national statistics institute (INSEE) adapter: it
// queries the open BDM (Banque de Données Macroéconomiques) SDMX web service for
// the national labor-market series (ILO unemployment-rate annual averages, by
// age and sex), parses the SDMX-ML 2.1 response, and maps each observation to a
// source-agnostic domain.Datapoint the stats foundation renders, embeds, and
// stores. See spec.go for why the immigrant-status breakdown itself is not a BDM
// series (it ships only as EEC Excel tables, out of scope for an SDMX adapter).
//
// API verified 2026-06-19 against INSEE's SDMX web service documentation:
//   - The BDM SDMX endpoint at https://bdm.insee.fr/series/sdmx is a fully open,
//     anonymous, keyless service (the old OAuth2 api.insee.fr portal was retired
//     in September 2025). Data query:
//     GET {base}/data/SERIES_BDM/{idbank}?startPeriod={year}
//   - The default response is SDMX-ML 2.1 StructureSpecificData: a DataSet of
//     <Series> elements (each carrying IDBANK and dimension attributes) with
//     <Obs> children carrying TIME_PERIOD, OBS_VALUE, OBS_STATUS attributes. A
//     missing value is the string "NaN".
//   - Documented rate limit: 30 requests/minute/IP. The source throttles to at
//     least MinInterval between requests rather than hammering.
//   - An optional API key (for the newer portail-api.insee.fr) is read from the
//     environment only and sent as a Bearer token when present; the open BDM
//     endpoint needs none, so an absent key is a clean anonymous client.
//
// Standard library only (net/http + encoding/xml): the response is flat
// SDMX-ML, so no third-party SDMX dependency is warranted.
package insee

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

const (
	defaultBaseURL = "https://bdm.insee.fr/series/sdmx"
	defaultTimeout = 60 * time.Second
	// defaultMinInterval throttles successive requests to stay under the
	// documented 30 req/min/IP limit (one every 2s leaves comfortable headroom).
	defaultMinInterval = 2 * time.Second

	sourceName = "Insee"
	// apiKeyEnv is the only place an INSEE credential is read from: the
	// environment. The open BDM endpoint needs none, so it is optional.
	apiKeyEnv = "INSEE_API_KEY"
	// missingValue is the SDMX sentinel for an absent observation value.
	missingValue = "NaN"
	// defaultGeography labels a series whose spec sets no geography (the
	// hand-curated national series); the dataflow sweep derives the real scope
	// from each series' REF_AREA so a métropolitaine-only figure is not mislabeled.
	defaultGeography = "France"
)

// Config configures a Client. Every field is optional; the zero Config targets
// the public anonymous BDM endpoint with sane defaults.
type Config struct {
	// BaseURL overrides the SDMX base (without trailing slash). Used by tests.
	BaseURL string
	// APIKey is an optional credential for the authenticated portal, sourced from
	// the environment only (see ConfigFromEnv); when empty the client is
	// anonymous, which the open BDM endpoint accepts.
	APIKey string
	// HTTPClient overrides the HTTP client (timeout, transport).
	HTTPClient *http.Client
	// MinInterval is the minimum spacing between requests, enforcing the rate
	// limit. Zero applies defaultMinInterval.
	MinInterval time.Duration
}

// ConfigFromEnv builds a Config reading the optional API key from the
// environment only, never hard-coded; an unset key yields an anonymous client.
func ConfigFromEnv() Config {
	return Config{APIKey: os.Getenv(apiKeyEnv)}
}

// Client queries and parses the INSEE BDM SDMX web service. It is not safe for
// concurrent use: throttle mutates lastRequest without synchronization, and the
// Source fetches its specs sequentially precisely so successive requests are
// spaced by the rate limit.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	apiKey      string
	minInterval time.Duration
	lastRequest time.Time
}

// New builds a Client from cfg, applying defaults for the unset fields.
func New(cfg Config) *Client {
	c := &Client{
		httpClient:  cfg.HTTPClient,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:      cfg.APIKey,
		minInterval: cfg.MinInterval,
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	if c.minInterval == 0 {
		c.minInterval = defaultMinInterval
	}
	return c
}

// APIError is a non-2xx response from the INSEE API; callers match it with
// errors.As to distinguish a provider failure from a parse failure.
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("insee: api status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// structureSpecificData is the SDMX-ML 2.1 StructureSpecificData envelope: a
// DataSet of Series, each with Obs children. Only the fields the adapter maps
// are modeled; unknown attributes are ignored.
type structureSpecificData struct {
	XMLName xml.Name `xml:"StructureSpecificData"`
	Series  []struct {
		IDBank string `xml:"IDBANK,attr"`
		Obs    []struct {
			TimePeriod string `xml:"TIME_PERIOD,attr"`
			ObsValue   string `xml:"OBS_VALUE,attr"`
		} `xml:"Obs"`
	} `xml:"DataSet>Series"`
}

// Fetch queries spec's IDBANK series and returns one datapoint per numeric
// observation, throttled to the rate limit. Only the requested IDBANK's series
// is mapped. Non-200 responses and malformed XML fail loudly with wrapped errors.
func (c *Client) Fetch(ctx context.Context, spec Spec) ([]domain.Datapoint, error) {
	queryURL := c.queryURL(spec)
	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}

	var doc structureSpecificData
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("insee: parse sdmx for %s: %w", spec.IDBank, err)
	}

	geography := spec.Geography
	if geography == "" {
		geography = defaultGeography
	}

	var out []domain.Datapoint
	for _, series := range doc.Series {
		if series.IDBank != spec.IDBank {
			continue
		}
		for _, obs := range series.Obs {
			raw := strings.TrimSpace(obs.ObsValue)
			if raw == "" || raw == missingValue {
				continue // suppressed observation
			}
			figure, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("insee: %s OBS_VALUE %q: %w", spec.IDBank, raw, err)
			}
			out = append(out, domain.Datapoint{
				SourceName: sourceName,
				SourceURL:  queryURL,
				Dataset:    spec.Dataset,
				SeriesKey:  spec.IDBank,
				Title:      spec.Title,
				Geography:  geography,
				Dimensions: spec.Dimensions,
				Period:     strings.TrimSpace(obs.TimePeriod),
				Figure:     figure,
				Unit:       spec.Unit,
			})
		}
	}
	return out, nil
}

// queryURL builds the SDMX data query for spec's IDBANK series.
func (c *Client) queryURL(spec Spec) string {
	q := url.Values{}
	if spec.StartYear != "" {
		q.Set("startPeriod", spec.StartYear)
	}
	u := fmt.Sprintf("%s/data/SERIES_BDM/%s", c.baseURL, spec.IDBank)
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// get throttles to the rate limit, issues a GET, and returns the body, mapping a
// non-2xx status to *APIError. An optional API key is sent as a Bearer token.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	if err := c.throttle(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("insee: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.sdmx.structurespecificdata+xml;version=2.1")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("insee: do request %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("insee: read body %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return nil, &APIError{StatusCode: resp.StatusCode, URL: rawURL, Body: snippet}
	}
	return body, nil
}

// throttle blocks until at least minInterval has elapsed since the last request,
// honoring context cancellation, so the source respects the documented rate
// limit rather than hammering the API.
func (c *Client) throttle(ctx context.Context) error {
	if !c.lastRequest.IsZero() {
		if wait := c.minInterval - time.Since(c.lastRequest); wait > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("insee: throttle: %w", ctx.Err())
			case <-time.After(wait):
			}
		}
	}
	c.lastRequest = time.Now()
	return nil
}
