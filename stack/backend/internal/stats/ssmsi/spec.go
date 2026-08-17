package ssmsi

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Spec is one SSMSI delinquency base to ingest: the data.gouv.fr dataset slug, the
// resource-title substring that selects the CSV for a territorial level, a stable
// provenance slug, and the geography column and label for that level. The
// departmental and regional bases share the schema and differ only in the geography
// column, so one Spec per level covers them.
type Spec struct {
	// DatasetSlug is the stable data.gouv.fr dataset slug the current CSV resource
	// is resolved through, so a rotated file name is followed automatically.
	DatasetSlug string
	// ResourceMatch is the case-insensitive substring of the resource title that
	// selects the CSV for this level (e.g. "épartement", "égional").
	ResourceMatch string
	// Dataset is a stable slug stamped on every datapoint for provenance; with the
	// series key it forms the idempotency key.
	Dataset string
	// GeographyColumn is the header carrying the territory code for this level
	// ("Code_departement" or "Code_region").
	GeographyColumn string
	// GeographyLabel is the French level noun prefixed to the code in the rendered
	// geography (e.g. "département", "région").
	GeographyLabel string
}

// validate rejects a spec that cannot resolve or map a base.
func (s Spec) validate() error {
	switch {
	case s.DatasetSlug == "":
		return fmt.Errorf("spec: empty dataset slug")
	case s.ResourceMatch == "":
		return fmt.Errorf("spec %q: empty resource match", s.Dataset)
	case s.Dataset == "":
		return fmt.Errorf("spec: empty dataset")
	case s.GeographyColumn == "":
		return fmt.Errorf("spec %q: empty geography column", s.Dataset)
	case s.GeographyLabel == "":
		return fmt.Errorf("spec %q: empty geography label", s.Dataset)
	}
	return nil
}

// delinquencyDatasetSlug is the SSMSI communal/departmental/regional delinquency
// base on data.gouv.fr (verified 2026-07). The départemental and régional CSV
// resources are the aggregated levels this connector ingests; the communal base
// ships only gzip/parquet and is left to a later card.
const delinquencyDatasetSlug = "bases-statistiques-communale-departementale-et-regionale-de-la-delinquance-enregistree-par-la-police-et-la-gendarmerie-nationales"

// DepartmentalBase is the départemental delinquency series.
var DepartmentalBase = Spec{
	DatasetSlug:     delinquencyDatasetSlug,
	ResourceMatch:   "épartement",
	Dataset:         "delinquance-departementale",
	GeographyColumn: "Code_departement",
	GeographyLabel:  "département",
}

// RegionalBase is the régional delinquency series.
var RegionalBase = Spec{
	DatasetSlug:     delinquencyDatasetSlug,
	ResourceMatch:   "égion",
	Dataset:         "delinquance-regionale",
	GeographyColumn: "Code_region",
	GeographyLabel:  "région",
}

// CuratedSpecs is the default set cmd/odsingest sweeps: the départemental and
// régional delinquency bases.
var CuratedSpecs = []Spec{DepartmentalBase, RegionalBase}

// Source adapts a Client and a set of specs to the stats.Source contract: it
// downloads every base and concatenates the datapoints, so the source-agnostic
// stats foundation ingests the SSMSI series in one run. A failure for any base
// fails the run (wrapped), so a partial corpus is never committed.
type Source struct {
	client *Client
	specs  []Spec
}

// NewSource builds a Source over client and specs. With nil specs it uses
// CuratedSpecs.
func NewSource(client *Client, specs []Spec) *Source {
	if specs == nil {
		specs = CuratedSpecs
	}
	return &Source{client: client, specs: specs}
}

// Corpus is the SSMSI security-statistics corpus label.
func (s *Source) Corpus() string { return domain.SSMSIStatCorpus }

// Datapoints downloads every spec in order and returns the combined datapoints.
func (s *Source) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	var all []domain.Datapoint
	for _, spec := range s.specs {
		dps, err := s.client.Fetch(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("ssmsi: fetch %s: %w", spec.Dataset, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
