package insee

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// realNoDataResponse is the SDMX-ML 2.1 StructureSpecificData wire format of a
// live `GET /data/{DATAFLOW_ID}?detail=nodata` dataflow-expansion query against
// bdm.insee.fr (shape captured 2026-06-19): a DataSet of Series elements, each
// carrying IDBANK, FREQ, TITLE_FR, UNIT_MEASURE and the dataflow's dimension
// attributes, with NO Obs children. A passing fixture means the real anonymous
// expansion call parses too. The third series carries SERIE_ARRETEE="true" so the
// test proves discontinued series are dropped from the live catalog.
const realNoDataResponse = `<?xml version="1.0" encoding="UTF-8"?>
<message:StructureSpecificData xmlns:message="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/message" xmlns:ss="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/data/structurespecific">
  <message:Header>
    <message:ID>IDREF</message:ID>
  </message:Header>
  <message:DataSet ss:structureRef="CHOMAGE-TRIM-NATIONAL">
    <Series IDBANK="001688526" FREQ="T" TITLE_FR="Taux de chômage au sens du BIT - ensemble - 15 ans ou plus - France métropolitaine - données CVS" UNIT_MEASURE="POURCENT" UNIT_MULT="0" REF_AREA="FM" SEXE="0" AGE="00-" CORRECTION="CVS" SERIE_ARRETEE="false"/>
    <Series IDBANK="001688527" FREQ="T" TITLE_FR="Taux de chômage au sens du BIT - femmes - 15 ans ou plus - France métropolitaine - données CVS" UNIT_MEASURE="POURCENT" UNIT_MULT="0" REF_AREA="FM" SEXE="2" AGE="00-" CORRECTION="CVS" SERIE_ARRETEE="false"/>
    <Series IDBANK="001688999" FREQ="T" TITLE_FR="Série arrêtée à ne pas ingérer" UNIT_MEASURE="POURCENT" UNIT_MULT="0" REF_AREA="FM" SEXE="1" AGE="15-24" CORRECTION="CVS" SERIE_ARRETEE="true"/>
  </message:DataSet>
</message:StructureSpecificData>`

func chomageDataflow() DataflowSpec {
	return DataflowSpec{
		ID:        "CHOMAGE-TRIM-NATIONAL",
		Corpus:    domain.INSEEUnemploymentCorpus,
		Theme:     "Chômage au sens du BIT",
		StartYear: "2014",
	}
}

func TestExpandDataflowReadsMemberSeries(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/data/CHOMAGE-TRIM-NATIONAL") {
			t.Errorf("path = %q, want the dataflow data query", r.URL.Path)
		}
		if got := r.URL.Query().Get("detail"); got != "nodata" {
			t.Errorf("detail = %q, want nodata (light expansion)", got)
		}
		_, _ = w.Write([]byte(realNoDataResponse))
	})

	members, err := c.ExpandDataflow(context.Background(), chomageDataflow())
	if err != nil {
		t.Fatalf("ExpandDataflow: %v", err)
	}
	// Two live, non-discontinued series; the SERIE_ARRETEE="true" one is dropped.
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2 (discontinued dropped)", len(members))
	}
	first := members[0]
	if first.IDBank != "001688526" {
		t.Errorf("IDBANK = %q, want 001688526", first.IDBank)
	}
	if first.Frequency != "T" {
		t.Errorf("FREQ = %q, want T", first.Frequency)
	}
	if first.Title == "" || !strings.Contains(first.Title, "chômage") {
		t.Errorf("TITLE_FR = %q, want the French label", first.Title)
	}
	if first.Unit != "%" {
		t.Errorf("unit = %q, want %% (mapped from POURCENT)", first.Unit)
	}
	// REF_AREA="FM" must surface as "France métropolitaine", not the national
	// default, so a métropolitaine-only figure is not mislabeled as the whole
	// country.
	if first.Geography != "France métropolitaine" {
		t.Errorf("geography = %q, want France métropolitaine (from REF_AREA=FM)", first.Geography)
	}
}

// TestExpandDataflowEmptyCatalogErrors proves a dataflow that returns no series
// at all fails loudly rather than reporting a green no-op, so a truncated or
// vanished catalog surfaces instead of silently dropping the theme's corpus.
func TestExpandDataflowEmptyCatalogErrors(t *testing.T) {
	const empty = `<?xml version="1.0" encoding="UTF-8"?>
<message:StructureSpecificData xmlns:message="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/message" xmlns:ss="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/data/structurespecific">
  <message:DataSet ss:structureRef="CHOMAGE-TRIM-NATIONAL"/>
</message:StructureSpecificData>`
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(empty))
	})
	if _, err := c.ExpandDataflow(context.Background(), chomageDataflow()); err == nil {
		t.Fatal("ExpandDataflow accepted an empty catalog")
	}
}

// TestExpandDataflowValidatesIdentifiers proves discovery never invents an
// identifier: a Series with no IDBANK attribute is a malformed catalog entry and
// fails the expansion loudly rather than fabricating a key.
func TestExpandDataflowValidatesIdentifiers(t *testing.T) {
	const missingIDBank = `<?xml version="1.0" encoding="UTF-8"?>
<message:StructureSpecificData xmlns:message="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/message" xmlns:ss="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/data/structurespecific">
  <message:DataSet ss:structureRef="CHOMAGE-TRIM-NATIONAL">
    <Series FREQ="T" TITLE_FR="No idbank" UNIT_MEASURE="POURCENT" SERIE_ARRETEE="false"/>
  </message:DataSet>
</message:StructureSpecificData>`
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(missingIDBank))
	})
	if _, err := c.ExpandDataflow(context.Background(), chomageDataflow()); err == nil {
		t.Fatal("ExpandDataflow accepted a series with no IDBANK")
	}
}

