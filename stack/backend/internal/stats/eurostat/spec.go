package eurostat

// Spec is one curated Eurostat series to ingest: a dataset, a dot-notation
// dimension key, a period window, and the French labels the rendered passage
// carries. A spec maps a single, fully-resolved series so the synchronous query
// stays well under the cell limit and the rendered sentence is unambiguous.
type Spec struct {
	// Dataset is the Eurostat dataset code, e.g. "MIGR_RESFIRST".
	Dataset string
	// Key is the dot-notation dimension key in the dataset's DSD order; an
	// empty segment is a wildcard. Geo is the last dimension in these datasets.
	Key string
	// StartPeriod and EndPeriod bound the query (inclusive), e.g. "2014".."2023".
	StartPeriod string
	EndPeriod   string
	// Title is the French series label used as the passage title.
	Title string
	// GeographyLabel is the fallback French geography when a row's geo code is
	// unmapped; rows whose geo code is known are labeled from geoLabels.
	GeographyLabel string
	// Dimensions are the French breakdown labels (citizenship, age, ...) the
	// rendered sentence weaves in, in a stable order.
	Dimensions []string
	// Unit is the French unit label, e.g. "personnes" or "%".
	Unit string
}

// geoLabels maps the geo dimension codes the curated specs touch to their
// French country names. Unmapped codes fall back to Spec.GeographyLabel.
var geoLabels = map[string]string{
	"FR":        "France",
	"DE":        "Allemagne",
	"IT":        "Italie",
	"ES":        "Espagne",
	"EU27_2020": "l'Union européenne",
}

// ResidencePermitsFR is first residence permits issued in France, all reasons
// and citizenships, annual. MIGR_RESFIRST DSD order: freq.reason.citizen.duration.unit.geo.
var ResidencePermitsFR = Spec{
	Dataset:        "MIGR_RESFIRST",
	Key:            "A.TOTAL.TOTAL.TOTAL.PER.FR",
	StartPeriod:    "2014",
	EndPeriod:      "2024",
	Title:          "Premiers titres de séjour délivrés",
	GeographyLabel: "France",
	Dimensions:     []string{"toutes nationalités", "tous motifs"},
	Unit:           "personnes",
}

// ActivityRateForeignFR is the activity rate of foreign citizens aged 15-64 in
// France, both sexes, annual, in percent. LFSA_ARGAN DSD order:
// freq.unit.sex.age.citizen.geo.
var ActivityRateForeignFR = Spec{
	Dataset:        "LFSA_ARGAN",
	Key:            "A.PC.T.Y15-64.FOR.FR",
	StartPeriod:    "2014",
	EndPeriod:      "2024",
	Title:          "Taux d'activité",
	GeographyLabel: "France",
	Dimensions:     []string{"ressortissants étrangers", "15 à 64 ans", "ensemble des sexes"},
	Unit:           "%",
}

// CuratedSpecs is the default set the offline ingest pulls: both halves of the
// motivating immigration claim (permit inflows and immigrant labor-market
// participation) for France.
var CuratedSpecs = []Spec{ResidencePermitsFR, ActivityRateForeignFR}
