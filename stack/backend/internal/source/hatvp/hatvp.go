// Package hatvp ingests the Haute Autorite pour la transparence de la vie
// publique (HATVP) open data into the fact-check corpus through the
// source-connector framework. The HATVP publishes, for every covered official, a
// declaration of interests (and of activities): the mandates, professional
// activities, corporate roles and financial holdings the official declared. The
// connector diffs the open-data CSV index and, for each new or changed published
// declaration, fetches that declaration's XML and renders a structured attributed
// French summary passage published as the generic connector.EvidenceJob, drained
// by the generic evidence worker (cmd/evidenceworker) into evidence_chunks. A "the
// minister declared X" or "official Y sits on board Z" claim is then checked
// against the primary declaration.
//
// # Index diffing, per-declaration detail
//
// The CSV index (liste.csv) is one small file listing every declaration with its
// official, mandate, type, dates, nominative-page URL, and the declaration XML
// file name. A conditional GET skips the whole index when it is unchanged; the
// manifest diff then republishes only the declarations whose index row moved, and
// only those fetch their (small) per-declaration XML - so a daily run does bounded
// work. MaxItems bounds a backfill run to a starter slice.
//
// # Verified format only
//
// The wire formats are captured from the real feeds before the parsers are
// written (testdata/liste.csv and the per-declaration XML files are real excerpts
// of https://www.hatvp.fr/livraison/opendata/liste.csv and
// https://www.hatvp.fr/livraison/dossiers/<file>.xml). Private fields the HATVP
// withholds are served as the sentinel "[Données non publiées]" and are dropped.
// See docs/fact-check-sources.md for the licence and reuse conditions.
package hatvp

import (
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
)

// Source is the connector name and the evidence Source every record lands under.
const Source = "hatvp"

// Feed endpoints. IndexURL is the open-data CSV index; DossierBaseURL is the base
// the per-declaration XML files hang off (joined with the row's open_data file
// name). Both are keyless open data.
const (
	IndexURL       = "https://www.hatvp.fr/livraison/opendata/liste.csv"
	DossierBaseURL = "https://www.hatvp.fr/livraison/dossiers"
	// nominativeBaseURL prefixes the index's relative url_dossier into an absolute
	// provenance link to the official's HATVP nominative page.
	nominativeBaseURL = "https://www.hatvp.fr"
)

// withheldSentinel is the placeholder the HATVP serves for a private field; it is
// treated as empty so a withheld value never renders into a passage.
const withheldSentinel = "[Données non publiées]"

// statutLivree marks an index row whose declaration file has been delivered and is
// available to fetch; a row in any other state has no XML to ingest yet.
const statutLivree = "Livrée"

// indexRow is one parsed CSV index row: the official's identity and mandate, the
// declaration type and dates, the nominative-page URL, and the declaration XML
// file name.
type indexRow struct {
	Civilite        string
	Prenom          string
	Nom             string
	Classement      string
	TypeMandat      string
	Qualite         string
	TypeDocument    string
	Departement     string
	DatePublication string
	DateDepot       string
	URLDossier      string
	OpenDataFile    string
	Statut          string
}

// fingerprint is the index-row content digest the manifest diffs on: any change
// to the row (a new publication date, a corrected qualite) republishes the
// declaration.
func (r indexRow) fingerprint() string {
	return evidencesrc.Fingerprint(r.OpenDataFile, r.DatePublication, r.DateDepot, r.Qualite, r.Statut)
}

