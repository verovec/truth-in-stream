// Package ssmsi is the French interior-ministry security-statistics adapter: it
// reads the SSMSI (Service statistique ministériel de la sécurité intérieure)
// recorded-delinquency bases published as CSV on the national open-data portal
// (data.gouv.fr) and maps each row to a source-agnostic domain.Datapoint the stats
// foundation renders, embeds, and stores. It rides alongside the OpenDataSoft
// connector because the SSMSI series extend the interior-ministry statistics
// already ingested (internal/stats/interieur) and share the rendering conventions.
//
// The published resource files are timestamped and rotate on each refresh, so the
// fetcher resolves the current CSV through the data.gouv.fr dataset API (a stable
// dataset slug) rather than a hard-coded file URL: it lists the dataset's
// resources, selects the CSV whose title matches the requested territorial level,
// then downloads and parses it. The base is verified against the expected header
// columns, so a schema change fails loudly rather than silently dropping the corpus.
//
// Wire format (departmental/regional bases, verified 2026-07 on data.gouv.fr):
//   - Semicolon-delimited, double-quoted UTF-8 CSV with a single header row.
//   - Columns: a geography code (Code_departement or Code_region), annee,
//     indicateur, unite_de_compte, nombre, taux_pour_mille, and INSEE population
//     columns. The figure is the recorded count (nombre); a decimal comma is
//     normalised to a dot so a rate-style value still parses.
//   - A blank or suppressed count is skipped, not a parse error.
//
// The resources are open under the Etalab Licence Ouverte 2.0; no key is needed.
// Standard library only (net/http + encoding/csv + encoding/json).
package ssmsi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

const (
	defaultTimeout = 120 * time.Second
	sourceName     = "SSMSI"
	// dataGouvBase is the national open-data portal API host; overridable for tests.
	dataGouvBase = "https://www.data.gouv.fr"

	// The SSMSI base schema. The geography column varies by territorial level
	// (Code_departement / Code_region); the rest are shared across the bases.
	colYear      = "annee"
	colIndicator = "indicateur"
	colUnit      = "unite_de_compte"
	colCount     = "nombre"
	// figureUnit is the French unit rendered after the recorded count.
	figureUnit = "faits enregistrés"
)

// Config configures a Client. Every field is optional; the zero Config targets the
// public data.gouv.fr resources with sane defaults.
type Config struct {
	// HTTPClient overrides the base HTTP client (timeout, transport); it is wrapped
	// in a retrying doer. It must follow redirects, since a resource URL may 302 to
	// the static file.
	HTTPClient *http.Client
	// Retry tunes the retry/backoff wrapper; a zero value uses the httpx defaults.
	Retry httpx.RetryConfig
	// BaseURL overrides the data.gouv.fr API host for tests; empty uses the public
	// portal.
	BaseURL string
}

// Client resolves and downloads SSMSI delinquency CSV bases from data.gouv.fr.
type Client struct {
	httpClient httpx.Doer
	baseURL    string
}

// New builds a Client from cfg, applying a default HTTP client when unset and
// wrapping it in a retrying doer so a 429/5xx from the portal is backed off.
func New(cfg Config) *Client {
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: defaultTimeout}
	}
	host := cfg.BaseURL
	if host == "" {
		host = dataGouvBase
	}
	return &Client{httpClient: httpx.NewRetryClient(base, cfg.Retry), baseURL: strings.TrimRight(host, "/")}
}

// APIError is a non-2xx response from the portal; callers match it with errors.As
// to distinguish a fetch failure from a parse failure.
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ssmsi: request status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// datasetResponse is the subset of the data.gouv.fr dataset API envelope the
// fetcher reads: the resource list, each with a title, format, and download URL.
type datasetResponse struct {
	Resources []resource `json:"resources"`
}

type resource struct {
	Title  string `json:"title"`
	Format string `json:"format"`
	URL    string `json:"url"`
}

// Fetch resolves spec's CSV resource through the dataset API and maps each row of
// the downloaded base to a datapoint. A non-2xx status, a missing expected column,
// or an unparseable count fails loudly with a wrapped error; a row with a blank
// count is skipped.
func (c *Client) Fetch(ctx context.Context, spec Spec) ([]domain.Datapoint, error) {
	if err := spec.validate(); err != nil {
		return nil, fmt.Errorf("ssmsi: %w", err)
	}
	resourceURL, err := c.resolveResource(ctx, spec)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, resourceURL)
	if err != nil {
		return nil, err
	}
	return parseCSV(spec, resourceURL, body)
}

