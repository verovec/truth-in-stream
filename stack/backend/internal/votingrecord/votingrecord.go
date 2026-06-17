// Package votingrecord parses Assemblee Nationale open-data scrutins (recorded
// votes) into the structured domain.VotingRecord rows the voting store holds.
//
// The source is the AN open-data bulk archive Scrutins.json.zip
// (https://data.assemblee-nationale.fr/static/openData/repository/{legislature}/loi/scrutins/Scrutins.json.zip,
// under the Etalab open license, attribution "Assemblee nationale -
// donnees.assemblee-nationale.fr", regenerated daily). Each inner file wraps one
// scrutin as {"scrutin": {...}}. This package turns one such file into one row
// per deputy named in the nominative decompte. The Senat publishes no equivalent
// machine-readable nominative scrutin feed, so only the AN path is ingested here.
//
// The JSON is generated from XML by the AN tooling, which leaves three traps a
// parser must handle: every numeric count is a JSON string, parDelegation is the
// string "true"/"false", and a single-element list is emitted as a bare object
// rather than a one-element array. votantList absorbs the last one; the others
// are simply read as strings since this package only needs the nominative lists.
package votingrecord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// dateLayout is the scrutin date format (dateScrutin), an ISO calendar date with
// no time component. Dates are interpreted as UTC midnight so a stored voted_on
// round-trips through the date column unchanged.
const dateLayout = "2006-01-02"

// scrutinFile is the {"scrutin": {...}} envelope every inner archive file wraps.
type scrutinFile struct {
	Scrutin scrutin `json:"scrutin"`
}

type scrutin struct {
	UID         string      `json:"uid"`
	Numero      string      `json:"numero"`
	Legislature string      `json:"legislature"`
	DateScrutin string      `json:"dateScrutin"`
	Objet       libelle     `json:"objet"`
	Ventilation ventilation `json:"ventilationVotes"`
}

type libelle struct {
	Libelle string `json:"libelle"`
}

type ventilation struct {
	Organe struct {
		Groupes struct {
			Groupe groupeList `json:"groupe"`
		} `json:"groupes"`
	} `json:"organe"`
}

type groupe struct {
	Vote struct {
		DecompteNominatif *decompteNominatif `json:"decompteNominatif"`
	} `json:"vote"`
}

// groupeList tolerates the same single-element-as-object quirk as votantList: a
// scrutin in which only one political group took part serializes "groupe" as a
// bare object rather than a one-element array. Without this, json.Unmarshal
// rejects such a scrutin outright and aborts the whole ingest run.
type groupeList []groupe

func (l *groupeList) UnmarshalJSON(data []byte) error {
	return unmarshalSingleOrArray(data, (*[]groupe)(l))
}

// decompteNominatif holds the four nominative lists. Each is nil when the scrutin
// records no voter in that category (the source emits null), so a nil list simply
// contributes no rows.
type decompteNominatif struct {
	Pours       *votantBucket `json:"pours"`
	Contres     *votantBucket `json:"contres"`
	Abstentions *votantBucket `json:"abstentions"`
	NonVotants  *votantBucket `json:"nonVotants"`
}

type votantBucket struct {
	Votant votantList `json:"votant"`
}

type votant struct {
	ActeurRef string `json:"acteurRef"`
}

// votantList tolerates the source's single-element-as-object quirk: a list with
// one voter is serialized as a bare object, not a one-element array. It always
// decodes to a slice so callers range over it uniformly.
type votantList []votant

func (l *votantList) UnmarshalJSON(data []byte) error {
	return unmarshalSingleOrArray(data, (*[]votant)(l))
}

// unmarshalSingleOrArray decodes a field the AN tooling renders as either a JSON
// array or, when it holds exactly one element, a bare object. null and empty
// input decode to a nil slice. It is the one place the XML-to-JSON
// single-element quirk is handled, shared by every repeated element (votant,
// groupe).
func unmarshalSingleOrArray[T any](data []byte, out *[]T) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*out = nil
		return nil
	}
	if trimmed[0] == '[' {
		return json.Unmarshal(trimmed, out)
	}
	var single T
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return err
	}
	*out = []T{single}
	return nil
}

// ParseScrutin decodes one AN scrutin file ({"scrutin": {...}}) into one
// domain.VotingRecord per deputy named in the nominative decompte. A scrutin
// published without a nominative decompte yields zero records and no error.
// PersonName falls back to the actor ref because the scrutin file carries only
// the ref; the deputy's display name lives in the separate actors dataset and a
// later enrichment pass can backfill it.
func ParseScrutin(data []byte) ([]domain.VotingRecord, error) {
	var file scrutinFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("votingrecord: decode scrutin: %w", err)
	}
	s := file.Scrutin

	if s.UID == "" {
		return nil, fmt.Errorf("votingrecord: scrutin id is empty")
	}
	if s.Legislature == "" || s.Numero == "" {
		return nil, fmt.Errorf("votingrecord: scrutin %q: missing legislature or numero for source url", s.UID)
	}
	votedOn, err := time.Parse(dateLayout, s.DateScrutin)
	if err != nil {
		return nil, fmt.Errorf("votingrecord: scrutin %q: parse date %q: %w", s.UID, s.DateScrutin, err)
	}
	bill := s.Objet.Libelle
	if bill == "" {
		return nil, fmt.Errorf("votingrecord: scrutin %q: empty bill objet", s.UID)
	}
	sourceURL := fmt.Sprintf("https://www.assemblee-nationale.fr/dyn/%s/scrutins/%s", s.Legislature, s.Numero)

	var records []domain.VotingRecord
	seen := make(map[string]struct{})

	add := func(bucket *votantBucket, position domain.VotePosition) error {
		if bucket == nil {
			return nil
		}
		for _, v := range bucket.Votant {
			if v.ActeurRef == "" {
				return fmt.Errorf("votingrecord: scrutin %q: %s voter with empty acteurRef", s.UID, position)
			}
			if _, dup := seen[v.ActeurRef]; dup {
				return fmt.Errorf("votingrecord: scrutin %q: actor %q recorded more than once", s.UID, v.ActeurRef)
			}
			seen[v.ActeurRef] = struct{}{}
			records = append(records, domain.VotingRecord{
				PersonID:   v.ActeurRef,
				PersonName: v.ActeurRef,
				Chamber:    domain.ChamberAssemblee,
				ScrutinID:  s.UID,
				BillTitle:  bill,
				VotedOn:    votedOn,
				Position:   position,
				SourceURL:  sourceURL,
			})
		}
		return nil
	}

	for _, g := range s.Ventilation.Organe.Groupes.Groupe {
		dn := g.Vote.DecompteNominatif
		if dn == nil {
			continue
		}
		for _, p := range []struct {
			bucket   *votantBucket
			position domain.VotePosition
		}{
			{dn.Pours, domain.VoteFor},
			{dn.Contres, domain.VoteAgainst},
			{dn.Abstentions, domain.VoteAbstain},
			{dn.NonVotants, domain.VoteAbsent},
		} {
			if err := add(p.bucket, p.position); err != nil {
				return nil, err
			}
		}
	}

	return records, nil
}
