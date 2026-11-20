package sdmx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
)

func TestSourceConcatenatesSpecsUnderCorpus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(realECBCSV))
	}))
	t.Cleanup(srv.Close)
	ep := ECBEndpoint()
	ep.BaseURL = srv.URL
	ep.MinInterval = 0
	ep.Retry = httpx.RetryConfig{MaxRetries: -1}
	client := New(ep)

	src := NewSource(client, domain.ECBStatCorpus, []Spec{ecbSpec(), ecbSpec()})
	if src.Corpus() != domain.ECBStatCorpus {
		t.Fatalf("corpus = %q, want %q", src.Corpus(), domain.ECBStatCorpus)
	}
	dps, err := src.Datapoints(context.Background())
	if err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	// Two specs, two non-suppressed observations each.
	if len(dps) != 4 {
		t.Fatalf("got %d datapoints, want 4", len(dps))
	}
}

func TestSourceDatapointsPropagatesFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	ep := ECBEndpoint()
	ep.BaseURL = srv.URL
	ep.MinInterval = 0
	ep.Retry = httpx.RetryConfig{MaxRetries: -1}
	src := NewSource(New(ep), domain.ECBStatCorpus, []Spec{ecbSpec()})
	if _, err := src.Datapoints(context.Background()); err == nil {
		t.Fatal("Datapoints() = nil error, want the upstream failure propagated")
	}
}

func TestNewSourceRejectsNonStatisticalCorpus(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewSource with a non-statistical corpus did not panic")
		}
	}()
	NewSource(New(ECBEndpoint()), "wikipedia", nil)
}
