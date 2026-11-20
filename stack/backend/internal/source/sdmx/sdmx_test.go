package sdmx

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

// realECBCSV is the SDMX-CSV wire shape of an ECB Data Portal ICP query
// (format=csvdata): a leading KEY column, one column per DSD dimension, then
// TIME_PERIOD, OBS_VALUE and observation-status attributes. Columns are resolved
// by header name, so the exact dimension set is irrelevant to parsing.
const realECBCSV = `KEY,FREQ,REF_AREA,ADJUSTMENT,ICP_ITEM,STS_INSTITUTION,ICP_SUFFIX,TIME_PERIOD,OBS_VALUE,OBS_STATUS,OBS_CONF
ICP.M.U2.N.000000.4.ANR,M,U2,N,000000,4,ANR,2024-01,2.8,A,F
ICP.M.U2.N.000000.4.ANR,M,U2,N,000000,4,ANR,2024-02,2.6,A,F
ICP.M.U2.N.000000.4.ANR,M,U2,N,000000,4,ANR,2024-03,,L,F
`

// realOECDCSV is the SDMX-CSV wire shape of an OECD query (format=csvfilewithlabels):
// STRUCTURE metadata columns, paired code/label columns per dimension, and the
// TIME_PERIOD and OBS_VALUE code columns the parser keys off. A "NaN" OBS_VALUE is
// a suppressed observation.
const realOECDCSV = `STRUCTURE,STRUCTURE_ID,ACTION,REF_AREA,Reference area,MEASURE,Measure,TIME_PERIOD,Time period,OBS_VALUE,Observation value,UNIT_MEASURE,Unit of measure
DATAFLOW,OECD.SDD.TPS:DSD_LFS@DF_IALFS_UNE_M(1.0),I,FRA,France,UNE_RT,Unemployment rate,2024-01,January 2024,7.4,7.4,PT_LF_SUB,Percentage
DATAFLOW,OECD.SDD.TPS:DSD_LFS@DF_IALFS_UNE_M(1.0),I,FRA,France,UNE_RT,Unemployment rate,2024-02,NaN,NaN,PT_LF_SUB,Percentage
DATAFLOW,OECD.SDD.TPS:DSD_LFS@DF_IALFS_UNE_M(1.0),I,FRA,France,UNE_RT,Unemployment rate,2024-03,7.3,7.3,PT_LF_SUB,Percentage
`

func newTestClient(t *testing.T, ep Endpoint, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	ep.BaseURL = srv.URL
	// Retries disabled by default so an error-status test asserts the first
	// outcome without backoff waits; a retry test sets its own Retry.
	if (ep.Retry == httpx.RetryConfig{}) {
		ep.Retry = httpx.RetryConfig{MaxRetries: -1}
	}
	return New(ep)
}

func ecbSpec() Spec {
	return Spec{FlowRef: "ICP", Key: "M.U2.N.000000.4.ANR", StartPeriod: "2024-01", Dataset: "ICP", Title: "Inflation IPCH", GeographyLabel: "la zone euro", Unit: "%"}
}

func TestFetchParsesECB(t *testing.T) {
	c := newTestClient(t, ECBEndpoint(), func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "csvdata" {
			t.Errorf("format = %q, want csvdata", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/data/ICP/M.U2.N.000000.4.ANR") {
			t.Errorf("path = %q, want the ECB data path", r.URL.Path)
		}
		_, _ = w.Write([]byte(realECBCSV))
	})

	dps, err := c.Fetch(context.Background(), ecbSpec())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2 (the suppressed 2024-03 row is skipped)", len(dps))
	}
	if dps[0].Period != "2024-01" || dps[0].Figure != 2.8 {
		t.Errorf("dp0 = %s/%v, want 2024-01/2.8", dps[0].Period, dps[0].Figure)
	}
	if dps[0].SourceName != "Banque centrale européenne (BCE)" {
		t.Errorf("source name = %q", dps[0].SourceName)
	}
	if dps[0].Dataset != "ICP" || dps[0].SeriesKey != "M.U2.N.000000.4.ANR" || dps[0].Unit != "%" {
		t.Errorf("provenance = %s/%s/%s", dps[0].Dataset, dps[0].SeriesKey, dps[0].Unit)
	}
	if err := dps[0].Validate(); err != nil {
		t.Errorf("parsed datapoint does not validate: %v", err)
	}
}

func TestFetchParsesOECDWithLabels(t *testing.T) {
	spec := Spec{FlowRef: "OECD.SDD.TPS,DSD_LFS@DF_IALFS_UNE_M,1.0", Key: "FRA...._Z.Y._T.Y_GE15.M", StartPeriod: "2024-01", Title: "Chômage", GeographyLabel: "France", Unit: "%"}
	c := newTestClient(t, OECDEndpoint(), func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "csvfilewithlabels" {
			t.Errorf("format = %q, want csvfilewithlabels", got)
		}
		// The OECD flow-ref triple's commas and @ must survive as path punctuation.
		if !strings.Contains(r.URL.Path, "DSD_LFS@DF_IALFS_UNE_M,1.0") {
			t.Errorf("path lost the OECD flow ref: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(realOECDCSV))
	})

	dps, err := c.Fetch(context.Background(), spec)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2 (the NaN 2024-02 row is skipped)", len(dps))
	}
	if dps[1].Period != "2024-03" || dps[1].Figure != 7.3 {
		t.Errorf("dp1 = %s/%v, want 2024-03/7.3", dps[1].Period, dps[1].Figure)
	}
	if dps[0].Dataset != spec.FlowRef {
		t.Errorf("dataset fallback = %q, want the flow ref %q", dps[0].Dataset, spec.FlowRef)
	}
}

