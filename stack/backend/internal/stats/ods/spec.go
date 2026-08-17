package ods

import (
	"context"
	"fmt"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Spec is one OpenDataSoft dataset to ingest: the dataset id plus the field roles
// that map a flat record to a domain.Datapoint. Fields are resolved by name so a
// dataset column reorder cannot silently misread the figure; a missing configured
// field is schema drift the client fails loudly on.
type Spec struct {
	// Dataset is the portal's stable Explore dataset id (e.g. "cns_financement").
	// With the record's series key it forms the idempotency key.
	Dataset string
	// Title is the French series label used as the passage title.
	Title string
	// ValueField is the numeric field carrying the figure (e.g. "montants").
	ValueField string
	// KeyField is the field whose value identifies a series within the dataset
	// (e.g. "poste_code"); it seeds the datapoint series key so each row occupies a
	// distinct provenance row. Dimension fields are folded in after it.
	KeyField string
	// PeriodField is the field carrying the observation period (e.g. "annee" or a
	// bare-year "date"). When empty the dataset has no period column and Year is
	// used for every row. It must resolve to a "YYYY" year; a quarter is composed
	// from QuarterField, so a raw full ISO date is never passed through (the domain
	// period parser rejects it).
	PeriodField string
	// QuarterField, when set, carries the quarter number (1..4) that is combined
	// with PeriodField's year into a "YYYY-Qn" period, so a quarterly dataset does
	// not collapse its four quarters onto one annual provenance row.
	QuarterField string
	// Year is the fixed period when the dataset has no PeriodField.
	Year string
	// GeographyField is the field carrying the geographic area. When empty the
	// fixed Geography label is used for every row.
	GeographyField string
	// Geography is the fixed area label when the dataset has no GeographyField.
	Geography string
	// DimensionFields are the fields woven into the rendered sentence as French
	// breakdown labels, in order; each is also folded into the series key.
	DimensionFields []string
	// Unit is the French unit label rendered after the figure (e.g. "millions
	// d'euros", "salariés").
	Unit string
	// Where is an optional Explore API v2.1 ODSQL filter narrowing the rows (e.g.
	// `annee >= "2015"`), left empty to ingest the whole dataset.
	Where string
}

// validate rejects a spec that cannot map a record, so a misconfigured curated
// entry fails before any request rather than silently ingesting nothing.
func (s Spec) validate() error {
	switch {
	case s.Dataset == "":
		return fmt.Errorf("spec: empty dataset")
	case s.Title == "":
		return fmt.Errorf("spec %q: empty title", s.Dataset)
	case s.ValueField == "":
		return fmt.Errorf("spec %q: empty value field", s.Dataset)
	case s.KeyField == "":
		return fmt.Errorf("spec %q: empty key field", s.Dataset)
	case s.Unit == "":
		return fmt.Errorf("spec %q: empty unit", s.Dataset)
	case s.PeriodField == "" && s.Year == "":
		return fmt.Errorf("spec %q: neither period field nor fixed year", s.Dataset)
	}
	return nil
}

// selectClause is the comma-separated field list the records request narrows to,
// so a page carries only the mapped fields. Deduped and ordered for a stable URL.
func (s Spec) selectClause() string {
	seen := make(map[string]struct{})
	var fields []string
	add := func(f string) {
		if f == "" {
			return
		}
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		fields = append(fields, f)
	}
	add(s.ValueField)
	add(s.KeyField)
	add(s.PeriodField)
	add(s.QuarterField)
	add(s.GeographyField)
	for _, f := range s.DimensionFields {
		add(f)
	}
	return strings.Join(fields, ",")
}

// Portal is one OpenDataSoft institutional portal: its host, the human-readable
// publisher name and corpus label stamped on every passage, and the curated
// dataset allowlist. BaseURL overrides the host for tests; production leaves it
// empty so the public https host is used.
type Portal struct {
	Host       string
	SourceName string
	Corpus     string
	BaseURL    string
	Specs      []Spec
}

// baseURL is the scheme+host the records and dataset URLs are built on, honoring a
// test override.
func (p Portal) baseURL() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	return "https://" + p.Host
}

// Curated portals verified 2026-07 against the live Explore API v2.1 (every field
// name and value shape below was captured from a real records response). The
// dataset allowlist is a minimal political-relevance starter set per the card; each
// portal writes its own corpus so a retrieved passage's publisher is identifiable.
// Field mappings are resolved by name and a mismatch fails loudly (schema drift),
// so an operator's first live run surfaces any drift rather than corrupting the
// corpus.
const (
	dreesHost  = "data.drees.solidarites-sante.gouv.fr"
	daresHost  = "data.dares.travail-emploi.gouv.fr"
	urssafHost = "open.urssaf.fr"
)

