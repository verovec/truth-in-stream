package parliament

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// compteRenduURLTemplate is the stable per-record open-data locator for a seance
// compte rendu, each chunk's provenance link.
const compteRenduURLTemplate = "https://www.assemblee-nationale.fr/dyn/opendata/%s.json"

// crMetadata is the header of one seance compte rendu (the syseron XML). Namespaces
// are ignored: encoding/xml matches on the element local name. Only the fields used
// for attribution and provenance are read; the debate body is scanned separately.
type crMetadata struct {
	XMLName     xml.Name `xml:"compteRendu"`
	UID         string   `xml:"uid"`
	SeanceRef   string   `xml:"seanceRef"`
	Metadonnees struct {
		DateSeance     string `xml:"dateSeance"`
		DateSeanceJour string `xml:"dateSeanceJour"`
		NumSeance      string `xml:"numSeance"`
		Legislature    string `xml:"legislature"`
		Session        string `xml:"session"`
	} `xml:"metadonnees"`
}

// intervention is one speaker turn: who spoke (name, and role/qualite when given)
// and what they said (flattened to plain text).
type intervention struct {
	speaker string
	qualite string
	text    string
}

// extractComptesRendus reads the debate-records zip, parsing every .xml entry (one
// seance per file) into a record.
func extractComptesRendus(source, archivePath string) ([]record, error) {
	return extractZipEntries(source, archivePath, ".xml", parseCompteRendu)
}

// parseCompteRendu decodes one seance compte rendu into a record: the seance header
// plus every speaker turn rendered as "Orateur (qualite) : propos", chunked to the
// corpus convention so a debate excerpt is retrievable. A seance with no
// interventions still yields the attributed header, so the record is never empty.
// It returns a one-element slice so it plugs into the shared zip reader.
func parseCompteRendu(source string, data []byte) ([]record, error) {
	var meta crMetadata
	if err := xml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parliament: decode compte rendu: %w", err)
	}
	if meta.UID == "" {
		return nil, fmt.Errorf("parliament: compte rendu has empty uid")
	}

	interventions, err := scanInterventions(data)
	if err != nil {
		return nil, fmt.Errorf("parliament: compte rendu %q: %w", meta.UID, err)
	}

	title := comptesRendusTitle(meta)
	url := fmt.Sprintf(compteRenduURLTemplate, meta.UID)
	content := renderCompteRendu(meta, interventions)
	rec := buildEvidenceRecord(source, meta.UID, title, url, content, comptesRendusMetadata(meta, len(interventions)))
	return []record{rec}, nil
}

// comptesRendusTitle names the record for citation display.
func comptesRendusTitle(m crMetadata) string {
	day := firstNonEmpty(m.Metadonnees.DateSeanceJour, m.Metadonnees.DateSeance)
	if day != "" {
		return "Compte rendu - " + day
	}
	return "Compte rendu " + m.UID
}

// renderCompteRendu builds the attributed passage: a header naming the seance,
// then each intervention on its own line.
func renderCompteRendu(m crMetadata, ivs []intervention) string {
	var b strings.Builder
	b.WriteString("Compte rendu de la seance")
	if day := m.Metadonnees.DateSeanceJour; day != "" {
		b.WriteString(" du ")
		b.WriteString(day)
	}
	if sess := m.Metadonnees.Session; sess != "" {
		b.WriteString(" (")
		b.WriteString(sess)
		b.WriteString(")")
	}
	b.WriteString(".")
	for _, iv := range ivs {
		if iv.text == "" {
			continue
		}
		b.WriteString(" ")
		if iv.speaker != "" {
			b.WriteString(iv.speaker)
			if iv.qualite != "" {
				b.WriteString(" (")
				b.WriteString(iv.qualite)
				b.WriteString(")")
			}
			b.WriteString(" : ")
		}
		b.WriteString(iv.text)
	}
	return b.String()
}

// comptesRendusMetadata renders the source-specific provenance carried verbatim.
func comptesRendusMetadata(m crMetadata, nbInterventions int) map[string]any {
	meta := make(map[string]any)
	putMeta(meta, "legislature", m.Metadonnees.Legislature)
	putMeta(meta, "date_seance", firstNonEmpty(m.Metadonnees.DateSeanceJour, m.Metadonnees.DateSeance))
	putMeta(meta, "num_seance", m.Metadonnees.NumSeance)
	putMeta(meta, "session", m.Metadonnees.Session)
	putMeta(meta, "seance_ref", m.SeanceRef)
	meta["nb_interventions"] = nbInterventions
	return meta
}

// scanInterventions streams the XML and pairs each <texte> speech with its nearest
// preceding <orateur> name, tolerating the varying nesting of <paragraphe> elements
// (a struct unmarshal would miss deeply-nested turns). Entering an <orateurs> block
// resets the current speaker, so an empty <orateurs/> yields an unattributed turn
// rather than inheriting the previous speaker.
func scanInterventions(data []byte) ([]intervention, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []intervention
	var curSpeaker, curQualite string
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "orateurs":
			curSpeaker, curQualite = "", ""
		case "nom":
			txt, err := readElementText(dec)
			if err != nil {
				return nil, err
			}
			// A single turn almost always has one orateur; on the rare co-signed turn
			// (several <orateur> in one <orateurs>) only the last name is kept, which is
			// an accepted simplification for a searchable debate passage.
			curSpeaker = txt
		case "qualite":
			txt, err := readElementText(dec)
			if err != nil {
				return nil, err
			}
			curQualite = txt
		case "texte":
			txt, err := readElementText(dec)
			if err != nil {
				return nil, err
			}
			if txt != "" {
				out = append(out, intervention{speaker: curSpeaker, qualite: curQualite, text: txt})
			}
		}
	}
	return out, nil
}

// readElementText consumes the tokens of the element started by start and returns
// its character content flattened to a single line, so inline markup (<italique>,
// <br/>) is dropped while the words are kept. It stops at the matching end tag.
func readElementText(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			b.Write(t)
		}
	}
	return strings.Join(strings.Fields(b.String()), " "), nil
}
