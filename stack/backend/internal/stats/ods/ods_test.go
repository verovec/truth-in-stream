package ods

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// realDREESPage is the exact Explore API v2.1 records envelope captured 2026-07
// from data.drees.solidarites-sante.gouv.fr (dataset cns_financement): a top-level
// total_count plus a results array of FLAT field->value objects. A poste has one
// row per financeur (fin_lib), so the first two rows share poste_code and differ
// only on fin_lib; the third row's montants is null (a suppressed observation).
const realDREESPage = `{
  "total_count": 3,
  "results": [
    {"annee": "2010", "poste_niveau": 3, "poste_code": "p12100_niv3", "poste_lib": "---DMSUS", "fin_niveau": 0, "fin_code": "fin000_niv0", "fin_lib": "Tout financeur", "montants": 738.87},
    {"annee": "2010", "poste_niveau": 3, "poste_code": "p12100_niv3", "poste_lib": "---DMSUS", "fin_niveau": 1, "fin_code": "fin100_niv1", "fin_lib": "-Sécurité Sociale", "montants": 737.19},
    {"annee": "2010", "poste_niveau": 1, "poste_code": "p10000_niv1", "poste_lib": "x", "fin_code": "fin000_niv0", "fin_lib": "Tout financeur", "montants": null}
  ]
}`

// realDARESPage is the captured records envelope from
// data.dares.travail-emploi.gouv.fr (dataset dares_tempspartiel_detail_annuelles):
// the period is a bare-year "date", the figure is "valeur", and the series is
// distinguished by indicateur/indicateur_detaille/sexe.
const realDARESPage = `{
  "total_count": 2,
  "results": [
    {"date": "2014", "champ": "France", "indicateur": "Taux de temps partiel (%)", "indicateur_detaille": "Moins d'un mi-temps", "sexe": "Total", "valeur": 23.6},
    {"date": "2015", "champ": "France", "indicateur": "Taux de temps partiel (%)", "indicateur_detaille": "Moins d'un mi-temps", "sexe": "Femmes", "valeur": 30.1}
  ]
}`

// realURSSAFPage is the captured records envelope from open.urssaf.fr (dataset
// effectifs-salaries-et-masse-salariale-du-secteur-prive-par-zone-demploi): the
// series is quarterly, with annee and an integer trimestre that compose the period.
const realURSSAFPage = `{
  "total_count": 2,
  "results": [
    {"libelle_zone_d_emploi": "2804 Caen", "zone_d_emploi": "Caen", "annee": "2022", "trimestre": 1, "code_zone_d_emploi": "2804", "effectifs_salaries_cvs": 132010, "effectifs_salaries_brut": 130330},
    {"libelle_zone_d_emploi": "2804 Caen", "zone_d_emploi": "Caen", "annee": "2022", "trimestre": 2, "code_zone_d_emploi": "2804", "effectifs_salaries_cvs": 133000, "effectifs_salaries_brut": 131000}
  ]
}`

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{HTTPClient: srv.Client()}), srv.URL
}

// portalAt returns a copy of p pointed at the test server base.
func portalAt(p Portal, base string) Portal {
	p.BaseURL = base
	return p
}

