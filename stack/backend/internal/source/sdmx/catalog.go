package sdmx

import "time"

// This catalog declares the institutions the generic SDMX client currently
// ingests and their curated, politically-relevant series. Endpoint flavors and
// series identifiers were verified July 2026 against the ECB Data Portal and OECD
// SDMX API help pages (see package doc). SDMX-CSV parsing is stable and covered by
// fixtures; the exact series keys are curated data selection, so an operator
// re-confirms them on the first live dev-account run (a wrong key surfaces as an
// empty series or a 404 in the run log and is fixed by editing the key here, no
// pipeline change). IMF is excluded until its commercial-reuse terms are cleared;
// Banque de France Webstat is deferred until its gateway API key and exact
// endpoint template are registered (see docs/fact-check-sources.md).

// Window bounds the observation period applied to a curated run. An empty field
// leaves the spec's own default in place, so the operator can widen or narrow the
// window (SDMX_START_PERIOD / SDMX_END_PERIOD) without editing the catalog.
type Window struct {
	Start string
	End   string
}

// apply overlays w onto each spec's period bounds, keeping the spec default where
// w leaves a field empty. It copies so the package-level curated slices are never
// mutated by a run's window.
func (w Window) apply(specs []Spec) []Spec {
	out := make([]Spec, len(specs))
	copy(out, specs)
	for i := range out {
		if w.Start != "" {
			out[i].StartPeriod = w.Start
		}
		if w.End != "" {
			out[i].EndPeriod = w.End
		}
	}
	return out
}

// ecbDefaultStart is the default first period for the monthly ECB series; a
// decade of monthly observations is enough context for the verifier to catch a
// cherry-picked figure without an oversized query.
const ecbDefaultStart = "2015-01"

// ECBEndpoint is the anonymous ECB Data Portal SDMX-CSV endpoint. No key, no
// documented rate limit; a gentle spacing keeps the fleet a good citizen.
func ECBEndpoint() Endpoint {
	return Endpoint{
		SourceName:  "Banque centrale européenne (BCE)",
		BaseURL:     "https://data-api.ecb.europa.eu/service",
		Format:      "csvdata",
		MinInterval: 2 * time.Second,
	}
}

// ecbSpecs is the curated ECB series set: euro-area consumer-price inflation and
// the French long-term sovereign borrowing rate - the two macro figures most
// argued over in French/EU fiscal and monetary debate.
//
// NOTE (verified July 2026): the ICP dataflow (HICP) was announced for
// replacement in Feb 2026 under Eurostat methodology changes; if a live run
// returns no observations for the ICP series, the flow ref/key must be updated to
// the successor HICP dataset. Euro-area HICP is also ingested from Eurostat
// directly (prc_hicp_manr), so inflation evidence is not solely dependent on this
// series.
var ecbSpecs = []Spec{
	{
		FlowRef:        "ICP",
		Key:            "M.U2.N.000000.4.ANR",
		StartPeriod:    ecbDefaultStart,
		Dataset:        "ICP",
		Title:          "Inflation, indice des prix à la consommation harmonisé (glissement annuel)",
		GeographyLabel: "la zone euro",
		Dimensions:     []string{"ensemble des postes"},
		Unit:           "%",
	},
	{
		FlowRef:        "IRS",
		Key:            "M.FR.L.L40.CI.0000.EUR.N.Z",
		StartPeriod:    ecbDefaultStart,
		Dataset:        "IRS",
		Title:          "Taux d'intérêt à long terme des obligations souveraines à 10 ans",
		GeographyLabel: "France",
		Dimensions:     []string{"critère de convergence de Maastricht"},
		Unit:           "%",
	},
}

// ECBSpecs returns the curated ECB series for window.
func ECBSpecs(window Window) []Spec { return window.apply(ecbSpecs) }

// eurostatDefaultStart is the default first period for the Eurostat macro series.
// The annual series (debt, unemployment) use a year; the monthly HICP series uses
// a month - both parse to a valid chunk index, so a single default start covering
// the last decade is applied and each series' own frequency governs the period
// granularity in the response.
const eurostatDefaultStart = "2014"