func TestFetchSchemaDriftFailsLoudly(t *testing.T) {
	c := newTestClient(t, ECBEndpoint(), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("KEY,FREQ,TIME_PERIOD,VALUE\nX,M,2024-01,2.8\n"))
	})
	_, err := c.Fetch(context.Background(), ecbSpec())
	if err == nil || !strings.Contains(err.Error(), "OBS_VALUE") {
		t.Fatalf("err = %v, want a schema-drift error naming OBS_VALUE", err)
	}
}

func TestFetchEmptyBodyIsEmptySeries(t *testing.T) {
	c := newTestClient(t, ECBEndpoint(), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	dps, err := c.Fetch(context.Background(), ecbSpec())
	if err != nil {
		t.Fatalf("Fetch on empty body: %v", err)
	}
	if len(dps) != 0 {
		t.Fatalf("got %d datapoints, want 0", len(dps))
	}
}

func TestFetchMapsNon2xxToAPIError(t *testing.T) {
	c := newTestClient(t, ECBEndpoint(), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such flow"))
	})
	_, err := c.Fetch(context.Background(), ecbSpec())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", apiErr.StatusCode)
	}
}

func TestFetchRetriesOnThrottle(t *testing.T) {
	var calls atomic.Int32
	ep := ECBEndpoint()
	ep.MinInterval = 0
	ep.Retry = httpx.RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	c := newTestClient(t, ep, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(realECBCSV))
	})
	dps, err := c.Fetch(context.Background(), ecbSpec())
	if err != nil {
		t.Fatalf("Fetch after a throttled first attempt: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
	if calls.Load() != 2 {
		t.Fatalf("server hit %d times, want 2 (one throttle + one retry)", calls.Load())
	}
}

func TestFetchDefensivelyGunzips(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(realECBCSV))
	_ = zw.Close()
	c := newTestClient(t, ECBEndpoint(), func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately no Content-Encoding header, as some endpoints return.
		_, _ = w.Write(buf.Bytes())
	})
	dps, err := c.Fetch(context.Background(), ecbSpec())
	if err != nil {
		t.Fatalf("Fetch on gzip body: %v", err)
	}
	if len(dps) != 2 {
		t.Fatalf("got %d datapoints, want 2", len(dps))
	}
}

func TestClientIDHeaderSentOnlyWhenConfigured(t *testing.T) {
	t.Run("configured", func(t *testing.T) {
		ep := ECBEndpoint()
		ep.ClientIDHeader = "X-IBM-Client-Id"
		ep.ClientID = "secret-value"
		c := newTestClient(t, ep, func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("X-IBM-Client-Id"); got != "secret-value" {
				t.Errorf("client-id header = %q, want it forwarded", got)
			}
			_, _ = w.Write([]byte(realECBCSV))
		})
		if _, err := c.Fetch(context.Background(), ecbSpec()); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	})
	t.Run("anonymous", func(t *testing.T) {
		c := newTestClient(t, ECBEndpoint(), func(w http.ResponseWriter, r *http.Request) {
			if _, ok := r.Header["X-Ibm-Client-Id"]; ok {
				t.Errorf("anonymous client sent an auth header")
			}
			_, _ = w.Write([]byte(realECBCSV))
		})
		if _, err := c.Fetch(context.Background(), ecbSpec()); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	})
}

func TestThrottleSpacesSuccessiveRequests(t *testing.T) {
	ep := ECBEndpoint()
	ep.MinInterval = 40 * time.Millisecond
	c := newTestClient(t, ep, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realECBCSV))
	})
	start := time.Now()
	for range 2 {
		if _, err := c.Fetch(context.Background(), ecbSpec()); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < ep.MinInterval {
		t.Errorf("two throttled fetches took %s, want at least %s", elapsed, ep.MinInterval)
	}
}

func TestQueryURLBuildsWindowAndFormat(t *testing.T) {
	c := New(Endpoint{BaseURL: "https://example.test/service", Format: "csvdata"})
	got := c.queryURL(Spec{FlowRef: "ICP", Key: "M.U2.N.000000.4.ANR", StartPeriod: "2015-01", EndPeriod: "2025-12"})
	for _, want := range []string{
		"https://example.test/service/data/ICP/M.U2.N.000000.4.ANR?",
		"format=csvdata", "startPeriod=2015-01", "endPeriod=2025-12",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("query url %q missing %q", got, want)
		}
	}
}
