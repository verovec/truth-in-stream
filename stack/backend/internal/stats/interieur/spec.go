package interieur

// Spec is one interior-ministry open-data CSV resource to ingest: the stable
// download URL, the implicit reporting year, the column that carries the figure,
// the column that uniquely identifies a row within the resource (the provenance
// series key), and the breakdown columns the rendered sentence weaves in. A spec
// maps one resource so a re-run upserts the same rows under stable keys.
type Spec struct {
	// URL is the stable per-resource download URL on data.gouv.fr (the
	// /api/1/datasets/r/<uuid> permalink, which redirects to the current file).
	// It is also the provenance citation stored on every datapoint.
	URL string
	// Dataset is a stable slug for the source dataset, e.g.
	// "titres-de-sejour-2023". With SeriesKey it forms the idempotency key.
	Dataset string
	// Year is the reporting year the resource covers (the resource has no year
	// column), used as the datapoint period, e.g. "2023".
	Year string
	// Title is the French series label used as the passage title, e.g.
	// "Premiers titres de séjour délivrés".
	Title string
	// ValueColumn is the header of the count column, e.g. "nb_titres" or
	// "nb_demandes".
	ValueColumn string
	// KeyColumn is the header whose value uniquely identifies a row within the
	// resource (e.g. "code_iso3"); it becomes the datapoint series key so each
	// row occupies a distinct provenance row.
	KeyColumn string
	// DimensionColumns are the headers whose values are woven into the rendered
	// sentence as the French breakdown labels, in order (e.g. "pays_nationalite").
	DimensionColumns []string
	// Unit is the French unit label rendered after the figure, e.g. "personnes"
	// for permits or "demandes" for asylum applications.
	Unit string
}

// Curated resource permalinks verified 2026-06-19 on data.gouv.fr. The
// /api/1/datasets/r/<uuid> form is the stable permalink that redirects to the
// current static file, so it survives a dataset revision.
const (
	permitsByCountryURL = "https://www.data.gouv.fr/api/1/datasets/r/c2cd00ad-b43f-4bee-87dd-8c52991e4dc8"
	asylumByCountryURL  = "https://www.data.gouv.fr/api/1/datasets/r/3498c9a4-fc48-4e8b-a7a2-515bcb2b23fa"
)

// PermitsByCountry2023 is first residence permits issued in France in 2023,
// broken down by country of nationality (DSED, dataset
// "stock-et-flux-des-titres-de-sejour-en-france-sur-lannee-2023").
var PermitsByCountry2023 = Spec{
	URL:              permitsByCountryURL,
	Dataset:          "titres-de-sejour-2023",
	Year:             "2023",
	Title:            "Premiers titres de séjour délivrés",
	ValueColumn:      "nb_titres",
	KeyColumn:        "code_iso3",
	DimensionColumns: []string{"pays_nationalite"},
	Unit:             "personnes",
}

// AsylumByCountry2024 is asylum applications lodged in France in 2024, broken
// down by country of origin (dataset
// "demandes-dasile-et-transferts-dublin-en-france-sur-lannee-2024").
var AsylumByCountry2024 = Spec{
	URL:              asylumByCountryURL,
	Dataset:          "demandes-asile-2024",
	Year:             "2024",
	Title:            "Demandes d'asile",
	ValueColumn:      "nb_demandes",
	KeyColumn:        "code_iso3",
	DimensionColumns: []string{"pays_nationalite"},
	Unit:             "demandes",
}

// CuratedSpecs is the default set the offline ingest pulls: residence permits
// and asylum applications by country, the national-source half of the motivating
// immigration claim.
var CuratedSpecs = []Spec{PermitsByCountry2023, AsylumByCountry2024}
