package votingrecord_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

func readFixture(tb testing.TB, name string) []byte {
	tb.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		tb.Fatalf("read fixture %q: %v", name, err)
	}
	return b
}

func recordByActor(records []domain.VotingRecord, actorRef string) (domain.VotingRecord, bool) {
	i := slices.IndexFunc(records, func(r domain.VotingRecord) bool { return r.PersonID == actorRef })
	if i < 0 {
		return domain.VotingRecord{}, false
	}
	return records[i], true
}

func TestParseScrutinRealShape(t *testing.T) {
	t.Parallel()

	records, err := votingrecord.ParseScrutin(readFixture(t, "VTANR5L17V42.json"))
	if err != nil {
		t.Fatalf("ParseScrutin: %v", err)
	}

	// Two pours, one contre, one abstention, one non-votant => five records,
	// every deputy named in the nominative decompte regardless of group.
	if got, want := len(records), 5; got != want {
		t.Fatalf("got %d records, want %d: %+v", got, want, records)
	}

	wantDate := time.Date(2024, 10, 15, 0, 0, 0, 0, time.UTC)
	wantBill := "sur l'ensemble du projet de loi de finances pour 2025 (première lecture)."
	wantURL := "https://www.assemblee-nationale.fr/dyn/17/scrutins/42"

	for _, r := range records {
		if r.ScrutinID != "VTANR5L17V42" {
			t.Errorf("record %q: ScrutinID = %q, want VTANR5L17V42", r.PersonID, r.ScrutinID)
		}
		if r.Chamber != domain.ChamberAssemblee {
			t.Errorf("record %q: Chamber = %q, want assemblee", r.PersonID, r.Chamber)
		}
		if !r.VotedOn.Equal(wantDate) {
			t.Errorf("record %q: VotedOn = %v, want %v", r.PersonID, r.VotedOn, wantDate)
		}
		if r.BillTitle != wantBill {
			t.Errorf("record %q: BillTitle = %q, want %q", r.PersonID, r.BillTitle, wantBill)
		}
		if r.SourceURL != wantURL {
			t.Errorf("record %q: SourceURL = %q, want %q", r.PersonID, r.SourceURL, wantURL)
		}
		if !r.Position.Valid() {
			t.Errorf("record %q: Position %q invalid", r.PersonID, r.Position)
		}
		if r.PersonName != r.PersonID {
			// PersonName falls back to the actor ref when the scrutin file
			// carries no nominative name (the names live in a separate dataset).
			t.Errorf("record %q: PersonName = %q, want fallback to actor ref", r.PersonID, r.PersonName)
		}
	}

	cases := []struct {
		actorRef string
		position domain.VotePosition
	}{
		{"PA1592", domain.VoteFor},       // pours, array form
		{"PA1880", domain.VoteFor},       // pours, array form, parDelegation true
		{"PA721002", domain.VoteAgainst}, // contres, singleton-object form
		{"PA605036", domain.VoteAbstain}, // abstentions
		{"PA2630", domain.VoteAbsent},    // nonVotants, singleton-object form
	}
	for _, c := range cases {
		r, ok := recordByActor(records, c.actorRef)
		if !ok {
			t.Errorf("actor %q missing from records", c.actorRef)
			continue
		}
		if r.Position != c.position {
			t.Errorf("actor %q: Position = %q, want %q", c.actorRef, r.Position, c.position)
		}
	}
}

func TestParseScrutinDuplicateActorRejected(t *testing.T) {
	t.Parallel()

	const dup = `{"scrutin":{"uid":"VTANR5L17V1","numero":"1","legislature":"17",
		"dateScrutin":"2024-09-01","objet":{"libelle":"x"},
		"ventilationVotes":{"organe":{"groupes":{"groupe":[
			{"vote":{"decompteNominatif":{
				"pours":{"votant":{"acteurRef":"PA1"}},
				"contres":{"votant":{"acteurRef":"PA1"}}}}}
		]}}}}}`
	if _, err := votingrecord.ParseScrutin([]byte(dup)); err == nil {
		t.Fatal("expected error for an actor appearing twice in one scrutin")
	}
}

func TestParseScrutinMissingFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "empty uid",
			json: `{"scrutin":{"numero":"1","legislature":"17","dateScrutin":"2024-09-01","objet":{"libelle":"x"}}}`,
			want: "scrutin id",
		},
		{
			name: "bad date",
			json: `{"scrutin":{"uid":"VTANR5L17V1","numero":"1","legislature":"17","dateScrutin":"15/10/2024","objet":{"libelle":"x"}}}`,
			want: "date",
		},
		{
			name: "empty objet",
			json: `{"scrutin":{"uid":"VTANR5L17V1","numero":"1","legislature":"17","dateScrutin":"2024-09-01","objet":{"libelle":""}}}`,
			want: "bill",
		},
		{
			name: "malformed json",
			json: `{"scrutin":`,
			want: "decode",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := votingrecord.ParseScrutin([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseScrutinSingleGroupRenderedAsObject(t *testing.T) {
	t.Parallel()

	// When only one political group takes part, the AN tooling renders "groupe"
	// as a bare object rather than a one-element array - the same XML-to-JSON
	// quirk as a lone "votant". The parser must still read it.
	const oneGroup = `{"scrutin":{"uid":"VTANR5L17V7","numero":"7","legislature":"17",
		"dateScrutin":"2024-09-02","objet":{"libelle":"motion"},
		"ventilationVotes":{"organe":{"groupes":{"groupe":
			{"vote":{"decompteNominatif":{
				"pours":{"votant":{"acteurRef":"PA42"}},
				"contres":null,"abstentions":null,"nonVotants":null}}}
		}}}}}`
	records, err := votingrecord.ParseScrutin([]byte(oneGroup))
	if err != nil {
		t.Fatalf("ParseScrutin: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(records), records)
	}
	if records[0].PersonID != "PA42" || records[0].Position != domain.VoteFor {
		t.Errorf("record = %+v, want PA42/for", records[0])
	}
}

func TestParseScrutinEmptyNominativeYieldsNoRecords(t *testing.T) {
	t.Parallel()

	// A scrutin published only as a group position (no nominative decompte) has
	// no per-person rows to ingest; that is not an error, just zero records.
	const noNominative = `{"scrutin":{"uid":"VTANR5L17V9","numero":"9","legislature":"17",
		"dateScrutin":"2024-09-01","objet":{"libelle":"x"},
		"modePublicationDesVotes":"DecompteDissidentsPositionGroupe",
		"ventilationVotes":{"organe":{"groupes":{"groupe":[
			{"vote":{"positionMajoritaire":"pour","decompteNominatif":null}}
		]}}}}}`
	records, err := votingrecord.ParseScrutin([]byte(noNominative))
	if err != nil {
		t.Fatalf("ParseScrutin: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("got %d records, want 0: %+v", len(records), records)
	}
}

func TestParseScrutinIsDeterministic(t *testing.T) {
	t.Parallel()

	raw := readFixture(t, "VTANR5L17V42.json")
	first, err := votingrecord.ParseScrutin(raw)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	second, err := votingrecord.ParseScrutin(raw)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if diff := cmp.Diff(first, second, cmpopts.EquateComparable(time.Time{})); diff != "" {
		t.Errorf("parse is not deterministic (-first +second):\n%s", diff)
	}
}

// FuzzParseScrutin asserts ParseScrutin never panics on arbitrary input - it
// parses externally-sourced JSON with a branchy custom unmarshaller, exactly the
// surface the Go skill's fuzz rule targets. A returned error is fine; a panic is
// the bug being hunted.
func FuzzParseScrutin(f *testing.F) {
	f.Add(readFixture(f, "VTANR5L17V42.json"))
	f.Add([]byte(`{"scrutin":{"uid":"x","numero":"1","legislature":"17","dateScrutin":"2024-01-01","objet":{"libelle":"y"},"ventilationVotes":{"organe":{"groupes":{"groupe":{"vote":{"decompteNominatif":{"pours":{"votant":{"acteurRef":"PA1"}}}}}}}}}}`))
	f.Add([]byte(`{"scrutin":null}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = votingrecord.ParseScrutin(data)
	})
}
