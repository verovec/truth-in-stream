package legifrance

import (
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/source/evidencesrc"
)

// Source is the connector name and the evidence Source every record lands under.
const Source = "legifrance"

// articleURLTemplate builds the public Legifrance link to a consolidated article,
// each chunk's provenance.
const articleURLTemplate = "https://www.legifrance.gouv.fr/codes/article_lc/"

// buildRecord renders one fetched article into an evidence record. Label is the
// operator-supplied human name of the code the article belongs to (empty when the
// operator listed the id alone), used only for display since getArticle does not
// return the code name.
func buildRecord(art Article, label string) evidencesrc.Record {
	title := articleTitle(art, label)
	url := articleURLTemplate + art.ID
	return evidencesrc.BuildRecord(Source, art.ID, title, url, render(art, label), metadata(art, label))
}

// articleTitle names the passage for citation display.
func articleTitle(art Article, label string) string {
	num := strings.TrimSpace(art.Num)
	switch {
	case num != "" && label != "":
		return "Article " + num + " - " + label
	case num != "":
		return "Article " + num
	case label != "":
		return label
	default:
		return "Article " + art.ID
	}
}

// render builds the attributed French passage: the article citation, its status
// and effective date, then its consolidated text.
func render(art Article, label string) string {
	var b strings.Builder
	if num := strings.TrimSpace(art.Num); num != "" {
		b.WriteString("Article ")
		b.WriteString(num)
	} else {
		b.WriteString("Article de loi")
	}
	if label != "" {
		b.WriteString(" (")
		b.WriteString(label)
		b.WriteString(")")
	}
	b.WriteString(" - texte consolidé (source Légifrance).")
	if etat := strings.TrimSpace(art.Etat); etat != "" {
		b.WriteString(" État : ")
		b.WriteString(etat)
		b.WriteString(".")
	}
	if d := strings.TrimSpace(art.DateDebut); d != "" {
		b.WriteString(" En vigueur depuis le ")
		b.WriteString(d)
		b.WriteString(".")
	}
	if txt := evidencesrc.PlainText(art.Texte); txt != "" {
		b.WriteString(" ")
		b.WriteString(txt)
	}
	return b.String()
}

// metadata renders the source-specific provenance carried verbatim as jsonb.
func metadata(art Article, label string) map[string]any {
	meta := make(map[string]any)
	evidencesrc.PutMeta(meta, "article_id", art.ID)
	evidencesrc.PutMeta(meta, "num", art.Num)
	evidencesrc.PutMeta(meta, "etat", art.Etat)
	evidencesrc.PutMeta(meta, "cid", art.Cid)
	evidencesrc.PutMeta(meta, "date_debut", art.DateDebut)
	evidencesrc.PutMeta(meta, "code", label)
	return meta
}

// articleFingerprint is the article content digest the manifest diffs on: a
// consolidation change (new text, new status, new effective date) republishes.
func articleFingerprint(art Article) string {
	return evidencesrc.Fingerprint(art.ID, art.Num, art.Etat, art.DateDebut, art.Texte)
}
