package parliament

import (
	"strings"
)

// senatDoslegURL is the Senat legislative-dossiers landing page, used as a generic
// provenance link when a dossier carries no Journal Officiel URL.
const senatDoslegURL = "https://www.senat.fr/dossiers-legislatifs/"

// doslegRow is the subset of a dosleg `loi` row this connector renders.
type doslegRow struct {
	loicod    string
	typloicod string
	intitule  string
	titre     string
	objet     string
	urlJO     string
	date      string
}

// extractSenatDosleg streams the Senat dosleg PostgreSQL dump and renders each
// legislative dossier (the loi table) into an attributed evidence record, resolving
// the dossier type label from the typloi table. The join is order-independent: loi
// rows are buffered during the pass and rendered after the whole stream completes,
// once the typloi labels are loaded, so the dump's COPY table order does not matter.
func extractSenatDosleg(source, archivePath string) ([]record, error) {
	var rows []doslegRow
	typeLabels := make(map[string]string)
	idxCache := make(map[string]map[string]int)

	want := map[string]bool{"loi": true, "typloi": true}
	err := streamCopyFromDumpZip(archivePath, want, func(table string, cols, fields []string) error {
		idx, ok := idxCache[table]
		if !ok {
			idx = colIndex(cols)
			idxCache[table] = idx
		}
		switch table {
		case "typloi":
			if code := field(idx, fields, "typloicod"); code != "" {
				typeLabels[code] = field(idx, fields, "typloilib")
			}
		case "loi":
			id := field(idx, fields, "loicod")
			if id == "" {
				return nil
			}
			rows = append(rows, doslegRow{
				loicod:    id,
				typloicod: field(idx, fields, "typloicod"),
				intitule:  field(idx, fields, "loiint"),
				titre:     firstNonEmpty(field(idx, fields, "loitit"), field(idx, fields, "loient")),
				objet:     field(idx, fields, "objet"),
				urlJO:     field(idx, fields, "url_jo"),
				date:      senatDate(field(idx, fields, "date_loi")),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	records := make([]record, 0, len(rows))
	for _, r := range rows {
		records = append(records, buildEvidenceRecord(
			source, r.loicod, doslegTitle(r), doslegURL(r), renderDosleg(r, typeLabels), doslegMetadata(r, typeLabels),
		))
	}
	return records, nil
}

func doslegTitle(r doslegRow) string {
	if t := firstNonEmpty(r.titre, r.intitule); t != "" {
		return "Dossier legislatif - " + t
	}
	return "Dossier legislatif " + r.loicod
}

func doslegURL(r doslegRow) string {
	if r.urlJO != "" {
		return r.urlJO
	}
	return senatDoslegURL
}

// renderDosleg builds the attributed French passage for one dossier.
func renderDosleg(r doslegRow, typeLabels map[string]string) string {
	var b strings.Builder
	b.WriteString("Dossier legislatif du Senat")
	if t := firstNonEmpty(r.titre, r.intitule); t != "" {
		b.WriteString(" : ")
		b.WriteString(t)
	}
	if label := typeLabels[r.typloicod]; label != "" {
		b.WriteString(" (")
		b.WriteString(strings.TrimSpace(label))
		b.WriteString(")")
	}
	b.WriteString(".")
	if r.intitule != "" && r.intitule != r.titre {
		b.WriteString(" Intitule : ")
		b.WriteString(r.intitule)
		b.WriteString(".")
	}
	if r.objet != "" {
		b.WriteString(" Objet : ")
		b.WriteString(r.objet)
		b.WriteString(".")
	}
	if r.date != "" {
		b.WriteString(" Date : ")
		b.WriteString(r.date)
		b.WriteString(".")
	}
	return b.String()
}

func doslegMetadata(r doslegRow, typeLabels map[string]string) map[string]any {
	meta := make(map[string]any)
	putMeta(meta, "chambre", "senat")
	putMeta(meta, "loicod", r.loicod)
	putMeta(meta, "type", strings.TrimSpace(typeLabels[r.typloicod]))
	putMeta(meta, "date", r.date)
	putMeta(meta, "url_jo", r.urlJO)
	return meta
}
