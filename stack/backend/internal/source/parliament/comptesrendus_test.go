package parliament

import (
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestParseCompteRenduRealShape(t *testing.T) {
	t.Parallel()
	recs, err := parseCompteRendu("an-comptesrendus", readFile(t, "an_compterendu.xml"))
	if err != nil {
		t.Fatalf("parseCompteRendu: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	rec := recs[0]
	if rec.externalID != "CRSANR5L17S2026O1N002" {
		t.Errorf("externalID = %q", rec.externalID)
	}
	lead := rec.jobs[0]
	if lead.Kind != string(domain.EvidenceKindLead) || lead.Source != "an-comptesrendus" {
		t.Errorf("lead job wrong: %+v", lead)
	}

	full := joinContent(rec)
	// The header names the seance date; interventions carry the speaker and text.
	for _, want := range []string{"Compte rendu de la seance", "jeudi 02 octobre 2025", "La séance est ouverte"} {
		if !strings.Contains(full, want) {
			t.Errorf("passage missing %q", want)
		}
	}
	// The president speaks, so a named orateur must appear attributed with " : ".
	if !strings.Contains(full, " : ") {
		t.Errorf("no attributed intervention rendered:\n%.400s", full)
	}
	// XML markup is flattened away.
	if strings.ContainsAny(full, "<>") {
		t.Errorf("XML markup survived flattening:\n%.400s", full)
	}

	nb, ok := lead.Metadata["nb_interventions"].(int)
	if !ok || nb == 0 {
		t.Errorf("nb_interventions metadata = %v, want a positive int", lead.Metadata["nb_interventions"])
	}
	if lead.Metadata["legislature"] != "17" {
		t.Errorf("legislature metadata = %v", lead.Metadata["legislature"])
	}
	if err := lead.Validate(); err != nil {
		t.Errorf("job does not validate: %v", err)
	}
}

func TestScanInterventionsPairsSpeakerAndText(t *testing.T) {
	t.Parallel()
	xml := `<compteRendu><contenu>
	  <paragraphe><orateurs><orateur><nom>Mme Dupont</nom><qualite>ministre</qualite></orateur></orateurs><texte>Je vous <i>remercie</i>.</texte></paragraphe>
	  <paragraphe><orateurs/><texte>Reprise de la seance.</texte></paragraphe>
	</contenu></compteRendu>`
	ivs, err := scanInterventions([]byte(xml))
	if err != nil {
		t.Fatalf("scanInterventions: %v", err)
	}
	if len(ivs) != 2 {
		t.Fatalf("got %d interventions, want 2", len(ivs))
	}
	if ivs[0].speaker != "Mme Dupont" || ivs[0].qualite != "ministre" || ivs[0].text != "Je vous remercie." {
		t.Errorf("first intervention wrong: %+v", ivs[0])
	}
	// An empty <orateurs/> must not inherit the previous speaker.
	if ivs[1].speaker != "" {
		t.Errorf("empty orateurs should yield an unattributed turn, got speaker %q", ivs[1].speaker)
	}
}

func TestParseCompteRenduEmptyUIDRejected(t *testing.T) {
	t.Parallel()
	if _, err := parseCompteRendu("an-comptesrendus", []byte(`<compteRendu></compteRendu>`)); err == nil {
		t.Error("a compte rendu with no uid must be rejected")
	}
}
