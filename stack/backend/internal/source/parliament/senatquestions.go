package parliament

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
)

// extractSenatQuestions reads the Senat written-questions CSV extract (a single
// ISO-8859-1, semicolon-separated file, not a zip) into one record per question.
func extractSenatQuestions(source, archivePath string) ([]record, error) {
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		return nil, fmt.Errorf("parliament: read senat questions csv: %w", err)
	}
	return parseSenatQuestionsCSV(source, raw)
}

// parseSenatQuestionsCSV decodes the Senat questions CSV (verified real header:
// "Sort;Nature;Numero;Reference;Titre;Nom;Prenom;Civilite;Circonscription;Groupe;
// ...;URL Question"). The file is Latin-1, so it is decoded byte-for-byte to
// Unicode before CSV parsing. Columns are resolved by header name, not position, so
// a reordered or extended export still parses. Each data row becomes one attributed
// evidence record keyed by the question's stable Reference.
func parseSenatQuestionsCSV(source string, raw []byte) ([]record, error) {
	r := csv.NewReader(strings.NewReader(decodeLatin1(raw)))
	r.Comma = ';'
	r.FieldsPerRecord = -1 // the export has trailing empty columns of varying count

	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parliament: parse senat questions csv: %w", err)
	}
	if len(rows) < 2 {
		return nil, nil
	}
	col := headerIndex(rows[0])
	get := func(row []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	records := make([]record, 0, len(rows)-1)
	for _, row := range rows[1:] {
		ref := get(row, "Référence")
		if ref == "" {
			continue // a stable id is required for idempotency
		}
		content := renderSenatQuestion(row, get)
		title := senatQuestionTitle(row, get)
		url := get(row, "URL Question")
		records = append(records, buildEvidenceRecord(source, ref, title, url, content, senatQuestionMetadata(row, get)))
	}
	return records, nil
}

// getFn resolves a named column of a CSV row.
type getFn func(row []string, name string) string

func senatQuestionTitle(row []string, get getFn) string {
	titre := get(row, "Titre")
	num := get(row, "Numéro")
	if titre != "" {
		return fmt.Sprintf("Question ecrite Senat n°%s - %s", num, titre)
	}
	return "Question ecrite Senat n°" + num
}

// renderSenatQuestion builds the attributed French passage. Unlike the AN questions
// (which carry only an actor ref), the Senat export names the senator, so the
// author renders in full.
func renderSenatQuestion(row []string, get getFn) string {
	var b strings.Builder
	b.WriteString("Question ecrite du Senat n°")
	b.WriteString(get(row, "Numéro"))
	auteur := strings.TrimSpace(get(row, "Civilité") + " " + get(row, "Prénom") + " " + get(row, "Nom"))
	if auteur != "" {
		b.WriteString(" de ")
		b.WriteString(auteur)
		if grp := get(row, "Groupe"); grp != "" {
			b.WriteString(" (")
			b.WriteString(grp)
			if circ := get(row, "Circonscription"); circ != "" {
				b.WriteString(", ")
				b.WriteString(circ)
			}
			b.WriteString(")")
		}
	}
	if ministry := get(row, "Ministère de dépôt"); ministry != "" {
		b.WriteString(" au ministere ")
		b.WriteString(ministry)
	}
	if d := get(row, "Date de publication JO"); d != "" {
		b.WriteString(", publiee au JO du ")
		b.WriteString(d)
	}
	b.WriteString(".")
	if titre := get(row, "Titre"); titre != "" {
		b.WriteString(" Sujet : ")
		b.WriteString(titre)
		b.WriteString(".")
	}
	if theme := get(row, "Thème(s)"); theme != "" {
		b.WriteString(" Themes : ")
		b.WriteString(theme)
		b.WriteString(".")
	}
	if dr := get(row, "Date de réponse JO"); dr != "" {
		b.WriteString(" Reponse publiee au JO du ")
		b.WriteString(dr)
		if minr := get(row, "Ministère de réponse"); minr != "" {
			b.WriteString(" (ministere ")
			b.WriteString(minr)
			b.WriteString(")")
		}
		b.WriteString(".")
	} else {
		b.WriteString(" Aucune reponse publiee a ce jour.")
	}
	if sort := get(row, "Sort"); sort != "" {
		b.WriteString(" Statut : ")
		b.WriteString(sort)
		b.WriteString(".")
	}
	return b.String()
}

func senatQuestionMetadata(row []string, get getFn) map[string]any {
	meta := make(map[string]any)
	putMeta(meta, "chambre", "senat")
	putMeta(meta, "numero", get(row, "Numéro"))
	putMeta(meta, "reference", get(row, "Référence"))
	putMeta(meta, "nature", get(row, "Nature"))
	putMeta(meta, "nom", get(row, "Nom"))
	putMeta(meta, "prenom", get(row, "Prénom"))
	putMeta(meta, "groupe", get(row, "Groupe"))
	putMeta(meta, "circonscription", get(row, "Circonscription"))
	putMeta(meta, "ministere_depot", get(row, "Ministère de dépôt"))
	putMeta(meta, "ministere_reponse", get(row, "Ministère de réponse"))
	putMeta(meta, "date_publication", get(row, "Date de publication JO"))
	putMeta(meta, "date_reponse", get(row, "Date de réponse JO"))
	putMeta(meta, "theme", get(row, "Thème(s)"))
	putMeta(meta, "statut", get(row, "Sort"))
	meta["repondue"] = get(row, "Date de réponse JO") != ""
	return meta
}

// headerIndex maps each trimmed header name to its column index.
func headerIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}
	return idx
}

// decodeLatin1 decodes ISO-8859-1 bytes to a UTF-8 string: every byte maps to the
// Unicode code point of the same value, which is exactly the ISO-8859-1 mapping. It
// avoids an x/text dependency for a single-byte charset.
func decodeLatin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}
	return string(runes)
}
