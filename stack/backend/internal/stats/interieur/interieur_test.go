package interieur

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realPermitsByCountryCSV is the exact wire format of the interior ministry's
// "sejour-flux-pays-nationalite-2023.csv" resource on data.gouv.fr (captured
// 2026-06-19): comma-delimited UTF-8, an "n.c" suppression sentinel, French
// country names. A passing fixture means the real download parses too.
const realPermitsByCountryCSV = `region_monde_origine,pays_nationalite,code_iso3,nb_titres
AFRIQUE_HORS_MAGHREB,AFRIQUE DU SUD,ZAF,588
AFRIQUE_HORS_MAGHREB,ANGOLA,AGO,975
AFRIQUE_HORS_MAGHREB,BOTSWANA,BWA,7
MAGHREB,ALGERIE,DZA,30000
MAGHREB,MAROC,MAR,33268
EUROPE,SUPPRIME,XXX,n.c
`

// realAsylumByCountryCSV is the wire format of "Asile_pays-origine_2024.csv":
// the same shape as the permit file but a nb_demandes value column.
const realAsylumByCountryCSV = `region_monde_origine,pays_nationalite,code_iso3,nb_demandes
AFRIQUE_HORS_MAGHREB,AFRIQUE DU SUD,ZAF,39
AFRIQUE_HORS_MAGHREB,ANGOLA,AGO,2852
AFRIQUE_HORS_MAGHREB,BOTSWANA,BWA,n.c
`

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{HTTPClient: srv.Client()}), srv.URL
}

func TestFetchParsesPermitsByCountry(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		_, _ = w.Write([]byte(realPermitsByCountryCSV))
	})

	spec := Spec{
		URL:              base + "/permits.csv",
		Dataset:          "titres-de-sejour-2023",
		Year:             "2023",
		Title:            "Premiers titres de séjour délivrés",
		ValueColumn:      "nb_titres",
		KeyColumn:        "code_iso3",
		DimensionColumns: []string{"pays_nationalite"},
		Unit:             "personnes",
	}

	dps, err := c.Fetch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// 5 data rows have a numeric value; the "n.c" row is suppressed.
	if len(dps) != 5 {
		t.Fatalf("got %d datapoints, want 5 (n.c row skipped)", len(dps))
	}

	var morocco bool
	for _, d := range dps {
		if d.SourceName != sourceName {
			t.Errorf("source name = %q, want %q", d.SourceName, sourceName)
		}
		if d.SourceURL != spec.URL {
			t.Errorf("source url = %q, want %q", d.SourceURL, spec.URL)
		}
		if d.Dataset != "titres-de-sejour-2023" {
			t.Errorf("dataset = %q", d.Dataset)
		}
		if d.Period != "2023" {
			t.Errorf("period = %q, want 2023", d.Period)
		}
		if d.Geography != "France" {
			t.Errorf("geography = %q, want France", d.Geography)
		}
		if d.Unit != "personnes" {
			t.Errorf("unit = %q", d.Unit)
		}
		if err := d.Validate(); err != nil {
			t.Errorf("datapoint invalid: %v", err)
		}
		if d.SeriesKey == "MAR" {
			morocco = true
			if d.Figure != 33268 {
				t.Errorf("Morocco figure = %v, want 33268", d.Figure)
			}
			if len(d.Dimensions) != 1 || d.Dimensions[0] != "MAROC" {
				t.Errorf("Morocco dimensions = %v, want [MAROC]", d.Dimensions)
			}
		}
	}
	if !morocco {
		t.Error("did not parse the Morocco (MAR) row")
	}
}

func TestFetchParsesAsylumByCountry(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realAsylumByCountryCSV))
	})

	spec := Spec{
		URL:              base + "/asylum.csv",
		Dataset:          "demandes-asile-2024",
		Year:             "2024",
		Title:            "Demandes d'asile",
		ValueColumn:      "nb_demandes",
		KeyColumn:        "code_iso3",
		DimensionColumns: []string{"pays_nationalite"},
		Unit:             "demandes",
	}
	dps, err := c.Fetch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2 (n.c skipped)", len(dps))
	}
	for _, d := range dps {
		if d.Unit != "demandes" {
			t.Errorf("unit = %q, want demandes", d.Unit)
		}
		if d.Period != "2024" {
			t.Errorf("period = %q, want 2024", d.Period)
		}
	}
}

func TestFetchSchemaDriftMissingValueColumn(t *testing.T) {
	csv := `region_monde_origine,pays_nationalite,code_iso3
MAGHREB,MAROC,MAR
`
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(csv))
	})
	_, err := c.Fetch(context.Background(), Spec{
		URL: base + "/x.csv", Dataset: "d", Year: "2023", Title: "t",
		ValueColumn: "nb_titres", KeyColumn: "code_iso3", Unit: "personnes",
	})
	if err == nil {
		t.Fatal("Fetch accepted a CSV missing the value column")
	}
	if !strings.Contains(err.Error(), "nb_titres") {
		t.Errorf("error %v should name the missing value column", err)
	}
}

func TestFetchSchemaDriftMissingKeyColumn(t *testing.T) {
	csv := `region_monde_origine,pays_nationalite,nb_titres
MAGHREB,MAROC,33268
`
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(csv))
	})
	_, err := c.Fetch(context.Background(), Spec{
		URL: base + "/x.csv", Dataset: "d", Year: "2023", Title: "t",
		ValueColumn: "nb_titres", KeyColumn: "code_iso3", Unit: "personnes",
	})
	if err == nil {
		t.Fatal("Fetch accepted a CSV missing the key column")
	}
	if !strings.Contains(err.Error(), "code_iso3") {
		t.Errorf("error %v should name the missing key column", err)
	}
}

func TestFetchNon200Wrapped(t *testing.T) {
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})
	_, err := c.Fetch(context.Background(), Spec{
		URL: base + "/missing.csv", Dataset: "d", Year: "2023", Title: "t",
		ValueColumn: "nb_titres", KeyColumn: "code_iso3", Unit: "personnes",
	})
	if err == nil {
		t.Fatal("Fetch accepted a 404")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
}

func TestFetchBadValueWrapped(t *testing.T) {
	csv := `region_monde_origine,pays_nationalite,code_iso3,nb_titres
MAGHREB,MAROC,MAR,thirty-three-thousand
`
	c, base := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(csv))
	})
	_, err := c.Fetch(context.Background(), Spec{
		URL: base + "/x.csv", Dataset: "d", Year: "2023", Title: "t",
		ValueColumn: "nb_titres", KeyColumn: "code_iso3", Unit: "personnes",
	})
	if err == nil {
		t.Fatal("Fetch accepted a non-numeric value")
	}
}

func TestSourceCorpus(t *testing.T) {
	s := NewSource(New(Config{}), nil)
	if s.Corpus() != "interieur" {
		t.Errorf("Corpus() = %q, want interieur", s.Corpus())
	}
}
