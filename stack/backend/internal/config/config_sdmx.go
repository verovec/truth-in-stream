package config

import (
	"fmt"
	"strings"
)

// knownSDMXSources is the set of institutions the generic SDMX connector can run;
// an unknown name in SDMX_SOURCES fails the run fast rather than silently
// ingesting nothing.
var knownSDMXSources = map[string]struct{}{
	"eurostat": {},
	"ecb":      {},
	"oecd":     {},
}

// defaultSDMXSources is the institution set a run ingests when SDMX_SOURCES is
// unset: every keyless institution the connector supports.
var defaultSDMXSources = []string{"eurostat", "ecb", "oecd"}

// SDMX holds the non-secret configuration for the SDMX connector producer. Sources
// selects which institutions to ingest; Start/End override the curated period
// window for every series (empty keeps each series' catalog default).
type SDMX struct {
	Sources []string
	Start   string
	End     string
}

// LoadSDMX reads the SDMX connector configuration from the environment. All
// values are optional and non-secret: SDMX_SOURCES is a comma-separated subset of
// the supported institutions (default all), SDMX_START_PERIOD / SDMX_END_PERIOD
// widen or narrow the observation window. The endpoints are anonymous, so no key
// is read here.
func LoadSDMX() (SDMX, error) {
	cfg := SDMX{
		Sources: append([]string(nil), defaultSDMXSources...),
		Start:   getenv("SDMX_START_PERIOD", ""),
		End:     getenv("SDMX_END_PERIOD", ""),
	}
	if raw := strings.TrimSpace(getenv("SDMX_SOURCES", "")); raw != "" {
		var selected []string
		seen := make(map[string]struct{})
		for _, part := range strings.Split(raw, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			if _, ok := knownSDMXSources[name]; !ok {
				return SDMX{}, fmt.Errorf("config: SDMX_SOURCES has unknown source %q", name)
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			selected = append(selected, name)
		}
		if len(selected) == 0 {
			return SDMX{}, fmt.Errorf("config: SDMX_SOURCES is set but lists no known source")
		}
		cfg.Sources = selected
	}
	return cfg, nil
}
