package eurostat

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSourceCombinesSpecs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "MIGR_RESFIRST") {
			_, _ = w.Write([]byte(realMigrCSV))
			return
		}
		_, _ = w.Write([]byte(realLfsaCSV))
	})
	src := NewSource(c, CuratedSpecs)
	dps, err := src.Datapoints(context.Background())
	if err != nil {
		t.Fatalf("Datapoints: %v", err)
	}
	// 2 permit rows + 2 activity rows.
	if len(dps) != 4 {
		t.Fatalf("got %d datapoints, want 4", len(dps))
	}
}

func TestSourceFailsOnSpecError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	})
	src := NewSource(c, nil) // default curated specs
	if _, err := src.Datapoints(context.Background()); err == nil {
		t.Fatal("Datapoints accepted a 500 from the first spec")
	}
}
