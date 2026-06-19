package insee

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// DataflowSpec is one curated INSEE BDM economic dataflow to sweep: the catalog
// dataflow id (validated against the live API at ingest time), the theme corpus
// label every member passage is stamped with, a human theme label used to derive
// a member title when the live series omits one, and the inclusive start year.
//
// A dataflow expands at ingest time into its live member series; the spec invents
// no IDBANK - it names only the dataflow, and discovery reads the real members
// off the SDMX catalog. Each spec maps to its own corpus so a retrieved macro
// passage's theme is identifiable.
type DataflowSpec struct {
	// ID is the BDM dataflow identifier as it appears in the catalog, e.g.
	// "CHOMAGE-TRIM-NATIONAL". It is the dataset half of the provenance key.
	ID string
	// Corpus is the wiki_chunks.corpus label every member passage is stamped with;
	// it must be one of domain.StatCorpora (an INSEE economic-theme corpus).
	Corpus string
	// Theme is a short French label for the dataflow, used as the passage title
	// fallback when a member series carries no TITLE_FR.
	Theme string
	// StartYear bounds each member query (inclusive) via startPeriod, e.g. "2014";
	// empty fetches the full available history.
	StartYear string
}

// Member is one live series discovered in a dataflow's catalog: its IDBANK, the
// SDMX frequency code, the French label, unit, and geographic scope read off the
// live series. Only members the live API actually returns reach this struct, so
// an ingest never fabricates an identifier.
type Member struct {
	IDBank    string
	Frequency string
	Title     string
	Unit      string
	Geography string
}

// noDataDataSet is the SDMX-ML 2.1 StructureSpecificData envelope of a
// `detail=nodata` dataflow-expansion query: a DataSet of Series elements carrying
// the discovery attributes and no Obs children. Only the attributes discovery
// needs are modeled; unknown attributes are ignored.
type noDataDataSet struct {
	XMLName xml.Name `xml:"StructureSpecificData"`
	Series  []struct {
		IDBank       string `xml:"IDBANK,attr"`
		Freq         string `xml:"FREQ,attr"`
		TitleFR      string `xml:"TITLE_FR,attr"`
		UnitMeasure  string `xml:"UNIT_MEASURE,attr"`
		RefArea      string `xml:"REF_AREA,attr"`
		SerieArretee string `xml:"SERIE_ARRETEE,attr"`
	} `xml:"DataSet>Series"`
}

// ExpandDataflow queries spec's dataflow with detail=nodata and returns its live
// member series, validating every identifier against the response: a series with
// no IDBANK is a malformed catalog entry and fails loudly rather than yielding a
// fabricated key, and a discontinued series (SERIE_ARRETEE="true") is dropped so
// the sweep ingests only live data. Non-2xx responses and malformed XML fail with
// wrapped errors.
func (c *Client) ExpandDataflow(ctx context.Context, spec DataflowSpec) ([]Member, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("insee: dataflow spec has empty id")
	}
	q := url.Values{}
	q.Set("detail", "nodata")
	queryURL := fmt.Sprintf("%s/data/%s?%s", c.baseURL, spec.ID, q.Encode())

	body, err := c.get(ctx, queryURL)
	if err != nil {
		return nil, err
	}

	var doc noDataDataSet
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("insee: parse dataflow %s catalog: %w", spec.ID, err)
	}

	members := make([]Member, 0, len(doc.Series))
	for _, s := range doc.Series {
		idbank := strings.TrimSpace(s.IDBank)
		if idbank == "" {
			return nil, fmt.Errorf("insee: dataflow %s catalog has a series with no IDBANK", spec.ID)
		}
		if strings.EqualFold(strings.TrimSpace(s.SerieArretee), "true") {
			continue // discontinued series: not live data
		}
		members = append(members, Member{
			IDBank:    idbank,
			Frequency: strings.TrimSpace(s.Freq),
			Title:     strings.TrimSpace(s.TitleFR),
			Unit:      unitLabel(s.UnitMeasure),
			Geography: geographyLabel(s.RefArea),
		})
	}
	// A curated dataflow that returns no series at all is an anomaly (a truncated
	// or empty response, or a catalog id that silently vanished), not a legitimate
	// empty result. Fail loudly so a scheduled run surfaces the gap rather than
	// reporting a green no-op while the theme's corpus silently disappears. An
	// all-discontinued dataflow is distinct: it returned series, so it succeeds
	// with zero live members.
	if len(doc.Series) == 0 {
		return nil, fmt.Errorf("insee: dataflow %s catalog returned no series", spec.ID)
	}
	return members, nil
}

// unitLabel maps a BDM UNIT_MEASURE code to the French unit label the rendered
// passage carries, defaulting to a neutral "unité" for an unmapped code so a
// figure is never rendered without a unit (which Validate would reject). "SO"
// ("sans objet") is the BDM code for an index/level with no physical unit, so it
// renders "en indice" rather than fabricating a dimension.
func unitLabel(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "POURCENT", "PCT":
		return "%"
	case "INDIVIDUS":
		return "personnes"
	case "EUROS", "EURO":
		return "euros"
	case "INDICE", "SO":
		return "indice"
	default:
		return "unité"
	}
}

// geographyLabel maps a BDM REF_AREA code to its French geography label so a
// passage cites the exact scope of the figure (a métropolitaine-only series is
// not mislabeled as the whole country). An unmapped or empty code falls back to
// the national default, which never overstates the scope beyond "France".
func geographyLabel(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "FM":
		return "France métropolitaine"
	case "FE":
		return "France entière"
	case "", "FR":
		return defaultGeography
	default:
		return defaultGeography
	}
}

// DataflowSource adapts a Client and one curated DataflowSpec to the stats.Source
// contract: it expands the dataflow into its live member series, fetches every
// member's observations in order (the Client throttles between requests to honor
// the rate limit), and stamps each datapoint with the dataflow's theme corpus.
// Discovery validates every identifier against the live catalog, so the source
// ingests only real series. A fetch failure for any member fails the run
// (wrapped), so a partial corpus is never silently committed.
type DataflowSource struct {
	client *Client
	spec   DataflowSpec
}

// NewDataflowSource builds a DataflowSource over client and spec.
func NewDataflowSource(client *Client, spec DataflowSpec) *DataflowSource {
	return &DataflowSource{client: client, spec: spec}
}

// Corpus is the dataflow's economic-theme corpus label every passage is stamped
// with, distinct per theme so a retrieved macro passage's theme is identifiable.
func (s *DataflowSource) Corpus() string { return s.spec.Corpus }

// Datapoints expands the dataflow and fetches every member series, returning the
// combined datapoints. Each member is fetched under a Spec derived from its live
// IDBANK, title, and unit, so a rendered passage cites the exact series and a
// re-run upserts the same provenance keys.
func (s *DataflowSource) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	members, err := s.client.ExpandDataflow(ctx, s.spec)
	if err != nil {
		return nil, fmt.Errorf("insee: expand dataflow %s: %w", s.spec.ID, err)
	}

	var all []domain.Datapoint
	for _, m := range members {
		title := m.Title
		if title == "" {
			title = s.spec.Theme
		}
		dps, err := s.client.Fetch(ctx, Spec{
			IDBank:    m.IDBank,
			Dataset:   s.spec.ID,
			Title:     title,
			Unit:      m.Unit,
			Geography: m.Geography,
			StartYear: s.spec.StartYear,
		})
		if err != nil {
			return nil, fmt.Errorf("insee: fetch dataflow %s member %s: %w", s.spec.ID, m.IDBank, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
