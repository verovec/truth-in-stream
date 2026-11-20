package votingrecord

import (
	"encoding/json"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func mustSenatBody(t *testing.T, s SenatScrutin) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestParseSenatScrutinMapsPositionsAndChamber(t *testing.T) {
	t.Parallel()
	body := mustSenatBody(t, SenatScrutin{
		ScrutinID: "senat-2026-15",
		Objet:     "sur l'ensemble du projet de loi X",
		Date:      "2026-02-10",
		SourceURL: "https://www.senat.fr/scrutin-public/2026/scr2026-15.html",
		Votes: []SenatVote{
			{PersonID: "98046X", PersonName: "François MARC", Group: "SOC", Position: "contre"},
			{PersonID: "98047Y", PersonName: "Yves FRÉVILLE", Position: "pour"},
			{PersonID: "92044U", PersonName: "", Position: "abstention"},
			{PersonID: "89029V", PersonName: "X", Position: "non-votant"},
		},
	})
	records, err := ParseSenatScrutin(body)
	if err != nil {
		t.Fatalf("ParseSenatScrutin: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("got %d records, want 4", len(records))
	}
	byID := map[string]domain.VotingRecord{}
	for _, r := range records {
		if r.Chamber != domain.ChamberSenat {
			t.Errorf("record %q chamber = %q, want senat", r.PersonID, r.Chamber)
		}
		if r.ScrutinID != "senat-2026-15" || r.BillTitle == "" {
			t.Errorf("record scrutin fields wrong: %+v", r)
		}
		byID[r.PersonID] = r
	}
	if byID["98046X"].Position != domain.VoteAgainst {
		t.Errorf("contre -> %q, want against", byID["98046X"].Position)
	}
	if byID["98047Y"].Position != domain.VoteFor {
		t.Errorf("pour -> %q, want for", byID["98047Y"].Position)
	}
	if byID["92044U"].Position != domain.VoteAbstain {
		t.Errorf("abstention -> %q, want abstain", byID["92044U"].Position)
	}
	if byID["89029V"].Position != domain.VoteAbsent {
		t.Errorf("non-votant -> %q, want absent", byID["89029V"].Position)
	}
	// A vote with no name falls back to the matricule so a record is never nameless.
	if byID["92044U"].PersonName != "92044U" {
		t.Errorf("nameless senator should fall back to matricule, got %q", byID["92044U"].PersonName)
	}
	// The voting date parses into the calendar-date column.
	if got := records[0].VotedOn.Format("2006-01-02"); got != "2026-02-10" {
		t.Errorf("voted_on = %q, want 2026-02-10", got)
	}
}

func TestParseSenatScrutinRejectsBadPayloads(t *testing.T) {
	t.Parallel()
	base := SenatScrutin{ScrutinID: "s1", Objet: "o", Date: "2026-01-01", Votes: []SenatVote{{PersonID: "a", Position: "pour"}}}
	tests := map[string]func(*SenatScrutin){
		"empty id":         func(s *SenatScrutin) { s.ScrutinID = "" },
		"empty objet":      func(s *SenatScrutin) { s.Objet = "" },
		"bad date":         func(s *SenatScrutin) { s.Date = "2026" },
		"empty person":     func(s *SenatScrutin) { s.Votes = []SenatVote{{PersonID: "", Position: "pour"}} },
		"unknown position": func(s *SenatScrutin) { s.Votes = []SenatVote{{PersonID: "a", Position: "peut-etre"}} },
		"duplicate senator": func(s *SenatScrutin) {
			s.Votes = []SenatVote{{PersonID: "a", Position: "pour"}, {PersonID: "a", Position: "contre"}}
		},
	}
	for name, mut := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := base
			mut(&s)
			if _, err := ParseSenatScrutin(mustSenatBody(t, s)); err == nil {
				t.Errorf("ParseSenatScrutin accepted a bad payload (%s), want error", name)
			}
		})
	}
}
