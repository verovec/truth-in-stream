package config

import (
	"fmt"
	"math"
)

// defaultParliamentLegislature is the current Assemblee Nationale legislature; an
// operator backfills an older one by setting PARLIAMENT_LEGISLATURE=16, so the
// volume-controlled default is always the current legislature (the card's "start
// with the current legislature; older is an explicit choice"). The Senat rolling
// exports ignore it.
const defaultParliamentLegislature = "17"

// Parliament configures a parliament open-data producer run. Dataset selects the
// dataset family (validated by internal/source/parliament); Legislature is
// interpolated into an AN dump URL; SinceYear bounds the Senat scrutins to recent
// sessions (0 = every session); MaxItems bounds a run (0 = unbounded) as a backfill
// safety valve; MarkerPath and ManifestPath are the per-dataset state files (derived
// from the dataset so the fleet's several parliament sources never share one
// checkpoint). It carries no secret: the dumps are public Licence Ouverte / Senat
// open data and the broker URL loads from the queue loader.
type Parliament struct {
	Dataset      string
	Legislature  string
	SinceYear    int
	MaxItems     int
	MarkerPath   string
	ManifestPath string
}

// LoadParliament reads the parliament producer configuration for the dataset named
// by PARLIAMENT_DATASET (the source's RequiredEnv), used by the standalone
// cmd/parliamentcrawl. The always-on scheduler, which runs several parliament
// datasets in one process, calls LoadParliamentFor with each dataset instead.
func LoadParliament() (Parliament, error) {
	dataset, err := requireEnv("PARLIAMENT_DATASET")
	if err != nil {
		return Parliament{}, err
	}
	return LoadParliamentFor(dataset)
}

// LoadParliamentFor reads the parliament producer configuration for an explicit
// dataset. PARLIAMENT_LEGISLATURE defaults to 17 and must be a bare positive integer
// (it is interpolated into an AN download URL); PARLIAMENT_SINCE_YEAR and
// PARLIAMENT_MAX_ITEMS default to 0 (no bound). The marker and manifest paths are
// derived from the dataset so each of the fleet's parliament sources keeps its own
// checkpoint under the shared state volume. Bad values fail fast at startup.
func LoadParliamentFor(dataset string) (Parliament, error) {
	if dataset == "" {
		return Parliament{}, fmt.Errorf("config: parliament dataset is required")
	}
	legislature := getenv("PARLIAMENT_LEGISLATURE", defaultParliamentLegislature)
	if !scrutinsLegislatureRe.MatchString(legislature) {
		return Parliament{}, fmt.Errorf("config: PARLIAMENT_LEGISLATURE %q must be a positive integer", legislature)
	}
	sinceYear, err := intEnv("PARLIAMENT_SINCE_YEAR", 0, 0, 3000)
	if err != nil {
		return Parliament{}, err
	}
	maxItems, err := intEnv("PARLIAMENT_MAX_ITEMS", 0, 0, math.MaxInt32)
	if err != nil {
		return Parliament{}, err
	}
	return Parliament{
		Dataset:      dataset,
		Legislature:  legislature,
		SinceYear:    sinceYear,
		MaxItems:     maxItems,
		MarkerPath:   fmt.Sprintf("/state/parliament-%s-marker.json", dataset),
		ManifestPath: fmt.Sprintf("/state/parliament-%s-manifest.json", dataset),
	}, nil
}
