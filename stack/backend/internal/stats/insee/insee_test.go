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
)

// realBDMResponse is the SDMX-ML 2.1 StructureSpecificData wire format of a live
// bdm.insee.fr/series/sdmx data query (shape captured 2026-06-19): a DataSet of
// Series elements, each carrying IDBANK and dimension attributes, with Obs
// children carrying TIME_PERIOD/OBS_VALUE/OBS_STATUS. A passing fixture means
// the real anonymous call parses too.
const realBDMResponse = `<?xml version="1.0" encoding="UTF-8"?>
<message:StructureSpecificData xmlns:message="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/message" xmlns:ss="http://www.sdmx.org/resources/sdmxml/schemas/v2_1/data/structurespecific">
  <message:Header>
    <message:ID>IDREF</message:ID>
  </message:Header>
  <message:DataSet ss:structureRef="SERIES_BDM">
    <Series IDBANK="010755676" FREQ="A" UNIT_MEASURE="PCT">
      <Obs TIME_PERIOD="2022" OBS_VALUE="59.5" OBS_STATUS="A"/>
      <Obs TIME_PERIOD="2023" OBS_VALUE="59.8" OBS_STATUS="A"/>
    </Series>
    <Series IDBANK="010755677" FREQ="A" UNIT_MEASURE="PCT">
      <Obs TIME_PERIOD="2023" OBS_VALUE="12.4" OBS_STATUS="A"/>
      <Obs TIME_PERIOD="2024" OBS_VALUE="NaN" OBS_STATUS="O"/>
    </Series>
  </message:DataSet>
</message:StructureSpecificData>`

func newTestClient(t *testing.T, cfg Config, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	cfg.HTTPClient = srv.Client()
	cfg.MinInterval = time.Millisecond // no real throttle sleep in tests
	return New(cfg)
}

func employmentRateImmigrants() Spec {
	return Spec{
		IDBank:     "010755676",
		Dataset:    "EEC",
		Title:      "Taux d'emploi",
		Dimensions: []string{"immigrés", "15 à 64 ans"},
		Unit:       "%",
		StartYear:  "2014",
	}
}

func TestFetchParsesEmploymentRate(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/data/SERIES_BDM/010755676") {
			t.Errorf("path = %q, want the SERIES_BDM data query", r.URL.Path)
		}
		if got := r.URL.Query().Get("startPeriod"); got != "2014" {
			t.Errorf("startPeriod = %q, want 2014", got)
		}
		_, _ = w.Write([]byte(realBDMResponse))
	})

	dps, err := c.Fetch(context.Background(), employmentRateImmigrants())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Two numeric observations on the requested IDBANK series; the NaN
	// observation on the other series is suppressed, and only the requested
	// IDBANK's series is mapped.
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
	by := map[string]float64{}
	for _, d := range dps {
		by[d.Period] = d.Figure
		if d.SourceName != sourceName {
			t.Errorf("source name = %q, want %q", d.SourceName, sourceName)
		}
		if d.Dataset != "EEC" {
			t.Errorf("dataset = %q", d.Dataset)
		}
		if d.SeriesKey != "010755676" {
			t.Errorf("series key = %q, want the IDBANK", d.SeriesKey)
		}
		if d.Geography != "France" {
			t.Errorf("geography = %q, want France", d.Geography)
		}
		if d.Unit != "%" {
			t.Errorf("unit = %q, want %%", d.Unit)
		}
		if err := d.Validate(); err != nil {
			t.Errorf("datapoint invalid: %v", err)
		}
	}
	if by["2022"] != 59.5 || by["2023"] != 59.8 {
		t.Errorf("figures = %v, want 2022:59.5 2023:59.8", by)
	}
}

func TestFetchSkipsNaNObservations(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realBDMResponse))
	})
	// The other series (010755677) carries one numeric and one NaN observation.
	dps, err := c.Fetch(context.Background(), Spec{
		IDBank: "010755677", Dataset: "EEC", Title: "Taux de chômage",
		Dimensions: []string{"immigrés"}, Unit: "%",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 1 {
		t.Fatalf("got %d datapoints, want 1 (NaN skipped)", len(dps))
	}
	if dps[0].Period != "2023" || dps[0].Figure != 12.4 {
		t.Errorf("kept %v, want 2023:12.4", dps[0])
	}
}

func TestFetchNon200Wrapped(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("rate limited"))
	})
	_, err := c.Fetch(context.Background(), employmentRateImmigrants())
	if err == nil {
		t.Fatal("Fetch accepted a 503")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not *APIError", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", apiErr.StatusCode)
	}
}

func TestFetchMalformedXMLWrapped(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<not-sdmx>"))
	})
	_, err := c.Fetch(context.Background(), employmentRateImmigrants())
	if err == nil {
		t.Fatal("Fetch accepted malformed XML")
	}
}

// TestAnonymousByDefault proves the open BDM endpoint is queried without an
// Authorization header when no API key is configured.
func TestAnonymousByDefault(t *testing.T) {
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("Authorization header = %q, want none (anonymous)", auth)
		}
		_, _ = w.Write([]byte(realBDMResponse))
	})
	if _, err := c.Fetch(context.Background(), employmentRateImmigrants()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

// TestAPIKeyForwardedWhenSet proves an optional API key, when configured, is
// sent as a Bearer token and never required.
func TestAPIKeyForwardedWhenSet(t *testing.T) {
	c := newTestClient(t, Config{APIKey: "secret-token"}, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want Bearer secret-token", got)
		}
		_, _ = w.Write([]byte(realBDMResponse))
	})
	if _, err := c.Fetch(context.Background(), employmentRateImmigrants()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

// TestSourceThrottlesBetweenRequests proves the source spaces successive series
// fetches by at least MinInterval, so the documented rate limit is respected.
func TestSourceThrottlesBetweenRequests(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(realBDMResponse))
	}))
	t.Cleanup(srv.Close)

	c := New(Config{BaseURL: srv.URL, HTTPClient: srv.Client(), MinInterval: 40 * time.Millisecond})
	specs := []Spec{
		{IDBank: "010755676", Dataset: "EEC", Title: "Taux d'emploi", Dimensions: []string{"immigrés"}, Unit: "%"},
		{IDBank: "010755677", Dataset: "EEC", Title: "Taux de chômage", Dimensions: []string{"immigrés"}, Unit: "%"},
	}
	src := NewSource(c, specs)

	start := time.Now()
	if _, err := src.Datapoints(context.Background()); err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("two throttled requests took %v, want >= 40ms (rate limit respected)", elapsed)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Errorf("server hit %d times, want 2", hits)
	}
}

func TestSourceCorpus(t *testing.T) {
	s := NewSource(New(Config{}), nil)
	if s.Corpus() != "insee" {
		t.Errorf("Corpus() = %q, want insee", s.Corpus())
	}
}

// TestNewReadsAPIKeyFromEnvOnly proves the optional credential is sourced from
// the environment, never hard-coded, and absence is a clean anonymous client.
func TestNewReadsAPIKeyFromEnvOnly(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	if got := ConfigFromEnv().APIKey; got != "" {
		t.Errorf("APIKey = %q with empty env, want empty (anonymous)", got)
	}
	t.Setenv(apiKeyEnv, "from-env")
	if got := ConfigFromEnv().APIKey; got != "from-env" {
		t.Errorf("APIKey = %q, want from-env", got)
	}
}
