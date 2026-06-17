package stats

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// inseeBaseURL is the keyless INSEE BDM SDMX web service. Tier A
// (bdm.insee.fr) needs no token and serves SDMX-ML 2.1; it is the right default
// for live retrieval. The authenticated api.insee.fr tier (OAuth2) is a later
// throughput upgrade, not a correctness need.
const inseeBaseURL = "https://bdm.insee.fr/series/sdmx/data/SERIES_BDM"

// inseeSourceName is the publisher shown to readers for INSEE evidence.
const inseeSourceName = "INSEE"

// inseeClient fetches a BDM series by IDBANK over SDMX-ML. baseURL overrides the
// endpoint for tests; lastN bounds how many recent observations to request so a
// series stays a window of adjacent periods, not its full history.
type inseeClient struct {
	httpClient *http.Client
	baseURL    string
	lastN      int
}

// inseeMessage mirrors the SDMX-ML StructureSpecificData wire shape. The XML is
// namespaced; encoding/xml matches on local element/attribute names, so the tags
// carry only the local names.
type inseeMessage struct {
	XMLName xml.Name      `xml:"StructureSpecificData"`
	Series  []inseeSeries `xml:"DataSet>Series"`
}

type inseeSeries struct {
	IDBANK      string     `xml:"IDBANK,attr"`
	TitleFR     string     `xml:"TITLE_FR,attr"`
	UnitMeasure string     `xml:"UNIT_MEASURE,attr"`
	LastUpdate  string     `xml:"LAST_UPDATE,attr"`
	Obs         []inseeObs `xml:"Obs"`
}

type inseeObs struct {
	TimePeriod string `xml:"TIME_PERIOD,attr"`
	Value      string `xml:"OBS_VALUE,attr"`
}

func (c *inseeClient) endpoint() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return inseeBaseURL
}

// fetch retrieves the series for idbank and decodes it into a Series. A missing
// or non-200 response, or an empty/garbled body, is an error wrapped with the
// idbank so the caller can report which series failed.
func (c *inseeClient) fetch(ctx context.Context, idbank string) (Series, error) {
	reqURL := c.endpoint() + "/" + url.PathEscape(idbank)
	if c.lastN > 0 {
		reqURL += "?lastNObservations=" + strconv.Itoa(c.lastN)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Series{}, fmt.Errorf("building INSEE request for %s: %w", idbank, err)
	}
	req.Header.Set("Accept", "application/xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Series{}, fmt.Errorf("fetching INSEE series %s: %w", idbank, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Series{}, fmt.Errorf("fetching INSEE series %s: status %d", idbank, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Series{}, fmt.Errorf("reading INSEE series %s: %w", idbank, err)
	}
	return parseINSEE(body, idbank, reqURL)
}

// parseINSEE decodes an SDMX-ML body into a chronological Series. An "NaN" value
// is kept as a missing observation, not dropped, so a gap stays visible to the
// verifier.
func parseINSEE(body []byte, idbank, sourceURL string) (Series, error) {
	var msg inseeMessage
	if err := xml.Unmarshal(body, &msg); err != nil {
		return Series{}, fmt.Errorf("decoding INSEE series %s: %w", idbank, err)
	}
	if len(msg.Series) == 0 {
		return Series{}, fmt.Errorf("INSEE series %s: no series in response", idbank)
	}
	raw := msg.Series[0]
	series := Series{
		SourceID:    raw.IDBANK,
		Title:       raw.TitleFR,
		Unit:        raw.UnitMeasure,
		URL:         sourceURL,
		LastUpdated: raw.LastUpdate,
		Obs:         make([]Observation, 0, len(raw.Obs)),
	}
	if series.SourceID == "" {
		series.SourceID = idbank
	}
	for _, o := range raw.Obs {
		obs := Observation{Period: o.TimePeriod}
		if strings.EqualFold(o.Value, "NaN") || o.Value == "" {
			obs.Missing = true
		} else {
			v, err := strconv.ParseFloat(o.Value, 64)
			if err != nil {
				return Series{}, fmt.Errorf("INSEE series %s period %s value %q: %w", idbank, o.TimePeriod, o.Value, err)
			}
			obs.Value = v
		}
		series.Obs = append(series.Obs, obs)
	}
	series.sortChronologically()
	return series, nil
}
