package parliament

import (
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestExtractSenatDoslegRealShape(t *testing.T) {
	t.Parallel()
	recs, err := extractSenatDosleg("senat-dosleg", zipFromSQL(t, "senat_dosleg_loi_excerpt.sql"))
	if err != nil {
		t.Fatalf("extractSenatDosleg: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no dossier records produced from the loi excerpt")
	}
	rec := recs[0]
	if rec.externalID == "" {
		t.Error("dossier record has no external id (loicod)")
	}
	lead := rec.jobs[0]
	if lead.Source != "senat-dosleg" || lead.Kind != string(domain.EvidenceKindLead) {
		t.Errorf("lead job wrong: %+v", lead)
	}
	full := joinContent(rec)
	if !strings.Contains(full, "Dossier legislatif du Senat") {
		t.Errorf("passage missing the dossier header:\n%s", full)
	}
	if lead.Metadata["chambre"] != "senat" {
		t.Errorf("metadata chambre = %v, want senat", lead.Metadata["chambre"])
	}
	// The dossier type label resolves from the typloi table (pjl/ppl/cvn...).
	if lead.Metadata["type"] == nil || lead.Metadata["type"] == "" {
		t.Errorf("dossier type label not resolved from typloi: %+v", lead.Metadata)
	}
	if err := lead.Validate(); err != nil {
		t.Errorf("job does not validate: %v", err)
	}
}
