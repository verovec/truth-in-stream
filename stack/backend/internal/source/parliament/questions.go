package parliament

import (
	"encoding/json"
	"fmt"
	"strings"
)

// questionURLTemplate is the stable per-question open-data record URL (same dyn
// opendata endpoint the amendments use), each chunk's provenance link.
const questionURLTemplate = "https://www.assemblee-nationale.fr/dyn/opendata/%s.json"

// anQuestion is the {"question": {...}} envelope every written-question file wraps.
type anQuestion struct {
	Question anQuestionInner `json:"question"`
}

// anQuestionInner is the subset of the Assemblee Nationale written-question
// open-data JSON this connector reads, matching the verified live record shape.
// Nullable branches (a not-yet-answered question, a question without a closure
// record) are pointers, so an absent branch is simply nil.
type anQuestionInner struct {
	UID         string `json:"uid"`
	Type        string `json:"type"`
	Identifiant struct {
		Numero      string `json:"numero"`
		Legislature string `json:"legislature"`
	} `json:"identifiant"`
	IndexationAN struct {
		Rubrique string `json:"rubrique"`
	} `json:"indexationAN"`
	Auteur struct {
		Identite struct {
			ActeurRef string `json:"acteurRef"`
		} `json:"identite"`
		Groupe struct {
			Abrege    string `json:"abrege"`
			Developpe string `json:"developpe"`
		} `json:"groupe"`
	} `json:"auteur"`
	MinInt struct {
		Abrege    string `json:"abrege"`
		Developpe string `json:"developpe"`
	} `json:"minInt"`
	TextesQuestion struct {
		TexteQuestion struct {
			InfoJO infoJO `json:"infoJO"`
			Texte  string `json:"texte"`
		} `json:"texteQuestion"`
	} `json:"textesQuestion"`
	TextesReponse *struct {
		TexteReponse struct {
			InfoJO infoJO `json:"infoJO"`
			Texte  string `json:"texte"`
		} `json:"texteReponse"`
	} `json:"textesReponse"`
	Cloture *struct {
		CodeCloture    string `json:"codeCloture"`
		LibelleCloture string `json:"libelleCloture"`
		DateCloture    string `json:"dateCloture"`
	} `json:"cloture"`
}

// infoJO is the Journal Officiel publication stamp shared by the question and
// answer texts; only the date is read.
type infoJO struct {
	DateJO string `json:"dateJO"`
}

// extractQuestions reads the written-questions zip, parsing every .json entry (one
// question per file) into a record.
func extractQuestions(source, archivePath string) ([]record, error) {
	return extractZipEntries(source, archivePath, ".json", parseQuestion)
}

// parseQuestion decodes one written-question file into a record: an attributed
// French passage naming the author, the interrogated ministry, the rubric, and the
// question text, followed by the answer text and closure status when present. The
// answer's presence or absence ("le gouvernement n'a jamais repondu") is itself a
// checkable fact, so a still-open question renders too. parseQuestion returns a
// one-element slice so it plugs into the shared zip reader.
func parseQuestion(source string, data []byte) ([]record, error) {
	var q anQuestion
	if err := json.Unmarshal(data, &q); err != nil {
		return nil, fmt.Errorf("parliament: decode question: %w", err)
	}
	inner := q.Question
	if inner.UID == "" {
		return nil, fmt.Errorf("parliament: question has empty uid")
	}

	title := questionTitle(inner)
	url := fmt.Sprintf(questionURLTemplate, inner.UID)
	return []record{buildEvidenceRecord(source, inner.UID, title, url, renderQuestion(inner), questionMetadata(inner))}, nil
}

// questionTitle names the chunk for citation display.
func questionTitle(q anQuestionInner) string {
	numero := q.Identifiant.Numero
	if q.IndexationAN.Rubrique != "" {
		return fmt.Sprintf("Question ecrite n°%s (%s)", numero, q.IndexationAN.Rubrique)
	}
	if numero != "" {
		return "Question ecrite n°" + numero
	}
	return "Question ecrite " + q.UID
}

// renderQuestion builds the attributed French passage.
func renderQuestion(q anQuestionInner) string {
	var b strings.Builder
	b.WriteString("Question ecrite n°")
	b.WriteString(q.Identifiant.Numero)
	if ref := q.Auteur.Identite.ActeurRef; ref != "" {
		b.WriteString(" de ")
		b.WriteString(ref)
		if grp := q.Auteur.Groupe.Abrege; grp != "" {
			b.WriteString(" (groupe ")
			b.WriteString(grp)
			b.WriteString(")")
		}
	}
	if ministry := firstNonEmpty(q.MinInt.Developpe, q.MinInt.Abrege); ministry != "" {
		b.WriteString(" a ")
		b.WriteString(ministry)
	}
	if rub := q.IndexationAN.Rubrique; rub != "" {
		b.WriteString(", rubrique ")
		b.WriteString(rub)
	}
	if d := q.TextesQuestion.TexteQuestion.InfoJO.DateJO; d != "" {
		b.WriteString(", publiee au JO du ")
		b.WriteString(d)
	}
	b.WriteString(".")
	if txt := plainText(q.TextesQuestion.TexteQuestion.Texte); txt != "" {
		b.WriteString(" Question : ")
		b.WriteString(txt)
	}
	if q.TextesReponse != nil {
		if rep := plainText(q.TextesReponse.TexteReponse.Texte); rep != "" {
			b.WriteString(" Reponse")
			if d := q.TextesReponse.TexteReponse.InfoJO.DateJO; d != "" {
				b.WriteString(" (JO du ")
				b.WriteString(d)
				b.WriteString(")")
			}
			b.WriteString(" : ")
			b.WriteString(rep)
		}
	} else {
		b.WriteString(" Aucune reponse publiee a ce jour.")
	}
	if q.Cloture != nil && q.Cloture.LibelleCloture != "" {
		b.WriteString(" Statut : ")
		b.WriteString(q.Cloture.LibelleCloture)
		b.WriteString(".")
	}
	return b.String()
}

// questionMetadata renders the source-specific provenance carried verbatim as jsonb.
func questionMetadata(q anQuestionInner) map[string]any {
	meta := make(map[string]any)
	putMeta(meta, "legislature", q.Identifiant.Legislature)
	putMeta(meta, "numero", q.Identifiant.Numero)
	putMeta(meta, "type", q.Type)
	putMeta(meta, "rubrique", q.IndexationAN.Rubrique)
	putMeta(meta, "auteur_ref", q.Auteur.Identite.ActeurRef)
	putMeta(meta, "groupe", q.Auteur.Groupe.Abrege)
	putMeta(meta, "ministere", q.MinInt.Abrege)
	putMeta(meta, "date_question", q.TextesQuestion.TexteQuestion.InfoJO.DateJO)
	if q.TextesReponse != nil {
		putMeta(meta, "date_reponse", q.TextesReponse.TexteReponse.InfoJO.DateJO)
		meta["repondue"] = true
	} else {
		meta["repondue"] = false
	}
	if q.Cloture != nil {
		putMeta(meta, "statut", q.Cloture.CodeCloture)
	}
	return meta
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