// parseIndex reads the semicolon-delimited CSV index, returning the rows that
// name a delivered declaration XML file. A row without an open_data file, or not
// yet delivered, is skipped: there is nothing to fetch for it.
func parseIndex(r io.Reader) ([]indexRow, error) {
	cr := csv.NewReader(r)
	cr.Comma = ';'
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("hatvp: read index header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	get := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	var rows []indexRow
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("hatvp: read index row: %w", err)
		}
		row := indexRow{
			Civilite:        get(rec, "civilite"),
			Prenom:          get(rec, "prenom"),
			Nom:             get(rec, "nom"),
			Classement:      get(rec, "classement"),
			TypeMandat:      get(rec, "type_mandat"),
			Qualite:         get(rec, "qualite"),
			TypeDocument:    get(rec, "type_document"),
			Departement:     get(rec, "departement"),
			DatePublication: get(rec, "date_publication"),
			DateDepot:       get(rec, "date_depot"),
			URLDossier:      get(rec, "url_dossier"),
			OpenDataFile:    get(rec, "open_data"),
			Statut:          get(rec, "statut_publication"),
		}
		if row.OpenDataFile == "" || row.Statut != statutLivree {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// declaration is the subset of one per-declaration XML this connector reads,
// matching the verified live shape. Every interest section is an items wrapper
// with a neant ("nothing to declare") flag; a section with neant true renders no
// line.
type declaration struct {
	UUID                    string       `xml:"uuid"`
	DateDepot               string       `xml:"dateDepot"`
	ActivProfCinqDerniere   itemSection  `xml:"activProfCinqDerniereDto"`
	ActivProfConjoint       itemSection  `xml:"activProfConjointDto"`
	FonctionBenevole        itemSection  `xml:"fonctionBenevoleDto"`
	MandatElectif           itemSection  `xml:"mandatElectifDto"`
	ParticipationDirigeant  itemSection  `xml:"participationDirigeantDto"`
	ParticipationFinanciere itemSection  `xml:"participationFinanciereDto"`
	ObservationInteret      itemSection  `xml:"observationInteretDto"`
	General                 generalBlock `xml:"general"`
}

// itemSection is the shared shape of every HATVP interest section: a neant flag
// and a nested items/items list. The item carries the union of the fields the
// several sections use; a section only populates its own, and XML unmarshal leaves
// the rest empty.
type itemSection struct {
	Neant bool   `xml:"neant"`
	Items []item `xml:"items>items"`
}

// item is one declared interest across the sections (a past profession, a
// spouse's activity, an elective mandate, a corporate role, a financial holding,
// or a free-text observation).
type item struct {
	Description       string `xml:"description"`
	Employeur         string `xml:"employeur"`
	ActiviteProf      string `xml:"activiteProf"`
	EmployeurConjoint string `xml:"employeurConjoint"`
	DescriptionMandat string `xml:"descriptionMandat"`
	NomSociete        string `xml:"nomSociete"`
	Activite          string `xml:"activite"`
	Evaluation        string `xml:"evaluation"`
	NombreParts       string `xml:"nombreParts"`
	Contenu           string `xml:"contenu"`
	DateDebut         string `xml:"dateDebut"`
	DateFin           string `xml:"dateFin"`
}

// generalBlock is the declaration's identity: the declarant, the declaration
// type, and the mandate it covers.
type generalBlock struct {
	TypeDeclaration struct {
		ID    string `xml:"id"`
		Label string `xml:"label"`
	} `xml:"typeDeclaration"`
	Mandat struct {
		Label string `xml:"label"`
	} `xml:"mandat"`
	QualiteMandat struct {
		LabelTypeMandat string `xml:"labelTypeMandat"`
		LabelOrgane     string `xml:"labelOrgane"`
	} `xml:"qualiteMandat"`
	QualiteDeclarant string `xml:"qualiteDeclarant"`
	DateDebutMandat  string `xml:"dateDebutMandat"`
	DateFinMandat    string `xml:"dateFinMandat"`
	Declarant        struct {
		Civilite string `xml:"civilite"`
		Nom      string `xml:"nom"`
		Prenom   string `xml:"prenom"`
	} `xml:"declarant"`
}

// parseDeclaration decodes one per-declaration XML file.
func parseDeclaration(data []byte) (declaration, error) {
	var d declaration
	if err := xml.Unmarshal(data, &d); err != nil {
		return declaration{}, fmt.Errorf("hatvp: decode declaration: %w", err)
	}
	return d, nil
}

// buildRecord renders an index row and its declaration into one evidence record.
// The passage names the official, the declaration type and mandate, then
// summarizes each non-empty interest section; the nominative page is the
// provenance link.
func buildRecord(row indexRow, decl declaration) evidencesrc.Record {
	title := declarationTitle(row, decl)
	url := absoluteDossierURL(row.URLDossier)
	return evidencesrc.BuildRecord(Source, row.OpenDataFile, title, url, render(row, decl), metadata(row, decl))
}

// declarationTitle names the passage for citation display.
func declarationTitle(row indexRow, decl declaration) string {
	name := officialName(row, decl)
	typ := firstNonEmpty(decl.General.TypeDeclaration.Label, typeDocumentLabel(row.TypeDocument))
	if typ == "" {
		return "Déclaration HATVP - " + name
	}
	return typ + " - " + name
}

// render builds the structured attributed French passage.
func render(row indexRow, decl declaration) string {
	var b strings.Builder
	name := officialName(row, decl)
	b.WriteString(firstNonEmpty(decl.General.TypeDeclaration.Label, typeDocumentLabel(row.TypeDocument), "Déclaration HATVP"))
	b.WriteString(" de ")
	b.WriteString(name)
	if q := firstNonEmpty(clean(row.Qualite), clean(decl.General.QualiteDeclarant), clean(decl.General.Mandat.Label)); q != "" {
		b.WriteString(", ")
		b.WriteString(q)
	}
	b.WriteString(".")
	if d := firstNonEmpty(row.DatePublication, decl.DateDepot); d != "" {
		b.WriteString(" Publiée le ")
		b.WriteString(d)
		b.WriteString(".")
	}

	writeSection(&b, "Activités professionnelles (cinq dernières années)", decl.ActivProfCinqDerniere, func(it item) string {
		return joinParts(clean(it.Description), employerClause(it.Employeur), dateRange(it.DateDebut, it.DateFin))
	})
	writeSection(&b, "Activité professionnelle du conjoint", decl.ActivProfConjoint, func(it item) string {
		return joinParts(clean(it.ActiviteProf), employerClause(it.EmployeurConjoint))
	})
	writeSection(&b, "Fonctions bénévoles", decl.FonctionBenevole, func(it item) string {
		return joinParts(clean(it.Description), clean(it.NomSociete))
	})
	writeSection(&b, "Mandats électifs", decl.MandatElectif, func(it item) string {
		return joinParts(clean(it.DescriptionMandat), dateRange(it.DateDebut, it.DateFin))
	})
	writeSection(&b, "Participations à des organes dirigeants", decl.ParticipationDirigeant, func(it item) string {
		return joinParts(clean(it.NomSociete), clean(it.Activite), dateRange(it.DateDebut, it.DateFin))
	})
	writeSection(&b, "Participations financières", decl.ParticipationFinanciere, func(it item) string {
		return joinParts(clean(it.NomSociete), evaluationClause(it.Evaluation))
	})
	writeSection(&b, "Observations", decl.ObservationInteret, func(it item) string {
		return clean(it.Contenu)
	})
	return strings.TrimSpace(b.String())
}

// writeSection appends a section only when it has declarable content. A section
// flagged neant renders a single explicit "néant" line so the checkable fact that
// nothing was declared is itself in the corpus.
func writeSection(b *strings.Builder, label string, sec itemSection, line func(item) string) {
	if sec.Neant {
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" : néant.")
		return
	}
	parts := make([]string, 0, len(sec.Items))
	for _, it := range sec.Items {
		if s := strings.TrimSpace(line(it)); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return
	}
	b.WriteString(" ")
	b.WriteString(label)
	b.WriteString(" : ")
	b.WriteString(strings.Join(parts, " ; "))
	b.WriteString(".")
}

// metadata renders the source-specific provenance carried verbatim as jsonb.
func metadata(row indexRow, decl declaration) map[string]any {
	meta := make(map[string]any)
	evidencesrc.PutMeta(meta, "type_declaration", firstNonEmpty(decl.General.TypeDeclaration.ID, row.TypeDocument))
	evidencesrc.PutMeta(meta, "nom", clean(firstNonEmpty(row.Nom, decl.General.Declarant.Nom)))
	evidencesrc.PutMeta(meta, "prenom", clean(firstNonEmpty(row.Prenom, decl.General.Declarant.Prenom)))
	evidencesrc.PutMeta(meta, "qualite", clean(firstNonEmpty(row.Qualite, decl.General.QualiteDeclarant)))
	evidencesrc.PutMeta(meta, "type_mandat", clean(firstNonEmpty(row.TypeMandat, decl.General.QualiteMandat.LabelTypeMandat)))
	evidencesrc.PutMeta(meta, "departement", row.Departement)
	evidencesrc.PutMeta(meta, "date_publication", row.DatePublication)
	evidencesrc.PutMeta(meta, "date_depot", firstNonEmpty(row.DateDepot, decl.DateDepot))
	evidencesrc.PutMeta(meta, "uuid", decl.UUID)
	return meta
}

// officialName is the declarant's display name, preferring the index's cased
// name and falling back to the XML declarant.
func officialName(row indexRow, decl declaration) string {
	prenom := firstNonEmpty(clean(row.Prenom), clean(decl.General.Declarant.Prenom))
	nom := firstNonEmpty(clean(row.Nom), clean(decl.General.Declarant.Nom))
	name := strings.TrimSpace(prenom + " " + nom)
	if civ := clean(row.Civilite); civ != "" && name != "" {
		return civ + " " + name
	}
	if name == "" {
		return "déclarant HATVP"
	}
	return name
}

// typeDocumentLabel maps an index type_document code to its French label.
func typeDocumentLabel(code string) string {
	switch strings.ToLower(code) {
	case "di":
		return "Déclaration d'intérêts"
	case "dia":
		return "Déclaration d'intérêts et d'activités"
	case "diam":
		return "Déclaration d'intérêts et d'activités modificative"
	case "dim":
		return "Déclaration d'intérêts modificative"
	case "dsp":
		return "Déclaration de situation patrimoniale"
	case "dspm":
		return "Déclaration de situation patrimoniale modificative"
	default:
		return ""
	}
}

// absoluteDossierURL turns the index's relative url_dossier into an absolute
// nominative-page link, leaving an already-absolute value untouched.
func absoluteDossierURL(rel string) string {
	rel = strings.TrimSpace(rel)
	switch {
	case rel == "":
		return nominativeBaseURL
	case strings.HasPrefix(rel, "http://"), strings.HasPrefix(rel, "https://"):
		return rel
	case strings.HasPrefix(rel, "/"):
		return nominativeBaseURL + rel
	default:
		return nominativeBaseURL + "/" + rel
	}
}

// dossierURL builds the per-declaration XML URL from the base and the row's file.
func dossierURL(base, file string) string {
	return strings.TrimRight(base, "/") + "/" + file
}

// clean blanks the HATVP withheld-field sentinel and trims whitespace, so a
// private value never renders.
func clean(s string) string {
	s = strings.TrimSpace(s)
	if s == withheldSentinel {
		return ""
	}
	return s
}

// employerClause renders an employer as a parenthetical, dropping a withheld one.
func employerClause(employer string) string {
	if e := clean(employer); e != "" {
		return "employeur : " + e
	}
	return ""
}

// evaluationClause renders a financial evaluation in euros when present.
func evaluationClause(eval string) string {
	if e := clean(eval); e != "" {
		return "évaluation " + e + " €"
	}
	return ""
}

// dateRange renders a start-end span, either bound optional.
func dateRange(start, end string) string {
	s, e := clean(start), clean(end)
	switch {
	case s != "" && e != "":
		return s + "–" + e
	case s != "":
		return "depuis " + s
	case e != "":
		return "jusqu'à " + e
	default:
		return ""
	}
}

// joinParts joins the non-empty parts with ", ".
func joinParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, ", ")
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
