package stats

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// eurostatBaseURL is the keyless Eurostat dissemination API serving JSON-stat
// 2.0. No registration or token is required.
const eurostatBaseURL = "https://ec.europa.eu/eurostat/api/dissemination/statistics/1.0/data"

// eurostatSourceName is the publisher shown to readers for Eurostat evidence;
// the attribution Eurostat's license requires.
const eurostatSourceName = "Eurostat"

// eurostatClient fetches a dataset filtered to a single series and decodes the
// JSON-stat response. baseURL overrides the endpoint for tests.
type eurostatClient struct {
	httpClient *http.Client
	baseURL    string
}

// eurostatResponse mirrors the JSON-stat 2.0 wire shape. value and status are
// sparse objects keyed by a flattened index; a value pointer distinguishes an
// explicit null from an absent key, both of which mean "no datum".
type eurostatResponse struct {
	Label     string                       `json:"label"`
	Updated   string                       `json:"updated"`
	ID        []string                     `json:"id"`
	Size      []int                        `json:"size"`
	Dimension map[string]eurostatDimension `json:"dimension"`
	Value     map[string]*float64          `json:"value"`
	Extension eurostatExtension            `json:"extension"`
}

type eurostatDimension struct {
	Label    string `json:"label"`
	Category struct {
		Index map[string]int    `json:"index"`
		Label map[string]string `json:"label"`
	} `json:"category"`
}

type eurostatExtension struct {
	DatasetID string `json:"datasetId"`
}

func (c *eurostatClient) endpoint() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return eurostatBaseURL
}

// fetch retrieves the dataset, applying the dimension filters, and decodes the
// observations along the time dimension into a chronological Series.
func (c *eurostatClient) fetch(ctx context.Context, dataset string, filters map[string]string) (Series, error) {
	reqURL := c.endpoint() + "/" + url.PathEscape(dataset)
	q := url.Values{}
	q.Set("format", "JSON")
	q.Set("lang", "FR")
	for k, v := range filters {
		q.Set(k, v)
	}
	reqURL += "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Series{}, fmt.Errorf("building Eurostat request for %s: %w", dataset, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Series{}, fmt.Errorf("fetching Eurostat dataset %s: %w", dataset, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Series{}, fmt.Errorf("fetching Eurostat dataset %s: status %d", dataset, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Series{}, fmt.Errorf("reading Eurostat dataset %s: %w", dataset, err)
	}
	return parseEurostat(body, dataset, reqURL)
}

// parseEurostat decodes JSON-stat into a Series along the time dimension. It
// walks the time categories in index order, computes each one's flat index into
// the sparse value map holding every non-time dimension at its single selected
// position, and records the observation (missing when the key is absent or
// null). It requires the response to have been filtered so every non-time
// dimension has exactly one category.
func parseEurostat(body []byte, dataset, sourceURL string) (Series, error) {
	var resp eurostatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Series{}, fmt.Errorf("decoding Eurostat dataset %s: %w", dataset, err)
	}
	if len(resp.ID) != len(resp.Size) {
		return Series{}, fmt.Errorf("eurostat dataset %s: id/size length mismatch", dataset)
	}

	timePos := -1
	for i, dim := range resp.ID {
		if dim == "time" {
			timePos = i
			continue
		}
		if resp.Size[i] != 1 {
			return Series{}, fmt.Errorf("eurostat dataset %s: dimension %s must be filtered to one value, got %d", dataset, dim, resp.Size[i])
		}
	}
	if timePos < 0 {
		return Series{}, fmt.Errorf("eurostat dataset %s: no time dimension", dataset)
	}

	if err := resp.requireFilteredDimensions(timePos); err != nil {
		return Series{}, fmt.Errorf("eurostat dataset %s: %w", dataset, err)
	}
	timeStride := computeStrides(resp.Size)[timePos]

	timeDim := resp.Dimension["time"]
	periods := sortedByIndex(timeDim.Category.Index)

	series := Series{
		SourceID:    resp.Extension.DatasetID,
		Title:       resp.Label,
		URL:         sourceURL,
		LastUpdated: resp.Updated,
		Obs:         make([]Observation, 0, len(periods)),
	}
	if series.SourceID == "" {
		series.SourceID = dataset
	}
	for _, p := range periods {
		flat := timeDim.Category.Index[p] * timeStride
		obs := Observation{Period: p}
		if v, ok := resp.Value[strconv.Itoa(flat)]; ok && v != nil {
			obs.Value = *v
		} else {
			obs.Missing = true
		}
		series.Obs = append(series.Obs, obs)
	}
	series.sortChronologically()
	return series, nil
}

// requireFilteredDimensions confirms every non-time dimension is filtered to a
// single category. That invariant is what lets the flat index of a time period
// be just timeIndex*timeStride: a single-category dimension's only valid index
// in this response is 0 (JSON-stat indices run 0..size-1 against the response's
// own size, not the unfiltered dataset's), so it adds nothing to the base.
func (r eurostatResponse) requireFilteredDimensions(timePos int) error {
	for i, dim := range r.ID {
		if i == timePos {
			continue
		}
		if err := requireSingleCategory(r.Dimension[dim]); err != nil {
			return fmt.Errorf("dimension %s: %w", dim, err)
		}
	}
	return nil
}

// computeStrides returns the row-major stride for each dimension: the product of
// the sizes of all dimensions to its right.
func computeStrides(size []int) []int {
	strides := make([]int, len(size))
	stride := 1
	for i := len(size) - 1; i >= 0; i-- {
		strides[i] = stride
		stride *= size[i]
	}
	return strides
}

// requireSingleCategory confirms a filtered dimension has exactly one category,
// the invariant that lets baseFlatIndex treat its contribution as 0.
func requireSingleCategory(dim eurostatDimension) error {
	if len(dim.Category.Index) != 1 {
		return fmt.Errorf("want exactly one category, got %d", len(dim.Category.Index))
	}
	return nil
}

// sortedByIndex returns the category codes ordered by their JSON-stat index, so
// time periods are walked in the order the response lays them out.
func sortedByIndex(index map[string]int) []string {
	codes := make([]string, 0, len(index))
	for code := range index {
		codes = append(codes, code)
	}
	slices.SortStableFunc(codes, func(a, b string) int {
		return cmp.Compare(index[a], index[b])
	})
	return codes
}
