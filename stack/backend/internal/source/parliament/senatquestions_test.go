package parliament

import (
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestParseSenatQuestionsRealShape(t *testing.T) {
	t.Parallel()
	recs, err := parseSenatQuestionsCSV("senat-questions", readFile(t, "senat_questions.csv"))
	if err != nil {
		t.Fatalf("parseSenatQuestionsCSV: %v", err)
	}
	if len(recs) < 2 {
		t.Fatalf("got %d records, want the fixture's several rows", len(recs))
	}

	// The first data row is senator Vivette Lopez (Gard, Les Republicains).
	first := recs[0]
	if first.externalID != "SEQ250705636" {
		t.Errorf("externalID = %q, want the question Reference", first.externalID)
	}
	lead := first.jobs[0]
	if lead.Source != "senat-questions" || lead.Kind != string(domain.EvidenceKindLead) {
		t.Errorf("lead job wrong: %+v", lead)
	}
	if !strings.HasPrefix(lead.URL, "http://www.senat.fr/questions/base/2025/qSEQ250705636") {
		t.Errorf("provenance URL = %q", lead.URL)
	}

	full := joinContent(first)
	// The Latin-1 source decodes to accented French, and the author is named in full
	// (unlike the AN questions, which carry only an actor ref).
	for _, want := range []string{"Vivette Lopez", "Les Républicains", "Gard", "Statut :"} {
		if !strings.Contains(full, want) {
			t.Errorf("passage missing %q:\n%s", want, full)
		}
	}
	if strings.Contains(full, "�") {
		t.Errorf("Latin-1 decode produced replacement chars (mojibake):\n%s", full)
	}
	meta := lead.Metadata
	if meta["chambre"] != "senat" || meta["nom"] != "Lopez" || meta["reference"] != "SEQ250705636" {
		t.Errorf("metadata wrong: %+v", meta)
	}
	if err := lead.Validate(); err != nil {
		t.Errorf("job does not validate: %v", err)
	}
}

func TestParseSenatQuestionsSkipsRowsWithoutReference(t *testing.T) {
	t.Parallel()
	// The parser decodes ISO-8859-1, so the accented headers are supplied as Latin-1
	// bytes (\xe9 = e-acute), matching the real export the parser reads.
	csv := "Sort;Num\xe9ro;R\xe9f\xe9rence;Titre;Nom\nEn cours;1;;Sans reference;Martin\nEn cours;2;SEQ2;Avec reference;Durand\n"
	recs, err := parseSenatQuestionsCSV("senat-questions", []byte(csv))
	if err != nil {
		t.Fatalf("parseSenatQuestionsCSV: %v", err)
	}
	if len(recs) != 1 || recs[0].externalID != "SEQ2" {
		t.Fatalf("rows without a Reference must be skipped; got %+v", recs)
	}
}
