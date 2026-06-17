// Package voting answers voting-record claims ("did X vote for/against bill Y")
// from the structured voting store rather than by cosine similarity. It wraps the
// store behind the source.Retriever contract: a routing layer (card J) resolves
// the person, bill, and date selectors and passes them as query hints, and this
// pack returns one evidence passage per recorded position, each carrying the
// Assemblee Nationale / Senat source url and a stable evidence id keyed by the
// scrutin. The verdict itself is left to the verifier; the pack only supplies the
// recorded fact and its provenance.
//
// The pack is inert until wired by the routing layer (card J); it makes no
// outbound call and reads only the store handed to it.
package voting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// Hint keys the voting pack reads off a source.Query to select a recorded vote.
// A voting-record claim is routed (card J) with the deputy, bill, and date
// already resolved against the actors and scrutins data; this pack does not guess
// them from free text. All three are required for a lookup; a query missing any
// selects nothing.
const (
	// HintPersonID is the stable person identifier (the AN acteurRef) the store
	// keys a recorded position by.
	HintPersonID = "voting_person_id"
	// HintBill is the scrutin's bill title (objet), matched exactly by the store.
	HintBill = "voting_bill"
	// HintVotedOn is the scrutin date as an ISO calendar date (YYYY-MM-DD).
	HintVotedOn = "voting_date"
)

// dateLayout is the ISO calendar date the HintVotedOn selector carries and the
// date this pack renders back into a passage and a Source.Date. It mirrors the
// scrutin date format the store holds (UTC midnight, no time component).
const dateLayout = "2006-01-02"

// chamberNames render a chamber as the publisher shown for a recorded vote, so a
// claim that places a vote in the wrong assembly is visible to the verifier. An
// unknown chamber falls back to a generic label rather than an empty source name.
var chamberNames = map[domain.Chamber]string{
	domain.ChamberAssemblee: "Assemblee nationale (scrutin)",
	domain.ChamberSenat:     "Senat (scrutin)",
}

// fallbackSourceName is the publisher shown when a record carries no known
// chamber; the official scrutin url on each passage remains the precise
// provenance a reader verifies against.
const fallbackSourceName = "Scrutin parlementaire"

// Store is the slice of the voting store this pack reads: a relational lookup by
// (person, bill, date). It is the consumer-side narrowing of domain.VotingStore -
// the pack never upserts.
type Store interface {
	LookupVotingRecords(ctx context.Context, personID, billTitle string, votedOn time.Time) ([]domain.VotingRecord, error)
}

// Pack retrieves recorded parliamentary votes as evidence. It satisfies
// source.Retriever.
type Pack struct {
	store Store
}

// New builds a voting pack over the given store. The store is the only
// dependency; there is no network client and no key, so the zero-config pack is
// fully usable once a store is injected at wiring time.
func New(store Store) *Pack {
	return &Pack{store: store}
}

// Kind reports the source family.
func (p *Pack) Kind() source.Kind { return source.KindVotingRecord }

// Retrieve looks up the recorded positions for the (person, bill, date) the
// query hints select and returns one evidence passage per position. A query that
// does not carry all three selectors returns no evidence (not an error): the pack
// answers only structured voting-record claims and the routing layer is expected
// to supply the selectors. A store match yields a passage stating the recorded
// position in French with the official source url and a stable id keyed by the
// scrutin; no match returns an empty slice.
func (p *Pack) Retrieve(ctx context.Context, q source.Query) ([]source.Evidence, error) {
	personID, okPerson := q.Hint(HintPersonID)
	bill, okBill := q.Hint(HintBill)
	rawDate, okDate := q.Hint(HintVotedOn)
	if !okPerson || !okBill || !okDate || personID == "" || bill == "" || rawDate == "" {
		return nil, nil
	}

	votedOn, err := time.Parse(dateLayout, rawDate)
	if err != nil {
		return nil, fmt.Errorf("voting: parsing %s %q: %w", HintVotedOn, rawDate, err)
	}

	records, err := p.store.LookupVotingRecords(ctx, personID, bill, votedOn)
	if err != nil {
		return nil, fmt.Errorf("voting: looking up records for %q on %q: %w", personID, bill, err)
	}

	out := make([]source.Evidence, 0, len(records))
	for i, r := range records {
		out = append(out, source.Evidence{
			ID:      source.NewEvidenceID(source.KindVotingRecord, r.ScrutinID, i),
			Passage: renderRecord(r),
			Source: source.Source{
				Name: chamberName(r.Chamber),
				URL:  r.SourceURL,
				Date: r.VotedOn.Format(dateLayout),
			},
		})
	}
	return out, nil
}

// positionLabels renders a recorded position in French. The store's positions
// mirror the four nominative buckets the AN publishes; an unknown position falls
// back to its raw value so a future bucket is never silently dropped.
var positionLabels = map[domain.VotePosition]string{
	domain.VoteFor:     "a vote pour",
	domain.VoteAgainst: "a vote contre",
	domain.VoteAbstain: "s'est abstenu",
	domain.VoteAbsent:  "n'a pas pris part au vote",
}

// chamberName renders the chamber as the source publisher, falling back to a
// generic label for an unknown chamber so a citation always names a source.
func chamberName(c domain.Chamber) string {
	if name, ok := chamberNames[c]; ok {
		return name
	}
	return fallbackSourceName
}

func renderRecord(r domain.VotingRecord) string {
	label, ok := positionLabels[r.Position]
	if !ok {
		label = "position " + string(r.Position)
	}
	var b strings.Builder
	b.WriteString(r.PersonName)
	b.WriteByte(' ')
	b.WriteString(label)
	b.WriteString(" sur le scrutin \"")
	b.WriteString(r.BillTitle)
	b.WriteString("\" (")
	b.WriteString(chamberName(r.Chamber))
	b.WriteString(", ")
	b.WriteString(r.VotedOn.Format(dateLayout))
	b.WriteString(").")
	return b.String()
}
