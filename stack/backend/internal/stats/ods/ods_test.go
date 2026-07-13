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
// from data.drees.solidarites-sante.gouv.fr (dataset cns_financement): a
// top-level total_count plus a results array of FLAT field->value objects, values
// mixing string years, integer levels, and float amounts. A passing fixture means
// the real records response parses too.
const realDREESPage = `{
  "total_count": 3,
  "results": [
    {"annee": "2010", "poste_niveau": 3, "poste_code": "p12100_niv3", "montants": 738.87},
    {"annee": "2011", "poste_niveau": 3, "poste_code": "p12100_niv3", "montants": 802.5},
    {"annee": "2010", "poste_niveau": 1, "poste_code": "p10000_niv1", "montants": null}
  ]
}`

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{HTTPClient: srv.Client()}), srv.URL
}

func dreesPortal(base string) Portal {
	p := DREES
	p.BaseURL = base
	return p
}

func TestFetchParsesDREESRecords(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/explore/v2.1/catalog/datasets/cns_financement/records") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != strconv.Itoa(pageLimit) {
			t.Errorf("limit = %q, want %d", got, pageLimit)
		}
		if got := r.URL.Query().Get("select"); got == "" {
			t.Error("select clause missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(realDREESPage))
	})

	dps, err := c.Fetch(context.Background(), dreesPortal(base), DREES.Specs[0])
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
	if first.Dataset != "cns_financement" {
		t.Errorf("dataset = %q", first.Dataset)
	}
	if first.Period != "2010" {
		t.Errorf("period = %q, want 2010", first.Period)
	}
	if first.Figure != 738.87 {
		t.Errorf("figure = %v, want 738.87", first.Figure)
	}
	if first.Unit != "millions d'euros" {
		t.Errorf("unit = %q", first.Unit)
	}
	if first.Geography != "France" {
		t.Errorf("geography = %q, want France", first.Geography)
	}
	if !strings.Contains(first.SourceURL, "/explore/dataset/cns_financement/") {
		t.Errorf("source url = %q, want the dataset page", first.SourceURL)
	}
	if err := first.Validate(); err != nil {
		t.Errorf("datapoint invalid: %v", err)
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

	dps, err := c.Fetch(context.Background(), dreesPortal(base), DREES.Specs[0])
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
	body := `{"total_count":1,"results":[{"annee":"2010","poste_code":"x"}]}`
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	_, err := c.Fetch(context.Background(), dreesPortal(base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch accepted a record missing the value field")
	}
	if !strings.Contains(err.Error(), "montants") {
		t.Errorf("error %v should name the missing value field", err)
	}
}

func TestFetchNon200Wrapped(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	})
	_, err := c.Fetch(context.Background(), dreesPortal(base), DREES.Specs[0])
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
	body := `{"total_count":1,"results":[{"annee":"2010","poste_code":"x","montants":"not-a-number"}]}`
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	_, err := c.Fetch(context.Background(), dreesPortal(base), DREES.Specs[0])
	if err == nil {
		t.Fatal("Fetch accepted a non-numeric value")
	}
}

func TestDimensionFoldsIntoSeriesKey(t *testing.T) {
	// Two rows share the key field but differ on the dimension: they must map to
	// distinct series keys so they occupy distinct provenance rows.
	body := `{"total_count":2,"results":[
	  {"date":"2020","zone_demploi":"Lyon","grand_secteur_d_activite":"Industrie","effectifs_salaries":1000},
	  {"date":"2020","zone_demploi":"Lyon","grand_secteur_d_activite":"Services","effectifs_salaries":2000}
	]}`
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	p := URSSAF
	p.BaseURL = base
	dps, err := c.Fetch(context.Background(), p, URSSAF.Specs[0])
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
	if dps[0].SeriesKey == dps[1].SeriesKey {
		t.Errorf("rows differing on dimension share series key %q", dps[0].SeriesKey)
	}
	if dps[0].SeriesPageID() == dps[1].SeriesPageID() {
		t.Error("rows differing on dimension share a page id, would collide on one row")
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
// using the DREES field shape, for the pagination test.
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
		b.WriteString(`{"annee":"2010","poste_niveau":1,"poste_code":"p`)
		b.WriteString(strconv.Itoa(baseAmount + i))
		b.WriteString(`","montants":`)
		b.WriteString(strconv.Itoa(baseAmount + i))
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	return b.String()
}
