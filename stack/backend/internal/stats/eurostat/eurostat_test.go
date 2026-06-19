package eurostat

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realMigrCSV is the exact SDMX-CSV wire format of a live MIGR_RESFIRST query
// (captured 2026-06-19 from the dissemination API), so a passing fixture means
// the real call parses too.
const realMigrCSV = `DATAFLOW,LAST UPDATE,freq,reason,citizen,duration,unit,geo,TIME_PERIOD,OBS_VALUE,OBS_FLAG,CONF_STATUS
ESTAT:MIGR_RESFIRST(1.0),22/05/26 11:00:00,A,TOTAL,TOTAL,TOTAL,PER,FR,2021,287179,,
ESTAT:MIGR_RESFIRST(1.0),22/05/26 11:00:00,A,TOTAL,TOTAL,TOTAL,PER,FR,2022,326948,,
`

// realLfsaCSV is the exact wire format of a live LFSA_ARGAN query, including an
// OBS_FLAG ("bd"/"d") and a decimal OBS_VALUE.
const realLfsaCSV = `DATAFLOW,LAST UPDATE,freq,unit,sex,age,citizen,geo,TIME_PERIOD,OBS_VALUE,OBS_FLAG,CONF_STATUS
ESTAT:LFSA_ARGAN(1.0),11/06/26 23:00:00,A,PC,T,Y15-64,FOR,FR,2021,66.5,bd,
ESTAT:LFSA_ARGAN(1.0),11/06/26 23:00:00,A,PC,T,Y15-64,FOR,FR,2022,66.6,d,
`

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, HTTPClient: srv.Client()})
}

func TestFetchParsesResidencePermits(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "SDMX-CSV" {
			t.Errorf("format = %q, want SDMX-CSV", got)
		}
		w.Header().Set("Content-Type", "application/vnd.sdmx.data+csv;version=1.0.0")
		_, _ = w.Write([]byte(realMigrCSV))
	})

	spec := ResidencePermitsFR
	dps, err := c.Fetch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}

	by := map[string]float64{}
	for _, d := range dps {
		by[d.Period] = d.Figure
		if d.Dataset != "MIGR_RESFIRST" {
			t.Errorf("dataset = %q", d.Dataset)
		}
		if d.SourceName != "Eurostat" {
			t.Errorf("source name = %q", d.SourceName)
		}
		if d.Geography != "France" {
			t.Errorf("geography = %q, want France", d.Geography)
		}
		if d.Unit != "personnes" {
			t.Errorf("unit = %q, want personnes", d.Unit)
		}
		if !strings.Contains(d.SourceURL, "MIGR_RESFIRST") {
			t.Errorf("source url %q missing dataset", d.SourceURL)
		}
		if err := d.Validate(); err != nil {
			t.Errorf("datapoint invalid: %v", err)
		}
	}
	if by["2021"] != 287179 || by["2022"] != 326948 {
		t.Errorf("figures = %v, want 2021:287179 2022:326948", by)
	}
}

func TestFetchParsesEmploymentRate(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realLfsaCSV))
	})

	dps, err := c.Fetch(context.Background(), ActivityRateForeignFR)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
	for _, d := range dps {
		if d.Unit != "%" {
			t.Errorf("unit = %q, want %%", d.Unit)
		}
		if d.Dataset != "LFSA_ARGAN" {
			t.Errorf("dataset = %q", d.Dataset)
		}
		if !containsAll(d.Dimensions, "ressortissants étrangers") {
			t.Errorf("dimensions %v missing foreign-citizen label", d.Dimensions)
		}
	}
}

func containsAll(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func TestFetchSkipsSuppressedObservations(t *testing.T) {
	csv := `DATAFLOW,LAST UPDATE,freq,reason,citizen,duration,unit,geo,TIME_PERIOD,OBS_VALUE,OBS_FLAG,CONF_STATUS
ESTAT:MIGR_RESFIRST(1.0),22/05/26 11:00:00,A,TOTAL,TOTAL,TOTAL,PER,FR,2021,,,
ESTAT:MIGR_RESFIRST(1.0),22/05/26 11:00:00,A,TOTAL,TOTAL,TOTAL,PER,FR,2022,326948,,
`
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(csv))
	})
	dps, err := c.Fetch(context.Background(), ResidencePermitsFR)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 1 {
		t.Fatalf("got %d datapoints, want 1 (suppressed row skipped)", len(dps))
	}
	if dps[0].Period != "2022" {
		t.Errorf("kept period %q, want 2022", dps[0].Period)
	}
}