func serveJSON(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func TestFetchParsesDREESRecords(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/explore/v2.1/catalog/datasets/cns_financement/records") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != strconv.Itoa(pageLimit) {
			t.Errorf("limit = %q, want %d", got, pageLimit)
		}
		if got := r.URL.Query().Get("select"); !strings.Contains(got, "fin_lib") {
			t.Errorf("select %q should carry the fin_lib dimension", got)
		}
		serveJSON(realDREESPage)(w, r)
	})

	dps, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Two rows carry a figure; the null-montants row is a suppressed observation.
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2 (null skipped)", len(dps))
	}
	first := dps[0]
	if first.SourceName != "DREES" {
		t.Errorf("source name = %q, want DREES", first.SourceName)
	}
	if first.Dataset != "cns_financement" || first.Period != "2010" || first.Figure != 738.87 {
		t.Errorf("first = %+v; want cns_financement/2010/738.87", first)
	}
	if first.Unit != "millions d'euros" || first.Geography != "France" {
		t.Errorf("unit/geo = %q/%q", first.Unit, first.Geography)
	}
	if len(first.Dimensions) != 1 || first.Dimensions[0] != "Tout financeur" {
		t.Errorf("dimensions = %v, want [Tout financeur]", first.Dimensions)
	}
	if !strings.Contains(first.SourceURL, "/explore/dataset/cns_financement/") {
		t.Errorf("source url = %q, want the dataset page", first.SourceURL)
	}
	if err := first.Validate(); err != nil {
		t.Errorf("datapoint invalid: %v", err)
	}
	// The two financiers of the same poste+year must not collide on one row.
	if dps[0].SeriesPageID() == dps[1].SeriesPageID() {
		t.Error("two financiers of the same poste share a page id, would collide on one row")
	}
}

func TestFetchParsesDARESRecords(t *testing.T) {
	c, base := newTestClient(t, serveJSON(realDARESPage))
	dps, err := c.Fetch(context.Background(), portalAt(DARES, base), DARES.Specs[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
	d := dps[0]
	if d.SourceName != "DARES" || d.Dataset != "dares_tempspartiel_detail_annuelles" {
		t.Errorf("provenance = %s/%s", d.SourceName, d.Dataset)
	}
	if d.Period != "2014" || d.Figure != 23.6 || d.Unit != "%" || d.Geography != "France" {
		t.Errorf("d = %+v; want period 2014, figure 23.6, unit %%, geo France", d)
	}
	// indicateur/indicateur_detaille/sexe are woven in as the breakdown.
	if got := strings.Join(d.Dimensions, "|"); got != "Taux de temps partiel (%)|Moins d'un mi-temps|Total" {
		t.Errorf("dimensions = %q", got)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("datapoint invalid: %v", err)
	}
	// Two rows differing only on sexe must not collide.
	if dps[0].SeriesPageID() == dps[1].SeriesPageID() {
		t.Error("rows differing on sexe share a page id")
	}
}

func TestFetchComposesQuarterlyURSSAFPeriod(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("where"); got != `annee >= "2022"` {
			t.Errorf("where = %q, want the recent-years narrowing filter", got)
		}
		if got := r.URL.Query().Get("select"); !strings.Contains(got, "trimestre") {
			t.Errorf("select %q should carry the trimestre field", got)
		}
		serveJSON(realURSSAFPage)(w, r)
	})
	dps, err := c.Fetch(context.Background(), portalAt(URSSAF, base), URSSAF.Specs[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
	// annee + integer trimestre compose a domain-parseable "YYYY-Qn" period.
	if dps[0].Period != "2022-Q1" || dps[1].Period != "2022-Q2" {
		t.Fatalf("periods = %q, %q; want 2022-Q1, 2022-Q2", dps[0].Period, dps[1].Period)
	}
	if dps[0].Figure != 132010 || dps[0].Unit != "salariés" || dps[0].Geography != "Caen" {
		t.Errorf("d = %+v; want figure 132010, unit salariés, geo Caen", dps[0])
	}
	for _, d := range dps {
		if err := d.Validate(); err != nil {
			t.Errorf("datapoint %+v invalid: %v", d, err)
		}
	}
	// The two quarters are the same series (shared page id) distinguished by the
	// period's chunk index; without quarter composition both would map to period
	// "2022" and the same chunk index, colliding on one provenance row.
	ci0, err0 := dps[0].PeriodChunkIndex()
	ci1, err1 := dps[1].PeriodChunkIndex()
	if err0 != nil || err1 != nil {
		t.Fatalf("chunk index errors: %v, %v", err0, err1)
	}
	if ci0 == ci1 {
		t.Errorf("two quarters share chunk index %d, would collide on one row", ci0)
	}
}

// TestFetchErrorsWhenDatasetExceedsWindow guards the loud-failure contract: a
// dataset larger than the records-window ceiling must fail with a named error
// rather than silently returning only the first window of rows.
func TestFetchErrorsWhenDatasetExceedsWindow(t *testing.T) {
	body := `{"total_count":15000,"results":[{"annee":"2010","poste_code":"p1","fin_lib":"Tout financeur","montants":1.0}]}`
	c, base := newTestClient(t, serveJSON(body))
	_, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch silently truncated a dataset larger than the records window")
	}
	if !strings.Contains(err.Error(), "15000") || !strings.Contains(err.Error(), "cns_financement") {
		t.Errorf("error %v should name the dataset and the row count", err)
	}
}

func TestFetchPaginates(t *testing.T) {
	// total_count 150 forces a second page; the client must request offset 100 and
	// stop once it has drained total.
	page1 := recordsJSON(t, 150, 100, 2010)
	page2 := recordsJSON(t, 150, 50, 3000)
	var offsets []string
	c, base := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		w.Header().Set("Content-Type", "application/json")
		if offset == "0" {
			_, _ = w.Write([]byte(page1))
			return
		}
		_, _ = w.Write([]byte(page2))
	})

	dps, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 150 {
		t.Fatalf("got %d datapoints across pages, want 150", len(dps))
	}
	if len(offsets) != 2 || offsets[0] != "0" || offsets[1] != "100" {
		t.Fatalf("offsets = %v, want [0 100]", offsets)
	}
}

