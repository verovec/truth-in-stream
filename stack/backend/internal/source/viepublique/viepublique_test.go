package viepublique

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TestExtractRealDump parses the captured real excerpt of the DILA Discours
// publics dump and asserts the rendered records carry the attribution facts and a
// valid, well-keyed evidence job.
func TestExtractRealDump(t *testing.T) {
	t.Parallel()
	records, err := Extract(Source, filepath.Join("testdata", "vp_discours.json"))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("Extract returned no records from the real dump excerpt")
	}
	for _, rec := range records {
		if rec.ExternalID == "" {
			t.Errorf("record has empty external id")
		}
		if rec.Fingerprint == "" {
			t.Errorf("record %q has empty fingerprint", rec.ExternalID)
		}
		if len(rec.Jobs) == 0 {
			t.Fatalf("record %q rendered no jobs", rec.ExternalID)
		}
		lead := rec.Jobs[0]
		if lead.Source != Source {
			t.Errorf("job source = %q, want %q", lead.Source, Source)
		}
		if lead.ExternalID != rec.ExternalID {
			t.Errorf("job external id = %q, want %q", lead.ExternalID, rec.ExternalID)
		}
		if lead.Kind != string(domain.EvidenceKindLead) {
			t.Errorf("lead job kind = %q, want lead", lead.Kind)
		}
		if err := lead.Validate(); err != nil {
			t.Errorf("job for %q does not validate: %v", rec.ExternalID, err)
		}
		if !strings.HasPrefix(lead.Content, "Discours public") {
			t.Errorf("job content does not start with the passage lead: %q", lead.Content)
		}
	}
}

// TestExtractIsIdempotentByFingerprint proves two runs over the same dump yield
// the same identifiers and fingerprints, so the manifest diff republishes
// nothing on a re-run (AC: re-runs are idempotent).
func TestExtractIsIdempotentByFingerprint(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "vp_discours.json")
	first, err := Extract(Source, path)
	if err != nil {
		t.Fatalf("Extract (first): %v", err)
	}
	second, err := Extract(Source, path)
	if err != nil {
		t.Fatalf("Extract (second): %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("record count changed between runs: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ExternalID != second[i].ExternalID || first[i].Fingerprint != second[i].Fingerprint {
			t.Errorf("record %d not stable: (%s,%s) vs (%s,%s)", i,
				first[i].ExternalID, first[i].Fingerprint, second[i].ExternalID, second[i].Fingerprint)
		}
	}
}

// TestRenderNamesSpeakerAndDate checks the attribution rendering on a synthetic
// record with a named speaker, so a "who said it and when" claim has the facts.
func TestRenderNamesSpeakerAndDate(t *testing.T) {
	t.Parallel()
	d := discours{
		ID:            "266001249",
		Titre:         "Conseil des ministres du 9 juillet 2026.",
		URL:           "https://www.vie-publique.fr/discours/304014",
		Domaine:       "Conseil des ministres",
		Prononciation: "2026-07-09",
		Intervenants:  []intervenant{{Nom: "Sébastien Lecornu", Qualite: "Premier ministre"}},
		TypeEmetteur:  "Conseil des ministres",
		Thematiques:   []string{"Racisme"},
	}
	got := render(d)
	for _, want := range []string{"Sébastien Lecornu", "Premier ministre", "2026-07-09", "Conseil des ministres", "Racisme"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q in: %s", want, got)
		}
	}
}

// TestSpeakerListDropsNullEntry checks a communique's single all-null speaker
// entry renders no speaker line rather than an empty "()".
func TestSpeakerListDropsNullEntry(t *testing.T) {
	t.Parallel()
	if got := speakerList([]intervenant{{}}); got != "" {
		t.Errorf("speakerList of a null entry = %q, want empty", got)
	}
}

// TestExtractRejectsNonArray fails a dump that is not a JSON array, so a wrong URL
// or an error page surfaces instead of silently ingesting nothing.
func TestExtractRejectsNonArray(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"error":"not an array"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(Source, path); err == nil {
		t.Fatal("Extract accepted a non-array dump, want error")
	}
}
