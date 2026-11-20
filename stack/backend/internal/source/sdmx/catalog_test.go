package sdmx

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TestCuratedSpecsAreWellFormed proves every curated series carries the fields the
// query and the rendered passage need, and that each spec's default period parses
// to a valid chunk index, so a run never publishes a datapoint the store rejects.
func TestCuratedSpecsAreWellFormed(t *testing.T) {
	groups := map[string][]Spec{
		"eurostat": EurostatSpecs(Window{}),
		"ecb":      ECBSpecs(Window{}),
		"oecd":     OECDSpecs(Window{}),
	}
	for name, specs := range groups {
		if len(specs) == 0 {
			t.Errorf("%s has no curated specs", name)
		}
		for _, s := range specs {
			if s.FlowRef == "" || s.Key == "" || s.Title == "" || s.Unit == "" || s.StartPeriod == "" {
				t.Errorf("%s spec %q is missing a required field: %+v", name, s.Title, s)
			}
			// The default start period must be a period the store can key on.
			dp := domain.Datapoint{
				SourceName: "x", SourceURL: "https://x", Dataset: s.FlowRef,
				SeriesKey: s.Key, Title: s.Title, Unit: s.Unit, Period: s.StartPeriod,
			}
			if _, err := dp.PeriodChunkIndex(); err != nil {
				t.Errorf("%s spec %q default start %q is not a valid period: %v", name, s.Title, s.StartPeriod, err)
			}
		}
	}
}

func TestWindowOverridesSpecPeriodsWithoutMutating(t *testing.T) {
	base := ECBSpecs(Window{})
	baseStart := base[0].StartPeriod

	got := ECBSpecs(Window{Start: "2000-01", End: "2020-12"})
	if got[0].StartPeriod != "2000-01" || got[0].EndPeriod != "2020-12" {
		t.Errorf("window not applied: %+v", got[0])
	}
	// The package-level curated slice must be untouched by a windowed call.
	if ECBSpecs(Window{})[0].StartPeriod != baseStart {
		t.Errorf("windowed call mutated the shared curated specs")
	}
}

func TestEndpointsAreAnonymousSDMXCSV(t *testing.T) {
	for _, ep := range []Endpoint{EurostatEndpoint(), ECBEndpoint(), OECDEndpoint()} {
		if ep.ClientID != "" || ep.ClientIDHeader != "" {
			t.Errorf("endpoint %q is not anonymous", ep.SourceName)
		}
		if ep.BaseURL == "" || ep.SourceName == "" {
			t.Errorf("endpoint missing base url or source name: %+v", ep)
		}
	}
}
