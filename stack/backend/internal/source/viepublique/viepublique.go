// Package viepublique ingests the DILA "Discours publics" open-data dump from
// vie-publique.fr into the fact-check corpus through the source-connector
// framework. The dump is the metadata of ~150 000 public speeches (president,
// prime minister, ministers, Conseil des ministres communiques) since 1974: who
// spoke, when, on what, in what capacity, with a canonical vie-publique.fr link.
// Each record renders into one compact attributed French evidence passage
// published as the generic connector.EvidenceJob, drained by the generic evidence
// worker (cmd/evidenceworker) into evidence_chunks, so an "the minister said X on
// date Y" attribution claim is checked against the primary catalog.
//
// # Metadata, not full text
//
// The published dataset is metadata only: it carries the title, speaker(s), date,
// emitter, document type, themes and descriptors, and the source URL - not the
// speech body (there is no full-text field). The passage is therefore compact by
// construction, exactly what the dataset licence (Licence Ouverte / Open Licence
// v2.0, "DILA - vie-publique.fr") covers; the URL lets a reader open the full
// speech on vie-publique.fr.
//
// # Verified format only
//
// The wire format is captured from the real dump before the parser is written
// (testdata/vp_discours.json is a real excerpt of
// https://echanges.dila.gouv.fr/OPENDATA/DISCOURS_PUBLICS/vp_discours.json). A
// conditional GET (ETag/Last-Modified) short-circuits the whole download when the
// dump is unchanged; the manifest diff republishes only records whose fingerprint
// moved. See docs/fact-check-sources.md for the licence.
package viepublique

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
)

// Source is the connector name and the evidence Source every record lands under.
const Source = "viepublique"

// DumpURL is the DILA Discours publics metadata dump (a single JSON array). It is
// keyless open data served with ETag/Last-Modified, so the conditional GET skip
// works.
const DumpURL = "https://echanges.dila.gouv.fr/OPENDATA/DISCOURS_PUBLICS/vp_discours.json"

// discours is the subset of one vie-publique metadata record this connector
// reads, matching the verified live dump shape. Slice fields hold the repeated
// values (several speakers, several themes) the dump lists.
type discours struct {
	ID            string        `json:"id"`
	Titre         string        `json:"titre"`
	URL           string        `json:"url"`
	Domaine       string        `json:"domaine"`
	Prononciation string        `json:"prononciation"`
	Intervenants  []intervenant `json:"intervenants"`
	AuteurMoral   []string      `json:"auteur_moral"`
	Circonstance  string        `json:"circonstance"`
	TypeEmetteur  string        `json:"type_emetteur"`
	TypeDocument  string        `json:"type_document"`
	Resume        string        `json:"resume"`
	Thematiques   []string      `json:"thematiques"`
	Descripteurs  []string      `json:"descripteurs"`
	MiseEnLigne   string        `json:"mise_en_ligne"`
	MiseAJour     string        `json:"mise_a_jour"`
}

// intervenant is one speaker of a discours. A record with no identified speaker
// (a communique) carries a single all-null entry, which renders no speaker line.
type intervenant struct {
	Nom     string `json:"nom"`
	Qualite string `json:"qualite"`
}

// Extract streams the JSON-array dump and renders each metadata record into a
// compact attributed evidence passage. It decodes element-by-element rather than
// loading the whole (multi-hundred-MB) array into memory. A record with no id is
// skipped rather than failing the whole run, since one malformed catalog entry
// must not strand the rest.
func Extract(source, archivePath string) ([]evidencesrc.Record, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("viepublique: open dump: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(f)
	if tok, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("viepublique: read array start: %w", err)
	} else if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("viepublique: dump is not a JSON array (got %v)", tok)
	}

	var records []evidencesrc.Record
	for dec.More() {
		var d discours
		if err := dec.Decode(&d); err != nil {
			return nil, fmt.Errorf("viepublique: decode record: %w", err)
		}
		if d.ID == "" {
			continue
		}
		records = append(records, buildRecord(source, d))
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("viepublique: read array end: %w", err)
	}
	return records, nil
}

// buildRecord renders one discours into a Record. The passage names the speech,
// its speaker(s), date, emitter and themes, the checkable attribution facts; the
// vie-publique URL is the provenance link.
func buildRecord(source string, d discours) evidencesrc.Record {
	title := d.Titre
	if title == "" {
		title = "Discours public " + d.ID
	}
	return evidencesrc.BuildRecord(source, d.ID, title, d.URL, render(d), metadata(d))
}

// render builds the compact attributed French passage from the metadata.
func render(d discours) string {
	var b strings.Builder
	b.WriteString("Discours public")
	if d.Domaine != "" {
		b.WriteString(" (")
		b.WriteString(d.Domaine)
		b.WriteString(")")
	}
	b.WriteString(" : ")
	b.WriteString(strings.TrimSpace(d.Titre))
	b.WriteString(".")
	if speakers := speakerList(d.Intervenants); speakers != "" {
		b.WriteString(" Intervenant(s) : ")
		b.WriteString(speakers)
		b.WriteString(".")
	}
	if em := firstNonEmpty(d.TypeEmetteur, joinNonEmpty(d.AuteurMoral)); em != "" {
		b.WriteString(" Émetteur : ")
		b.WriteString(em)
		b.WriteString(".")
	}
	if d.Prononciation != "" {
		b.WriteString(" Prononcé le ")
		b.WriteString(d.Prononciation)
		b.WriteString(".")
	}
	if d.Circonstance != "" {
		b.WriteString(" Circonstance : ")
		b.WriteString(d.Circonstance)
		b.WriteString(".")
	}
	if th := joinNonEmpty(d.Thematiques); th != "" {
		b.WriteString(" Thématiques : ")
		b.WriteString(th)
		b.WriteString(".")
	}
	if desc := joinNonEmpty(d.Descripteurs); desc != "" {
		b.WriteString(" Mots-clés : ")
		b.WriteString(desc)
		b.WriteString(".")
	}
	if r := strings.TrimSpace(d.Resume); r != "" {
		b.WriteString(" Résumé : ")
		b.WriteString(evidencesrc.PlainText(r))
	}
	return b.String()
}

// speakerList joins the named speakers with their capacity, dropping the all-null
// entry a speaker-less communique carries.
func speakerList(items []intervenant) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		name := strings.TrimSpace(it.Nom)
		if name == "" {
			continue
		}
		if q := strings.TrimSpace(it.Qualite); q != "" {
			name += " (" + q + ")"
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// metadata renders the source-specific provenance carried verbatim as jsonb.
func metadata(d discours) map[string]any {
	meta := make(map[string]any)
	evidencesrc.PutMeta(meta, "domaine", d.Domaine)
	evidencesrc.PutMeta(meta, "type_document", d.TypeDocument)
	evidencesrc.PutMeta(meta, "type_emetteur", d.TypeEmetteur)
	evidencesrc.PutMeta(meta, "prononciation", d.Prononciation)
	evidencesrc.PutMeta(meta, "mise_a_jour", d.MiseAJour)
	if s := speakerList(d.Intervenants); s != "" {
		meta["intervenants"] = s
	}
	if len(d.Thematiques) > 0 {
		meta["thematiques"] = joinNonEmpty(d.Thematiques)
	}
	if len(d.AuteurMoral) > 0 {
		meta["auteur_moral"] = joinNonEmpty(d.AuteurMoral)
	}
	return meta
}

// joinNonEmpty joins the non-empty, trimmed entries of vals with ", ".
func joinNonEmpty(vals []string) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ", ")
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