func TestFetchNon200Wrapped(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<S:Fault><faultstring>INVALID_QUERY</faultstring></S:Fault>`))
	})
	_, err := c.Fetch(context.Background(), ResidencePermitsFR)
	if err == nil {
		t.Fatal("Fetch accepted a 400")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.StatusCode)
	}
}

func TestFetchSchemaDriftWrapped(t *testing.T) {
	// Missing the OBS_VALUE column entirely: schema drift must fail loudly.
	csv := `DATAFLOW,LAST UPDATE,freq,geo,TIME_PERIOD,OBS_FLAG
ESTAT:MIGR_RESFIRST(1.0),22/05/26,A,FR,2022,
`
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(csv))
	})
	_, err := c.Fetch(context.Background(), ResidencePermitsFR)
	if err == nil {
		t.Fatal("Fetch accepted a CSV missing OBS_VALUE")
	}
	if !strings.Contains(err.Error(), "OBS_VALUE") {
		t.Errorf("error %v should name the missing column", err)
	}
}

func TestFetchAsyncPathPolls(t *testing.T) {
	statusHits := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/async/status/"):
			statusHits++
			// First poll PROCESSING, then AVAILABLE.
			if statusHits < 2 {
				_, _ = w.Write([]byte("PROCESSING"))
			} else {
				_, _ = w.Write([]byte("AVAILABLE"))
			}
		case strings.Contains(r.URL.Path, "/async/data/"):
			_, _ = w.Write([]byte(realMigrCSV))
		default:
			// Initial data query returns a bare UUID -> async.
			_, _ = w.Write([]byte("98de05ea-540a-43d3-903b-7c9e14faf808"))
		}
	})
	c.pollInterval = 0 // no real sleeping in the test

	dps, err := c.Fetch(context.Background(), ResidencePermitsFR)
	if err != nil {
		t.Fatalf("Fetch (async): %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("async got %d datapoints, want 2", len(dps))
	}
	if statusHits < 2 {
		t.Errorf("polled status %d times, want >= 2", statusHits)
	}
}

func TestFetchAsyncErrorStatusWrapped(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/async/status/"):
			_, _ = w.Write([]byte("ERROR"))
		default:
			_, _ = w.Write([]byte("98de05ea-540a-43d3-903b-7c9e14faf808"))
		}
	})
	c.pollInterval = 0
	_, err := c.Fetch(context.Background(), ResidencePermitsFR)
	if err == nil {
		t.Fatal("Fetch accepted an async ERROR status")
	}
	if !strings.Contains(err.Error(), "ERROR") {
		t.Errorf("error %v should report the async status", err)
	}
}

func TestFetchDecompressesGzipBody(t *testing.T) {
	// Eurostat gzips the body (without a Content-Encoding header the Go
	// transport would honor) whenever a gzip-capable Accept-Encoding is sent,
	// so the adapter must detect the gzip magic and decompress it.
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte(realMigrCSV))
	_ = w.Close()
	gzipped := gz.Bytes()

	c := newTestClient(t, func(rw http.ResponseWriter, r *http.Request) {
		if ae := r.Header.Get("Accept-Encoding"); ae != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity", ae)
		}
		// Serve raw gzip bytes with no Content-Encoding header (the real API's
		// behavior); httptest must not re-encode.
		_, _ = rw.Write(gzipped)
	})
	dps, err := c.Fetch(context.Background(), ResidencePermitsFR)
	if err != nil {
		t.Fatalf("Fetch (gzip): %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("gzip got %d datapoints, want 2", len(dps))
	}
}

func TestQueryURLDotKeyAndFormat(t *testing.T) {
	c := New(Config{})
	got := c.queryURL(ResidencePermitsFR)
	for _, want := range []string{
		"data/MIGR_RESFIRST/",
		ResidencePermitsFR.Key,
		"format=SDMX-CSV",
		"startPeriod=",
		"endPeriod=",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("queryURL %q missing %q", got, want)
		}
	}
}