// EurostatEndpoint is the anonymous Eurostat SDMX 2.1 dissemination endpoint, used
// here through the generic client for the expanded macro series (inflation, debt,
// unemployment) that sit beside the immigration series statsingest already ingests
// under the same "eurostat" corpus. The data path is uniform (/data/{dataset}/{key})
// and SDMX-CSV is requested with Eurostat's own format token. The dedicated
// internal/stats/eurostat adapter keeps the asynchronous-extraction path for its
// large bulk queries; these curated single-series queries stay well under the
// synchronous cell limit, so the generic client needs no async handling.
func EurostatEndpoint() Endpoint {
	return Endpoint{
		SourceName:  "Eurostat",
		BaseURL:     "https://ec.europa.eu/eurostat/api/dissemination/sdmx/2.1",
		Format:      "SDMX-CSV",
		MinInterval: time.Second,
	}
}

// eurostatSpecs is the curated expanded Eurostat macro set: euro-area/France
// consumer-price inflation, general-government gross debt, and the unemployment
// rate - the fiscal-and-price backbone of French/EU budget debate.
//
// NOTE (verified July 2026 against the Eurostat data-browser DSDs): dimension keys
// follow each dataset's published DSD order (resolved per dataset, not position);
// re-confirmed on the first live run since a wrong key surfaces as an empty series.
var eurostatSpecs = []Spec{
	{
		FlowRef:        "prc_hicp_manr",
		Key:            "M.RCH_A.CP00.FR",
		StartPeriod:    eurostatDefaultStart,
		Dataset:        "PRC_HICP_MANR",
		Title:          "Inflation, indice des prix à la consommation harmonisé (taux annuel)",
		GeographyLabel: "France",
		Dimensions:     []string{"ensemble des postes"},
		Unit:           "%",
	},
	{
		FlowRef:        "gov_10dd_edpt1",
		Key:            "A.PC_GDP.S13.GD.FR",
		StartPeriod:    eurostatDefaultStart,
		Dataset:        "GOV_10DD_EDPT1",
		Title:          "Dette publique brute des administrations publiques",
		GeographyLabel: "France",
		Dimensions:     []string{"en pourcentage du PIB"},
		Unit:           "%",
	},
	{
		FlowRef:        "une_rt_a",
		Key:            "A.TOTAL.PC_ACT.T.FR",
		StartPeriod:    eurostatDefaultStart,
		Dataset:        "UNE_RT_A",
		Title:          "Taux de chômage",
		GeographyLabel: "France",
		Dimensions:     []string{"ensemble", "tous âges"},
		Unit:           "%",
	},
}

// EurostatSpecs returns the curated expanded Eurostat macro series for window.
func EurostatSpecs(window Window) []Spec { return window.apply(eurostatSpecs) }

// oecdDefaultStart is the default first period for the monthly OECD series.
const oecdDefaultStart = "2015-01"

// oecdMinInterval enforces the OECD API's documented 60-requests-per-hour fair-use
// limit (one request per minute) so a curated run never trips a throttle. A
// handful of series keeps the whole run well inside a few minutes.
const oecdMinInterval = time.Minute

// OECDEndpoint is the anonymous OECD SDMX-CSV endpoint. The 60/hour rate limit is
// enforced client-side; csvfilewithlabels returns flat rows with label columns,
// all resolved by header name.
func OECDEndpoint() Endpoint {
	return Endpoint{
		SourceName:  "OCDE (Organisation de coopération et de développement économiques)",
		BaseURL:     "https://sdmx.oecd.org/public/rest",
		Format:      "csvfilewithlabels",
		MinInterval: oecdMinInterval,
	}
}

// oecdSpecs is the curated OECD series set: the harmonized unemployment rate for
// France, an internationally comparable labor-market figure often cited in
// cross-country political comparison.
//
// NOTE (verified July 2026): OECD migrated many legacy .Stat dataflow ids during
// the Data Explorer transition; the flow-ref triple and key here follow the
// current DSD_LFS unemployment dataflow and are re-confirmed on the first live
// run.
var oecdSpecs = []Spec{
	{
		FlowRef:        "OECD.SDD.TPS,DSD_LFS@DF_IALFS_UNE_M,1.0",
		Key:            "FRA...._Z.Y._T.Y_GE15.M",
		StartPeriod:    oecdDefaultStart,
		Dataset:        "DSD_LFS@DF_IALFS_UNE_M",
		Title:          "Taux de chômage harmonisé",
		GeographyLabel: "France",
		Dimensions:     []string{"ensemble", "15 ans et plus"},
		Unit:           "%",
	},
}

// OECDSpecs returns the curated OECD series for window.
func OECDSpecs(window Window) []Spec { return window.apply(oecdSpecs) }
