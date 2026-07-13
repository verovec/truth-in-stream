package ssmsi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// realDepartmentalCSV is the exact wire format of the SSMSI départemental
// delinquency base captured 2026-07 from data.gouv.fr: semicolon-delimited,
// double-quoted UTF-8, a French decimal comma in taux_pour_mille, and the shared
// annee/indicateur/unite_de_compte/nombre columns. A passing fixture means the real
// download parses too.
const realDepartmentalCSV = `"Code_departement";"Code_region";"annee";"indicateur";"unite_de_compte";"nombre";"taux_pour_mille";"insee_pop";"insee_pop_millesime";"insee_log";"insee_log_millesime"
"01";"84";"2016";"Homicides";"Victime";"5";"0,0078318";"638425";"2016";"308491";"2016"
"01";"84";"2016";"Coups et blessures volontaires";"Victime";"1200";"1,8796";"638425";"2016";"308491";"2016"
"75";"11";"2016";"Vols sans violence contre des personnes";"Infraction";"90000";"40,12";"2190327";"2016";"1300000";"2016"
"13";"93";"2016";"Homicides";"Victime";"";"";"2016000";"2016";"900000";"2016"
`

// datasetJSON is the data.gouv.fr dataset API envelope the fetcher resolves the
// current CSV resource through. Two resources are present; only the départemental
// CSV must be selected (the communal one is gzip and must be skipped).
func datasetJSON(csvURL string) string {
	return fmt.Sprintf(`{
	  "resources": [
	    {"title": "Base communale de la délinquance (csv.gz)", "format": "csv.gz", "url": "https://example.test/communal.csv.gz"},
	    {"title": "Base départementale de la délinquance", "format": "csv", "url": %q},
	    {"title": "Base régionale de la délinquance", "format": "csv", "url": "https://example.test/regional.csv"}
	  ]
	}`, csvURL)
}

func newTestClient(t *testing.T, mux http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
}

func TestFetchResolvesAndParsesDepartmentalBase(t *testing.T) {
	mux := http.NewServeMux()
	var csvURL string
	mux.HandleFunc("/csv/departmental.csv", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		_, _ = w.Write([]byte(realDepartmentalCSV))
	})
	mux.HandleFunc("/api/1/datasets/"+delinquencyDatasetSlug+"/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(datasetJSON(csvURL)))
	})

	// Resolve the CSV URL against the same server so the fixture is self-contained.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	csvURL = srv.URL + "/csv/departmental.csv"
	c := New(Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	dps, err := c.Fetch(context.Background(), DepartmentalBase)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Three rows carry a count; the blank-nombre row is a suppressed observation.
	if len(dps) != 3 {
		t.Fatalf("got %d datapoints, want 3 (blank count skipped)", len(dps))
	}

	var homicide bool
	for _, d := range dps {
		if d.SourceName != sourceName {
			t.Errorf("source name = %q, want %q", d.SourceName, sourceName)
		}
		if d.Dataset != "delinquance-departementale" {
			t.Errorf("dataset = %q", d.Dataset)
		}
		if d.Period != "2016" {
			t.Errorf("period = %q, want 2016", d.Period)
		}
		if d.Unit != figureUnit {
			t.Errorf("unit = %q, want %q", d.Unit, figureUnit)
		}
		if err := d.Validate(); err != nil {
			t.Errorf("datapoint invalid: %v", err)
		}
		if d.Title == "Homicides" && d.Geography == "département 01" {
			homicide = true
			if d.Figure != 5 {
				t.Errorf("Ain homicides figure = %v, want 5", d.Figure)
			}
			if len(d.Dimensions) != 1 || d.Dimensions[0] != "Victime" {
				t.Errorf("dimensions = %v, want [Victime]", d.Dimensions)
			}
		}
	}
	if !homicide {
		t.Error("did not parse the Ain (01) homicides row")
	}
}

func TestFetchDistinctSeriesKeysPerIndicator(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/csv/departmental.csv", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realDepartmentalCSV))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	csvURL := srv.URL + "/csv/departmental.csv"
	mux.HandleFunc("/api/1/datasets/"+delinquencyDatasetSlug+"/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(datasetJSON(csvURL)))
	})
	c := New(Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	dps, err := c.Fetch(context.Background(), DepartmentalBase)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The two 2016 rows for department 01 (Homicides vs Coups et blessures) must not
	// collide on one provenance row.
	seen := make(map[int64]struct{})
	for _, d := range dps {
		if d.Geography != "département 01" {
			continue
		}
		id := d.SeriesPageID()
		if _, dup := seen[id]; dup {
			t.Errorf("two Ain indicators collide on page id %d", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("got %d distinct Ain series, want 2", len(seen))
	}
}

func TestFetchNoMatchingResource(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/datasets/"+delinquencyDatasetSlug+"/", func(w http.ResponseWriter, _ *http.Request) {
		// Only a gzip communal resource, no plain CSV: resolution must fail loudly.
		_, _ = w.Write([]byte(`{"resources":[{"title":"Base communale","format":"csv.gz","url":"https://example.test/c.csv.gz"}]}`))
	})
	c := newTestClient(t, mux)
	_, err := c.Fetch(context.Background(), DepartmentalBase)
	if err == nil {
		t.Fatal("Fetch accepted a dataset with no matching CSV resource")
	}
	if !strings.Contains(err.Error(), "no csv resource") {
		t.Errorf("error %v should report no matching csv resource", err)
	}
}

func TestFetchSchemaDriftMissingColumn(t *testing.T) {
	badCSV := `"Code_departement";"annee";"indicateur";"nombre"
"01";"2016";"Homicides";"5"
`
	mux := http.NewServeMux()
	mux.HandleFunc("/csv/x.csv", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(badCSV))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	csvURL := srv.URL + "/csv/x.csv"
	mux.HandleFunc("/api/1/datasets/"+delinquencyDatasetSlug+"/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(datasetJSON(csvURL)))
	})
	c := New(Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	_, err := c.Fetch(context.Background(), DepartmentalBase)
	if err == nil {
		t.Fatal("Fetch accepted a base missing unite_de_compte")
	}
	if !strings.Contains(err.Error(), colUnit) {
		t.Errorf("error %v should name the missing column", err)
	}
}

func TestFetchDatasetAPINon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/1/datasets/"+delinquencyDatasetSlug+"/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})
	c := newTestClient(t, mux)
	_, err := c.Fetch(context.Background(), DepartmentalBase)
	if err == nil {
		t.Fatal("Fetch accepted a 404 dataset lookup")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
}

func TestSourceCorpus(t *testing.T) {
	s := NewSource(New(Config{}), nil)
	if s.Corpus() != domain.SSMSIStatCorpus {
		t.Errorf("Corpus() = %q, want %q", s.Corpus(), domain.SSMSIStatCorpus)
	}
	if !domain.IsStatCorpus(s.Corpus()) {
		t.Errorf("corpus %q is not a registered statistical corpus", s.Corpus())
	}
}