// resolveResource lists the dataset's resources and returns the download URL of the
// CSV whose title matches the spec's territorial level, so a rotated file name is
// followed without hard-coding it. It fails loudly when no matching CSV is present.
func (c *Client) resolveResource(ctx context.Context, spec Spec) (string, error) {
	apiURL := fmt.Sprintf("%s/api/1/datasets/%s/", c.baseURL, spec.DatasetSlug)
	body, err := c.get(ctx, apiURL)
	if err != nil {
		return "", err
	}
	var decoded datasetResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("ssmsi: decode dataset %s: %w", spec.DatasetSlug, err)
	}
	for _, res := range decoded.Resources {
		if !strings.EqualFold(strings.TrimSpace(res.Format), "csv") {
			continue
		}
		if !strings.Contains(strings.ToLower(res.Title), strings.ToLower(spec.ResourceMatch)) {
			continue
		}
		if res.URL == "" {
			continue
		}
		return res.URL, nil
	}
	return "", fmt.Errorf("ssmsi: dataset %s has no csv resource matching %q", spec.DatasetSlug, spec.ResourceMatch)
}

// get downloads rawURL and returns the body, mapping a non-2xx status to *APIError.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ssmsi: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ssmsi: do request %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ssmsi: read body %s: %w", rawURL, err)
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

// parseCSV maps the semicolon-delimited base to datapoints, resolving columns by
// header name so a column reorder cannot silently misread the count. A row whose
// count is blank is skipped; a missing expected column or a non-numeric count is
// schema drift and fails loudly. The recorded indicator is the passage title, the
// unit of count is a breakdown dimension, and the geography+indicator+unit form the
// series key so each series occupies a distinct provenance row.
func parseCSV(spec Spec, citeURL string, body []byte) ([]domain.Datapoint, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	r.Comma = ';'
	r.FieldsPerRecord = -1 // tolerate trailing empties across the population columns

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("ssmsi: read csv header for %s: %w", spec.Dataset, err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		// Strip a leading UTF-8 BOM the portal may prepend to the first header.
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}

	required := []string{spec.GeographyColumn, colYear, colIndicator, colUnit, colCount}
	idx := make(map[string]int, len(required))
	for _, name := range required {
		i, ok := col[name]
		if !ok {
			return nil, fmt.Errorf("ssmsi: %s csv schema drift: no %q column in %v", spec.Dataset, name, header)
		}
		idx[name] = i
	}

	var out []domain.Datapoint
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ssmsi: read csv row for %s: %w", spec.Dataset, err)
		}

		rawCount := strings.TrimSpace(field(rec, idx[colCount]))
		if rawCount == "" {
			continue // suppressed / absent observation
		}
		figure, err := parseFrenchFloat(rawCount)
		if err != nil {
			return nil, fmt.Errorf("ssmsi: %s %s value %q: %w", spec.Dataset, colCount, rawCount, err)
		}

		geoCode := strings.TrimSpace(field(rec, idx[spec.GeographyColumn]))
		indicator := strings.TrimSpace(field(rec, idx[colIndicator]))
		unit := strings.TrimSpace(field(rec, idx[colUnit]))
		year := strings.TrimSpace(field(rec, idx[colYear]))
		if geoCode == "" || indicator == "" || year == "" {
			continue
		}

		dims := make([]string, 0, 1)
		if unit != "" {
			dims = append(dims, unit)
		}

		out = append(out, domain.Datapoint{
			SourceName: sourceName,
			SourceURL:  citeURL,
			Dataset:    spec.Dataset,
			SeriesKey:  strings.Join([]string{geoCode, indicator, unit}, "\x1f"),
			Title:      indicator,
			Geography:  spec.GeographyLabel + " " + geoCode,
			Dimensions: dims,
			Period:     year,
			Figure:     figure,
			Unit:       figureUnit,
		})
	}
	return out, nil
}

// parseFrenchFloat parses a count, tolerating a French decimal comma so a
// rate-style value ("0,0078318") still parses; a plain integer count ("5") parses
// unchanged.
func parseFrenchFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
}

// field returns the i-th record value, or "" when the record is shorter than the
// header (a defensive guard against a ragged row).
func field(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}
