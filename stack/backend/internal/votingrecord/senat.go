package votingrecord

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// The Senat publishes no per-scrutin JSON like the Assemblee; its roll-call votes
// live in the dosleg PostgreSQL dump (tables scr/votsen/posvot/auteur). The Senat
// producer joins those tables and publishes the self-contained [SenatScrutin]
// payload below, so this parser needs no database and no dump access: it maps one
// pre-joined scrutin into one domain.VotingRecord per senator, with
// domain.ChamberSenat. It is the chamber-aware counterpart to [ParseScrutin] (which
// parses the Assemblee open-data JSON and is hard-coded to domain.ChamberAssemblee),
// so the two chambers share the voting_records store and its (person, scrutin)
// idempotency without either parser guessing the other's format.

// SenatScrutin is the self-contained Senat roll-call vote a producer publishes: the
// scrutin's identity and objet plus one entry per senator's recorded position. It
// is the wire contract between the Senat scrutins producer and the scrutins worker.
type SenatScrutin struct {
	ScrutinID string      `json:"scrutin_id"`
	Objet     string      `json:"objet"`
	Date      string      `json:"date"`
	SourceURL string      `json:"source_url"`
	Votes     []SenatVote `json:"votes"`
}

// SenatVote is one senator's recorded position on a scrutin. PersonID is the
// senator matricule (the dump's stable key); PersonName is the display name;
// Position is the Senat position label (pour, contre, abstention, non-votant).
type SenatVote struct {
	PersonID   string `json:"person_id"`
	PersonName string `json:"person_name"`
	Group      string `json:"group,omitempty"`
	Position   string `json:"position"`
}

// senatPositions maps the Senat position labels (posvot.posvotlib) onto the shared
// vote positions. A senator absent from a scrutin's decompte simply has no row, so
// non-votant is the only "absent" the dump records explicitly.
var senatPositions = map[string]domain.VotePosition{
	"pour":                      domain.VoteFor,
	"contre":                    domain.VoteAgainst,
	"abstention":                domain.VoteAbstain,
	"non-votant":                domain.VoteAbsent,
	"non votant":                domain.VoteAbsent,
	"n'a pas pris part au vote": domain.VoteAbsent,
}

// ParseSenatScrutin turns one pre-joined Senat scrutin into one domain.VotingRecord
// per senator, stamped domain.ChamberSenat. It rejects a payload that could not
// produce a valid row (empty id, objet, or date, an unparseable date, an empty
// senator id, a duplicate senator, or an unknown position), mirroring the strictness
// of ParseScrutin so a malformed job is dead-lettered rather than writing nonsense.
func ParseSenatScrutin(data []byte) ([]domain.VotingRecord, error) {
	var s SenatScrutin
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("votingrecord: decode senat scrutin: %w", err)
	}
	if s.ScrutinID == "" {
		return nil, fmt.Errorf("votingrecord: senat scrutin id is empty")
	}
	if s.Objet == "" {
		return nil, fmt.Errorf("votingrecord: senat scrutin %q: empty objet", s.ScrutinID)
	}
	votedOn, err := time.Parse(dateLayout, s.Date)
	if err != nil {
		return nil, fmt.Errorf("votingrecord: senat scrutin %q: parse date %q: %w", s.ScrutinID, s.Date, err)
	}

	records := make([]domain.VotingRecord, 0, len(s.Votes))
	seen := make(map[string]struct{}, len(s.Votes))
	for _, v := range s.Votes {
		if v.PersonID == "" {
			return nil, fmt.Errorf("votingrecord: senat scrutin %q: vote with empty person id", s.ScrutinID)
		}
		if _, dup := seen[v.PersonID]; dup {
			return nil, fmt.Errorf("votingrecord: senat scrutin %q: senator %q recorded more than once", s.ScrutinID, v.PersonID)
		}
		seen[v.PersonID] = struct{}{}
		position, ok := senatPositions[strings.ToLower(strings.TrimSpace(v.Position))]
		if !ok {
			return nil, fmt.Errorf("votingrecord: senat scrutin %q: unknown position %q for senator %q", s.ScrutinID, v.Position, v.PersonID)
		}
		name := v.PersonName
		if name == "" {
			name = v.PersonID
		}
		records = append(records, domain.VotingRecord{
			PersonID:   v.PersonID,
			PersonName: name,
			Chamber:    domain.ChamberSenat,
			ScrutinID:  s.ScrutinID,
			BillTitle:  s.Objet,
			VotedOn:    votedOn,
			Position:   position,
			SourceURL:  s.SourceURL,
		})
	}
	return records, nil
}