func TestFetchSchemaDriftMissingValueField(t *testing.T) {
	body := `{"total_count":1,"results":[{"annee":"2010","poste_code":"x","fin_lib":"Tout financeur"}]}`
	c, base := newTestClient(t, serveJSON(body))
	_, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch accepted a record missing the value field")
	}
	if !strings.Contains(err.Error(), "montants") {
		t.Errorf("error %v should name the missing value field", err)
	}
}

func TestFetchSchemaDriftMissingDimensionField(t *testing.T) {
	// A record present but missing a configured dimension (fin_lib) is drift.
	body := `{"total_count":1,"results":[{"annee":"2010","poste_code":"x","montants":1.0}]}`
	c, base := newTestClient(t, serveJSON(body))
	_, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch accepted a record missing the fin_lib dimension")
	}
	if !strings.Contains(err.Error(), "fin_lib") {
		t.Errorf("error %v should name the missing dimension field", err)
	}
}

func TestFetchNon200Wrapped(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	})
	_, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch accepted a 429")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", apiErr.StatusCode)
	}
}

func TestFetchBadValueWrapped(t *testing.T) {
	body := `{"total_count":1,"results":[{"annee":"2010","poste_code":"x","fin_lib":"Tout financeur","montants":"not-a-number"}]}`
	c, base := newTestClient(t, serveJSON(body))
	_, err := c.Fetch(context.Background(), portalAt(DREES, base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch accepted a non-numeric value")
	}
}

func TestSourceCorpus(t *testing.T) {
	for _, tc := range []struct {
		portal Portal
		want   string
	}{
		{DREES, domain.DREESStatCorpus},
		{DARES, domain.DARESStatCorpus},
		{URSSAF, domain.URSSAFStatCorpus},
	} {
		s := NewSource(New(Config{}), tc.portal)
		if s.Corpus() != tc.want {
			t.Errorf("Corpus() = %q, want %q", s.Corpus(), tc.want)
		}
		if !domain.IsStatCorpus(s.Corpus()) {
			t.Errorf("corpus %q is not a registered statistical corpus", s.Corpus())
		}
	}
}

// recordsJSON builds a records envelope with n results starting at a base amount,
// using the DREES field shape (including the fin_lib dimension), for the
// pagination test.
func recordsJSON(t *testing.T, total, n, baseAmount int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"total_count":`)
	b.WriteString(strconv.Itoa(total))
	b.WriteString(`,"results":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"annee":"2010","poste_code":"p`)
		b.WriteString(strconv.Itoa(baseAmount + i))
		b.WriteString(`","fin_lib":"Tout financeur","montants":`)
		b.WriteString(strconv.Itoa(baseAmount + i))
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	return b.String()
}
