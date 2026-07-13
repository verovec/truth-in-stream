// Package interieur is the French interior-ministry (Ministère de l'Intérieur)
// open-data adapter: it downloads the residence-permit and asylum CSV resources
// published on the national open-data portal (data.gouv.fr) and maps each row to
// a source-agnostic domain.Datapoint the stats foundation renders, embeds, and
// stores. The resources are open under Licence Ouverte; no API key is needed.
//
// Wire format verified 2026-06-19 against the data.gouv.fr resources
// (stock-et-flux-des-titres-de-sejour-en-france-sur-lannee-2023 and
// demandes-dasile-et-transferts-dublin-en-france-sur-lannee-2024):
//   - Comma-delimited, UTF-8, RFC 4180 CSV with a single header row.
//   - The by-country resources carry region_monde_origine, pays_nationalite,
//     code_iso3 and a count column (nb_titres or nb_demandes).
//   - A suppressed cell is the literal string "n.c" (non-communiqué); such a row
//     is skipped, not a parse error.
//   - The reporting year is implicit in the dataset, not a column, so a Spec
//     carries it.
//
// Standard library only (net/http + encoding/csv): the resources are flat CSV,
// so no third-party dependency is warranted. The stable per-resource permalink
// (https://www.data.gouv.fr/api/1/datasets/r/<uuid>) redirects to the current
// file, so the default http.Client (which follows redirects) resolves it.
package interieur

import (
	"context"
	"encoding/csv"
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
	defaultTimeout = 60 * time.Second
	sourceName     = "Ministère de l'Intérieur"
	// suppressed is the data.gouv.fr sentinel for a cell withheld for
	// statistical disclosure control; a row carrying it has no figure to ingest.
	suppressed = "n.c"
	// total marks an inline subtotal row in the by-reason resources; ingesting
	// it would double-count the breakdown rows, so it is dropped.
	total = "TOTAL"
)

// Config configures a Client. Every field is optional; the zero Config targets
// the public data.gouv.fr resources with sane defaults.
type Config struct {
	// HTTPClient overrides the base HTTP client (timeout, transport); it is wrapped
	// in a retrying doer. It must follow redirects, since the stable resource
	// permalink 302-redirects to the file.
	HTTPClient *http.Client
	// Retry tunes the retry/backoff wrapper; a zero value uses the httpx defaults.
	Retry httpx.RetryConfig
}

// Client downloads and parses interior-ministry open-data CSV resources.
type Client struct {
	httpClient httpx.Doer
}

// New builds a Client from cfg, applying a default HTTP client when unset. The HTTP
// client is wrapped in a retrying doer so a 429/5xx from the open-data portal is
// backed off and retried (honoring Retry-After) rather than failing the run; the
// base client still follows the resource permalink's 302 redirect.
func New(cfg Config) *Client {
	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{httpClient: httpx.NewRetryClient(base, cfg.Retry)}
}

// APIError is a non-2xx response from the open-data portal; callers match it
// with errors.As to distinguish a download failure from a parse failure.
type APIError struct {
	StatusCode int
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("interieur: download status %d for %s: %s", e.StatusCode, e.URL, e.Body)
}

// Fetch downloads spec's CSV resource and returns one datapoint per non-suppressed
// row. A non-200 status, missing column, or unparseable figure fails loudly with
// a wrapped error so schema drift never silently drops the corpus.
func (c *Client) Fetch(ctx context.Context, spec Spec) ([]domain.Datapoint, error) {
	body, err := c.get(ctx, spec.URL)
	if err != nil {
		return nil, err
	}
	return parseCSV(spec, body)
}

// get downloads rawURL and returns the body, mapping a non-2xx status to
// *APIError. The default client follows the resource permalink redirect.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("interieur: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("interieur: do request %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("interieur: read body %s: %w", rawURL, err)
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

// parseCSV maps spec's CSV body to datapoints, resolving columns by header name
// so a column reorder does not silently misread the figure. A missing value or
// key column is schema drift and fails loudly; a row whose value is the "n.c"
// suppression sentinel, or whose distinguishing value is an inline "TOTAL"
// subtotal, is skipped.
func parseCSV(spec Spec, body []byte) ([]domain.Datapoint, error) {
	r := csv.NewReader(strings.NewReader(string(body)))
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("interieur: read csv header for %s: %w", spec.Dataset, err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}

	valueIdx, ok := col[spec.ValueColumn]
	if !ok {
		return nil, fmt.Errorf("interieur: %s csv schema drift: no %s column in %v", spec.Dataset, spec.ValueColumn, header)
	}
	keyIdx, ok := col[spec.KeyColumn]
	if !ok {
		return nil, fmt.Errorf("interieur: %s csv schema drift: no %s column in %v", spec.Dataset, spec.KeyColumn, header)
	}
	dimIdx := make([]int, len(spec.DimensionColumns))
	for i, name := range spec.DimensionColumns {
		idx, ok := col[name]
		if !ok {
			return nil, fmt.Errorf("interieur: %s csv schema drift: no %s column in %v", spec.Dataset, name, header)
		}
		dimIdx[i] = idx
	}

	var out []domain.Datapoint
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("interieur: read csv row for %s: %w", spec.Dataset, err)
		}

		rawValue := strings.TrimSpace(field(rec, valueIdx))
		if rawValue == "" || strings.EqualFold(rawValue, suppressed) {
			continue // suppressed observation
		}
		key := strings.TrimSpace(field(rec, keyIdx))
		if key == "" || strings.EqualFold(key, total) {
			continue // inline subtotal, not a distinct series
		}
		figure, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			return nil, fmt.Errorf("interieur: %s %s value %q: %w", spec.Dataset, spec.ValueColumn, rawValue, err)
		}

		dims := make([]string, 0, len(dimIdx))
		for _, idx := range dimIdx {
			if v := strings.TrimSpace(field(rec, idx)); v != "" {
				dims = append(dims, v)
			}
		}

		out = append(out, domain.Datapoint{
			SourceName: sourceName,
			SourceURL:  spec.URL,
			Dataset:    spec.Dataset,
			SeriesKey:  key,
			Title:      spec.Title,
			Geography:  "France",
			Dimensions: dims,
			Period:     spec.Year,
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
