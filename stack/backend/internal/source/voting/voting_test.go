package voting

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// stubStore is a hand double for the consumer-side voting.Store: it records the
// lookup arguments and returns a fixed result, so a test asserts exactly what the
// adapter asked the store and how it rendered the answer. It implements only
// LookupVotingRecords, the one method the pack depends on.
type stubStore struct {
	records []domain.VotingRecord
	err     error

	gotPerson string
	gotBill   string
	gotDate   time.Time
	calls     int
}

// stubStore satisfies the consumer interface the pack defines.
var _ Store = (*stubStore)(nil)

func (s *stubStore) LookupVotingRecords(_ context.Context, personID, billTitle string, votedOn time.Time) ([]domain.VotingRecord, error) {
	s.calls++
	s.gotPerson = personID
	s.gotBill = billTitle
	s.gotDate = votedOn
	if s.err != nil {
		return nil, s.err
	}
	return s.records, nil
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse date %q: %v", s, err)
	}
	return d
}

func TestKind(t *testing.T) {
	t.Parallel()
	p := New(&stubStore{})
	if p.Kind() != source.KindVotingRecord {
		t.Fatalf("Kind: got %q want %q", p.Kind(), source.KindVotingRecord)
	}
}

func TestRetrieveRecordedPosition(t *testing.T) {
	t.Parallel()
	store := &stubStore{records: []domain.VotingRecord{
		{
			PersonID:   "PA12345",
			PersonName: "Jean Dupont",
			Chamber:    domain.ChamberAssemblee,
			ScrutinID:  "VTANR5L17V42",
			BillTitle:  "Projet de loi de finances pour 2024",
			VotedOn:    mustDate(t, "2024-10-15"),
			Position:   domain.VoteFor,
			SourceURL:  "https://www.assemblee-nationale.fr/dyn/17/scrutins/42",
		},
	}}

	p := New(store)
	ev, err := p.Retrieve(t.Context(), source.Query{
		Text: "Jean Dupont a vote pour le budget 2024",
		Hints: map[string]string{
			HintPersonID: "PA12345",
			HintBill:     "Projet de loi de finances pour 2024",
			HintVotedOn:  "2024-10-15",
		},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	if store.gotPerson != "PA12345" || store.gotBill != "Projet de loi de finances pour 2024" {
		t.Errorf("lookup args: person=%q bill=%q", store.gotPerson, store.gotBill)
	}
	if !store.gotDate.Equal(mustDate(t, "2024-10-15")) {
		t.Errorf("lookup date: got %v", store.gotDate)
	}

	if len(ev) != 1 {
		t.Fatalf("want 1 evidence, got %d", len(ev))
	}
	e := ev[0]
	if e.Source.URL != "https://www.assemblee-nationale.fr/dyn/17/scrutins/42" {
		t.Errorf("source url: got %q", e.Source.URL)
	}
	if e.Source.Date != "2024-10-15" {
		t.Errorf("source date: got %q", e.Source.Date)
	}
	// Source name names the chamber so a wrong-assembly claim is visible.
	if !strings.Contains(e.Source.Name, "Assemblee nationale") {
		t.Errorf("source name should name the chamber, got %q", e.Source.Name)
	}
	if !strings.Contains(e.Passage, "Jean Dupont") {
		t.Errorf("passage missing person:\n%s", e.Passage)
	}
	if !strings.Contains(e.Passage, "Assemblee nationale") {
		t.Errorf("passage missing chamber:\n%s", e.Passage)
	}
	if !strings.Contains(e.Passage, "pour") {
		t.Errorf("passage missing rendered position:\n%s", e.Passage)
	}
	if !strings.Contains(e.Passage, "Projet de loi de finances pour 2024") {
		t.Errorf("passage missing bill:\n%s", e.Passage)
	}

	// evidence_id round-trips and is keyed by scrutin id.
	rt, err := source.ParseEvidenceID(e.ID.String())
	if err != nil {
		t.Fatalf("ParseEvidenceID: %v", err)
	}
	if rt != e.ID {
		t.Errorf("evidence_id round trip: got %+v want %+v", rt, e.ID)
	}
	if e.ID.Kind != source.KindVotingRecord || e.ID.SourceID != "VTANR5L17V42" {
		t.Errorf("evidence_id components: got %+v", e.ID)
	}
}

func TestRetrieveMultiplePositionsStableIndices(t *testing.T) {
	t.Parallel()
	store := &stubStore{records: []domain.VotingRecord{
		{PersonID: "PA1", PersonName: "A", ScrutinID: "S1", BillTitle: "Bill", VotedOn: mustDate(t, "2024-01-01"), Position: domain.VoteFor, SourceURL: "https://x/1"},
		{PersonID: "PA2", PersonName: "B", ScrutinID: "S1", BillTitle: "Bill", VotedOn: mustDate(t, "2024-01-01"), Position: domain.VoteAgainst, SourceURL: "https://x/1"},
	}}
	p := New(store)
	ev, err := p.Retrieve(t.Context(), source.Query{Hints: map[string]string{
		HintPersonID: "PA1", HintBill: "Bill", HintVotedOn: "2024-01-01",
	}})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 2 {
		t.Fatalf("want 2 evidence, got %d", len(ev))
	}
	if ev[0].ID.Index == ev[1].ID.Index {
		t.Errorf("evidence indices not distinct: %d", ev[0].ID.Index)
	}
}

func TestRetrieveSenatChamberNamed(t *testing.T) {
	t.Parallel()
	store := &stubStore{records: []domain.VotingRecord{
		{PersonID: "S1", PersonName: "Claire Martin", Chamber: domain.ChamberSenat, ScrutinID: "SEN1", BillTitle: "Bill", VotedOn: mustDate(t, "2024-02-02"), Position: domain.VoteAgainst, SourceURL: "https://senat/1"},
	}}
	p := New(store)
	ev, err := p.Retrieve(t.Context(), source.Query{Hints: map[string]string{
		HintPersonID: "S1", HintBill: "Bill", HintVotedOn: "2024-02-02",
	}})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 1 {
		t.Fatalf("want 1 evidence, got %d", len(ev))
	}
	if !strings.Contains(ev[0].Source.Name, "Senat") {
		t.Errorf("source name should name the Senat, got %q", ev[0].Source.Name)
	}
	if !strings.Contains(ev[0].Passage, "Senat") {
		t.Errorf("passage should name the Senat:\n%s", ev[0].Passage)
	}
}

func TestRetrieveNoMatch(t *testing.T) {
	t.Parallel()
	store := &stubStore{records: nil}
	p := New(store)
	ev, err := p.Retrieve(t.Context(), source.Query{Hints: map[string]string{
		HintPersonID: "PA1", HintBill: "Bill", HintVotedOn: "2024-01-01",
	}})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 0 {
		t.Fatalf("want no evidence for no match, got %d", len(ev))
	}
}

func TestRetrieveMissingHintsSkipsStore(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	p := New(store)
	ev, err := p.Retrieve(t.Context(), source.Query{Text: "some free text only"})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 0 {
		t.Fatalf("want no evidence without selectors, got %d", len(ev))
	}
	if store.calls != 0 {
		t.Fatalf("store should not be queried without selectors, calls=%d", store.calls)
	}
}

func TestRetrieveBadDateHint(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	p := New(store)
	_, err := p.Retrieve(t.Context(), source.Query{Hints: map[string]string{
		HintPersonID: "PA1", HintBill: "Bill", HintVotedOn: "15/10/2024",
	}})
	if err == nil {
		t.Fatalf("want error on unparseable date hint")
	}
	if store.calls != 0 {
		t.Fatalf("store queried despite bad date, calls=%d", store.calls)
	}
}

func TestRetrieveStoreError(t *testing.T) {
	t.Parallel()
	store := &stubStore{err: errors.New("db down")}
	p := New(store)
	_, err := p.Retrieve(t.Context(), source.Query{Hints: map[string]string{
		HintPersonID: "PA1", HintBill: "Bill", HintVotedOn: "2024-01-01",
	}})
	if err == nil {
		t.Fatalf("want error when store fails")
	}
}
