package parliament

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsjob"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

// zipFromSQL packs a .sql fixture into a dosleg-style zip (one .sql entry) and
// returns the zip path, mirroring the real dosleg.zip a Senat run downloads.
func zipFromSQL(t *testing.T, fixture string) string {
	t.Helper()
	return zipFromSQLBytes(t, readFile(t, fixture))
}

// zipFromSQLBytes packs raw .sql bytes into a dosleg-style zip.
func zipFromSQLBytes(t *testing.T, sql []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dosleg.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer func() { _ = f.Close() }()
	zw := zip.NewWriter(f)
	w, err := zw.Create("dosleg.sql")
	if err != nil {
		t.Fatalf("zip create entry: %v", err)
	}
	if _, err := w.Write(sql); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return path
}

// TestExtractSenatScrutinsOrderIndependent proves the scr<->votsen join does not
// depend on the dump's COPY table order: with votsen streamed BEFORE scr/posvot/
// auteur, every vote is still resolved (none silently dropped).
func TestExtractSenatScrutinsOrderIndependent(t *testing.T) {
	t.Parallel()
	// votsen first, then scr, posvot, auteur - the reverse of the join dependency.
	sql := "COPY votsen (sesann, scrnum, senmat, posvotcod) FROM stdin;\n" +
		"2026\t7\t98046X\t2\n" +
		"2026\t7\t98047Y\t1\n" +
		"\\.\n\n" +
		"COPY scr (sesann, scrnum, scrint, scrdat) FROM stdin;\n" +
		"2026\t7\tsur l'ensemble du projet de loi Z\t2026-03-01 00:00:00\n" +
		"\\.\n\n" +
		"COPY posvot (posvotcod, posvotlib) FROM stdin;\n" +
		"1\tpour\n2\tcontre\n\\.\n\n" +
		"COPY auteur (autmat, nomuse, prenom, grpapp) FROM stdin;\n" +
		"98046X\tMARC\tFrançois\tSOC\n98047Y\tFRÉVILLE\tYves\tUC\n\\.\n"

	payloads, err := extractSenatScrutins(zipFromSQLBytes(t, []byte(sql)), 0)
	if err != nil {
		t.Fatalf("extractSenatScrutins: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("got %d scrutins, want 1 (votsen-before-scr must not drop the scrutin)", len(payloads))
	}
	var job scrutinsjob.ScrutinJob
	if err := json.Unmarshal(payloads[0].body, &job); err != nil {
		t.Fatalf("unmarshal job: %v", err)
	}
	records, err := votingrecord.ParseSenatScrutin(job.Scrutin)
	if err != nil {
		t.Fatalf("ParseSenatScrutin: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 - a vote was silently dropped by table order", len(records))
	}
	positions := map[string]domain.VotePosition{}
	for _, r := range records {
		positions[r.PersonName] = r.Position
	}
	if positions["François MARC"] != domain.VoteAgainst || positions["Yves FRÉVILLE"] != domain.VoteFor {
		t.Errorf("votes not resolved order-independently: %+v", positions)
	}
}

func TestExtractSenatScrutinsJoinsDumpTables(t *testing.T) {
	t.Parallel()
	payloads, err := extractSenatScrutins(zipFromSQL(t, "senat_dosleg_excerpt.sql"), 0)
	if err != nil {
		t.Fatalf("extractSenatScrutins: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("got %d scrutins, want 1", len(payloads))
	}
	p := payloads[0]
	if p.id != "senat-2006-42" || p.fingerprint == "" {
		t.Errorf("payload id/fingerprint wrong: %+v", p)
	}

	// The body is a chamber-aware scrutins job the existing scrutins worker drains.
	var job scrutinsjob.ScrutinJob
	if err := json.Unmarshal(p.body, &job); err != nil {
		t.Fatalf("unmarshal job: %v", err)
	}
	if job.Chamber != string(domain.ChamberSenat) || job.ID != "senat-2006-42" {
		t.Errorf("job chamber/id wrong: %+v", job)
	}

	records, err := votingrecord.ParseSenatScrutin(job.Scrutin)
	if err != nil {
		t.Fatalf("ParseSenatScrutin: %v", err)
	}

	// The AC: a named senator's recorded position resolves. MARC (François) voted
	// contre, FREVILLE (Yves) voted pour, both in the auteur excerpt.
	positions := map[string]domain.VotePosition{}
	for _, r := range records {
		if r.Chamber != domain.ChamberSenat {
			t.Errorf("record %q chamber = %q, want senat", r.PersonName, r.Chamber)
		}
		if r.ScrutinID != "senat-2006-42" {
			t.Errorf("record scrutin id = %q", r.ScrutinID)
		}
		positions[r.PersonName] = r.Position
	}
	if got := positions["François MARC"]; got != domain.VoteAgainst {
		t.Errorf("François MARC position = %q, want against", got)
	}
	if got := positions["Yves FRÉVILLE"]; got != domain.VoteFor {
		t.Errorf("Yves FRÉVILLE position = %q, want for", got)
	}
	// A senator absent from the auteur excerpt still gets a record, named by matricule.
	if got, ok := positions["98053W"]; !ok || got != domain.VoteAgainst {
		t.Errorf("matricule-fallback senator 98053W position = %q (present=%v), want against", got, ok)
	}
	// The bill objet and provenance URL carry through.
	if len(records) > 0 {
		if !strings.Contains(records[0].BillTitle, "nergie") {
			t.Errorf("bill title = %q, want the scrutin objet", records[0].BillTitle)
		}
		if !strings.Contains(records[0].SourceURL, "scrutin-public/2006/scr2006-42") {
			t.Errorf("source url = %q", records[0].SourceURL)
		}
	}
}

func TestExtractSenatScrutinsSinceYearFilter(t *testing.T) {
	t.Parallel()
	payloads, err := extractSenatScrutins(zipFromSQL(t, "senat_dosleg_excerpt.sql"), 2023)
	if err != nil {
		t.Fatalf("extractSenatScrutins: %v", err)
	}
	if len(payloads) != 0 {
		t.Fatalf("sinceYear=2023 should exclude the 2006 scrutin, got %d", len(payloads))
	}
}