// DREES publishes health and social-policy statistics. cns_financement is the
// comptes de la santé financing series. A row is keyed on (poste_code, financeur),
// so fin_lib (the financeur label, e.g. "Tout financeur") is folded in as a
// dimension or the four financiers of one poste+year would collide on one row.
// Real record: {annee:"2010", poste_code:"p12100_niv3", fin_lib:"Tout financeur",
// montants:738.87}.
var DREES = Portal{
	Host:       dreesHost,
	SourceName: "DREES",
	Corpus:     domain.DREESStatCorpus,
	Specs: []Spec{
		{
			Dataset:         "cns_financement",
			Title:           "Financement des dépenses de santé",
			ValueField:      "montants",
			KeyField:        "poste_code",
			PeriodField:     "annee",
			Geography:       "France",
			DimensionFields: []string{"fin_lib"},
			Unit:            "millions d'euros",
		},
	},
}

// DARES publishes labor-market statistics. dares_tempspartiel_detail_annuelles is
// the annual part-time-work detail series. The period field is a bare-year "date".
// Real record: {date:"2014", champ:"France", indicateur:"Taux de temps partiel
// (%)", indicateur_detaille:"Moins d'un mi-temps", sexe:"Total", valeur:23.6}.
var DARES = Portal{
	Host:       daresHost,
	SourceName: "DARES",
	Corpus:     domain.DARESStatCorpus,
	Specs: []Spec{
		{
			Dataset:         "dares_tempspartiel_detail_annuelles",
			Title:           "Temps partiel",
			ValueField:      "valeur",
			KeyField:        "indicateur",
			PeriodField:     "date",
			GeographyField:  "champ",
			DimensionFields: []string{"indicateur", "indicateur_detaille", "sexe"},
			Unit:            "%",
		},
	},
}

// URSSAF publishes private-sector employment by territory. The zone-d'emploi
// effectifs/masse-salariale series is quarterly: annee + trimestre compose a
// "YYYY-Qn" period so the four quarters of a zone-year do not collide. The full
// dataset is ~38k rows (over the records-window ceiling), so it is narrowed to
// recent years with a Where filter, keeping it well inside the window. Real record:
// {zone_d_emploi:"Caen", annee:"2022", trimestre:1, code_zone_d_emploi:"2804",
// effectifs_salaries_cvs:132010}.
var URSSAF = Portal{
	Host:       urssafHost,
	SourceName: "URSSAF",
	Corpus:     domain.URSSAFStatCorpus,
	Specs: []Spec{
		{
			Dataset:        "effectifs-salaries-et-masse-salariale-du-secteur-prive-par-zone-demploi",
			Title:          "Effectifs salariés du secteur privé (CVS)",
			ValueField:     "effectifs_salaries_cvs",
			KeyField:       "code_zone_d_emploi",
			PeriodField:    "annee",
			QuarterField:   "trimestre",
			GeographyField: "zone_d_emploi",
			Where:          `annee >= "2022"`,
			Unit:           "salariés",
		},
	},
}

// CuratedPortals is the default set cmd/odsingest sweeps: the three institutional
// portals sharing the Explore API v2.1.
var CuratedPortals = []Portal{DREES, DARES, URSSAF}

// Source adapts a Client and one Portal to the stats.Source contract: it fetches
// every curated dataset of the portal and concatenates the datapoints, so the
// source-agnostic stats foundation ingests the portal in one run. A fetch failure
// for any dataset fails the run (wrapped), so a partial corpus is never committed.
type Source struct {
	client *Client
	portal Portal
}

// NewSource builds a Source over client and portal.
func NewSource(client *Client, portal Portal) *Source {
	return &Source{client: client, portal: portal}
}

// Corpus is the portal's evidence_chunks.source label.
func (s *Source) Corpus() string { return s.portal.Corpus }

// Datapoints fetches every curated dataset of the portal in order.
func (s *Source) Datapoints(ctx context.Context) ([]domain.Datapoint, error) {
	var all []domain.Datapoint
	for _, spec := range s.portal.Specs {
		dps, err := s.client.Fetch(ctx, s.portal, spec)
		if err != nil {
			return nil, fmt.Errorf("ods: %s fetch %s: %w", s.portal.SourceName, spec.Dataset, err)
		}
		all = append(all, dps...)
	}
	return all, nil
}