func TestExpandDataflowNon200Wrapped(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte("payload too large"))
	})
	_, err := c.ExpandDataflow(context.Background(), chomageDataflow())
	if err == nil {
		t.Fatal("ExpandDataflow accepted a 413")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
}

// TestDataflowSourceExpandsThenFetches proves the dataflow source discovers its
// member series from the live catalog, fetches each member's observations, and
// stamps every datapoint with the dataflow's theme corpus and the live TITLE_FR -
// no invented identifiers, every figure cited to its IDBANK.
func TestDataflowSourceExpandsThenFetches(t *testing.T) {
	var expansions, dataFetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("detail") == "nodata":
			atomic.AddInt32(&expansions, 1)
			_, _ = w.Write([]byte(realNoDataResponse))
		case strings.Contains(r.URL.Path, "/data/SERIES_BDM/001688526"):
			atomic.AddInt32(&dataFetches, 1)
			_, _ = w.Write([]byte(quarterlyObs("001688526", "7.5", "7.3")))
		case strings.Contains(r.URL.Path, "/data/SERIES_BDM/001688527"):
			atomic.AddInt32(&dataFetches, 1)
			_, _ = w.Write([]byte(quarterlyObs("001688527", "8.1", "7.9")))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client(), MinInterval: time.Millisecond})
	src := NewDataflowSource(c, chomageDataflow())

	if src.Corpus() != domain.INSEEUnemploymentCorpus {
		t.Errorf("Corpus() = %q, want %q", src.Corpus(), domain.INSEEUnemploymentCorpus)
	}

	dps, err := src.Datapoints(context.Background())
	if err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	// Two live members, two quarterly observations each.
	if len(dps) != 4 {
		t.Fatalf("got %d datapoints, want 4", len(dps))
	}
	if atomic.LoadInt32(&expansions) != 1 {
		t.Errorf("expansions = %d, want 1", expansions)
	}
	if atomic.LoadInt32(&dataFetches) != 2 {
		t.Errorf("data fetches = %d, want 2 (one per member)", dataFetches)
	}
	keys := map[string]bool{}
	for _, d := range dps {
		if err := d.Validate(); err != nil {
			t.Errorf("datapoint invalid: %v", err)
		}
		if d.Dataset != "CHOMAGE-TRIM-NATIONAL" {
			t.Errorf("dataset = %q, want the dataflow id", d.Dataset)
		}
		if d.Title == "" {
			t.Errorf("title empty for %s", d.SeriesKey)
		}
		// Geography is carried from discovery's REF_AREA=FM, not hardcoded
		// "France", so the métropolitaine scope is not overstated.
		if d.Geography != "France métropolitaine" {
			t.Errorf("geography = %q, want France métropolitaine", d.Geography)
		}
		if !strings.HasPrefix(d.Period, "2024-Q") {
			t.Errorf("period = %q, want a quarter", d.Period)
		}
		keys[d.SeriesKey] = true
	}
	if !keys["001688526"] || !keys["001688527"] {
		t.Errorf("missing a member series: %v", keys)
	}
}

// TestCuratedDataflowsAreWellFormed proves every curated dataflow names a real
// catalog id (no placeholder), maps to a distinct registered statistical corpus,
// and carries a French theme label - so the sweep never invents an identifier and
// every theme is excluded from the wiki-only maintenance reads.
func TestCuratedDataflowsAreWellFormed(t *testing.T) {
	if len(CuratedDataflows) == 0 {
		t.Fatal("CuratedDataflows is empty")
	}
	seenID := map[string]bool{}
	seenCorpus := map[string]bool{}
	for _, df := range CuratedDataflows {
		if df.ID == "" || strings.ContainsAny(df.ID, " ") {
			t.Errorf("dataflow id %q is malformed", df.ID)
		}
		if seenID[df.ID] {
			t.Errorf("duplicate dataflow id %q", df.ID)
		}
		seenID[df.ID] = true
		if !domain.IsStatCorpus(df.Corpus) {
			t.Errorf("dataflow %s corpus %q is not a registered statistical corpus", df.ID, df.Corpus)
		}
		if seenCorpus[df.Corpus] {
			t.Errorf("dataflow %s reuses corpus %q", df.ID, df.Corpus)
		}
		seenCorpus[df.Corpus] = true
		if df.Theme == "" {
			t.Errorf("dataflow %s has no theme label", df.ID)
		}
	}
}

// quarterlyObs renders a minimal StructureSpecificData data response for one
// IDBANK with two 2024 quarterly observations, mirroring the live BDM shape.
func quarterlyObs(idbank, q1, q2 string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<message:StructureSpecificData xmlns:message="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/message" xmlns:ss="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/data/structurespecific">
  <message:DataSet ss:structureRef="SERIES_BDM">
    <Series IDBANK="` + idbank + `" FREQ="T" UNIT_MEASURE="POURCENT" TITLE_FR="Taux de chômage au sens du BIT">
      <Obs TIME_PERIOD="2024-Q1" OBS_VALUE="` + q1 + `" OBS_STATUS="A"/>
      <Obs TIME_PERIOD="2024-Q2" OBS_VALUE="` + q2 + `" OBS_STATUS="A"/>
    </Series>
  </message:DataSet>
</message:StructureSpecificData>`
}
