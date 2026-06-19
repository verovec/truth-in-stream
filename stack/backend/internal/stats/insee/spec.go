package insee

// Spec is one curated INSEE BDM series to ingest: the IDBANK identifier, a
// dataset slug, the period window, and the French labels the rendered passage
// carries. A spec maps a single, fully-resolved series so the SDMX query is
// unambiguous and the rendered sentence is self-contained.
type Spec struct {
	// IDBank is the 9-digit BDM series identifier, e.g. "010755676". It is the
	// provenance series key, distinct per indicator and immigrant-status cut.
	IDBank string
	// Dataset is a stable slug for the source survey/dataset, e.g. "EEC" (Enquête
	// Emploi en Continu). With IDBank it forms the idempotency key.
	Dataset string
	// Title is the French series label used as the passage title, e.g. "Taux
	// d'emploi".
	Title string
	// Dimensions are the French breakdown labels (immigrant status, age band)
	// the rendered sentence weaves in, in a stable order.
	Dimensions []string
	// Unit is the French unit label rendered after the figure (these are rates,
	// so "%").
	Unit string
	// StartYear bounds the query (inclusive) via startPeriod, e.g. "2014"; empty
	// fetches the full available history.
	StartYear string
}

// CuratedSpecs is the default INSEE set the offline ingest pulls: the national
// ILO unemployment-rate annual averages from the Enquête Emploi, the
// labor-market figures the immigration debate turns on.
//
// Source note (verified 2026-06-19): INSEE does NOT publish the immigrant
// vs non-immigrant labor-market breakdown as BDM time series — that breakdown
// exists only as the EEC "IMMFRA" Excel tables, which are out of scope for an
// SDMX adapter (the EU foreign-citizen activity rate the eurostat adapter
// already ingests is the machine-readable immigrant-status cut). The BDM does
// expose the national unemployment-rate series below, which corroborate the
// labor-market context. IDBANKs and annual frequency (FREQ="A", UNIT_MEASURE
// "POURCENT") verified by direct SDMX fetch against bdm.insee.fr.
var CuratedSpecs = []Spec{
	{
		IDBank:     "001787717",
		Dataset:    "TAUX-CHOMAGE-BIT",
		Title:      "Taux de chômage au sens du BIT (moyenne annuelle)",
		Dimensions: []string{"ensemble", "15 ans ou plus"},
		Unit:       "%",
		StartYear:  "2014",
	},
	{
		IDBank:     "001787720",
		Dataset:    "TAUX-CHOMAGE-BIT",
		Title:      "Taux de chômage au sens du BIT (moyenne annuelle)",
		Dimensions: []string{"15 à 24 ans"},
		Unit:       "%",
		StartYear:  "2014",
	},
	{
		IDBank:     "001787719",
		Dataset:    "TAUX-CHOMAGE-BIT",
		Title:      "Taux de chômage au sens du BIT (moyenne annuelle)",
		Dimensions: []string{"femmes", "15 ans ou plus"},
		Unit:       "%",
		StartYear:  "2014",
	},
}
