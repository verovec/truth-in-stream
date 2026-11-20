package parliament

import (
	"os"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func TestParseQuestionRealShape(t *testing.T) {
	t.Parallel()
	recs, err := parseQuestion("an-questions", readFile(t, "an_question.json"))
	if err != nil {
		t.Fatalf("parseQuestion: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.externalID != "QANR5L17QE5268" {
		t.Errorf("externalID = %q", rec.externalID)
	}
	lead := rec.jobs[0]
	if lead.Source != "an-questions" || lead.Kind != string(domain.EvidenceKindLead) {
		t.Errorf("lead job wrong: %+v", lead)
	}
	if lead.URL != "https://www.assemblee-nationale.fr/dyn/opendata/QANR5L17QE5268.json" {
		t.Errorf("provenance URL = %q", lead.URL)
	}

	// The passage names the author ref, the interrogated ministry, the rubric, the
	// question and its answer - the checkable content.
	full := joinContent(rec)
	for _, want := range []string{"PA721764", "commerce et artisanat", "Question :", "Reponse (JO du", "Statut :"} {
		if !strings.Contains(full, want) {
			t.Errorf("passage missing %q", want)
		}
	}
	// The ministry name carries accented text decoded from the JSON.
	if !strings.Contains(full, "souveraineté industrielle") {
		t.Errorf("ministry name not rendered:\n%s", full)
	}
	if strings.Contains(full, "&#") || strings.ContainsAny(full, "<>") {
		t.Errorf("HTML entities/tags survived flattening:\n%s", full)
	}

	meta := lead.Metadata
	if meta["numero"] != "5268" || meta["rubrique"] != "commerce et artisanat" || meta["repondue"] != true {
		t.Errorf("metadata wrong: %+v", meta)
	}
	if err := lead.Validate(); err != nil {
		t.Errorf("job does not validate: %v", err)
	}
}

func TestParseQuestionUnansweredRendersAbsence(t *testing.T) {
	t.Parallel()
	raw := `{"question":{"uid":"QANR5L17QE9","identifiant":{"numero":"9","legislature":"17"},"textesQuestion":{"texteQuestion":{"texte":"Une question."}},"textesReponse":null}}`
	recs, err := parseQuestion("an-questions", []byte(raw))
	if err != nil {
		t.Fatalf("parseQuestion: %v", err)
	}
	full := joinContent(recs[0])
	if !strings.Contains(full, "Aucune reponse publiee") {
		t.Errorf("an unanswered question should state the absence of a reply:\n%s", full)
	}
	if recs[0].jobs[0].Metadata["repondue"] != false {
		t.Errorf("repondue metadata should be false for an unanswered question")
	}
}

func TestParseQuestionEmptyUIDRejected(t *testing.T) {
	t.Parallel()
	if _, err := parseQuestion("an-questions", []byte(`{"question":{"uid":""}}`)); err == nil {
		t.Error("a question with an empty uid must be rejected")
	}
}

// joinContent concatenates a record's chunk contents, for asserting on the whole
// rendered passage regardless of how it chunked.
func joinContent(rec record) string {
	var b strings.Builder
	for _, j := range rec.jobs {
		b.WriteString(j.Content)
		b.WriteString(" ")
	}
	return b.String()
}
