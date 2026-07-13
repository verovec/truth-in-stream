package parliament

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsjob"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

// senatScrutinURLTemplate is the official Senat scrutin-public page for a vote,
// each record's provenance link: scrutin-public/<year>/scr<year>-<num>.html.
const senatScrutinURLTemplate = "https://www.senat.fr/scrutin-public/%s/scr%s-%s.html"

// senatSenator is a senator's display name and group, resolved from the dump's
// auteur table by matricule (autmat), which is the key votsen.senmat references.
type senatSenator struct {
	name  string
	group string
}

// senatScrutinAcc accumulates one scrutin's metadata and its votes while the dump
// streams; the votes arrive (in votsen) after the scrutin header (in scr).
type senatScrutinAcc struct {
	sesann string
	scrnum string
	objet  string
	date   string
	votes  []senatVoteRow
}

type senatVoteRow struct {
	matricule string
	position  string
}

// bufferedVote is one votsen row held until the whole dump has streamed, so the
// scr <-> votsen join does not depend on the order the COPY blocks appear in.
type bufferedVote struct {
	key       string // "<sesann>-<scrnum>"
	matricule string
	posvotcod string
}

// extractSenatScrutins streams the Senat dosleg PostgreSQL dump and joins its
// scr (scrutins), votsen (per-senator votes), posvot (position labels), and auteur
// (senator names) tables into one publishable scrutin per vote. sinceYear bounds
// volume to recent sessions (0 = every session); the card's "start with the current
// legislature" is an operator setting PARLIAMENT_SINCE_YEAR.
//
// The join is order-independent: votsen rows are buffered during the pass and
// resolved against the scr/posvot/auteur maps only after the whole stream completes
// (exactly as auteur and typloi already resolve post-stream). So the dump's COPY
// table order does not matter, and a votsen row is dropped only when its scrutin is
// genuinely absent from scr (a dump inconsistency) or filtered by sinceYear - never
// merely because votsen happened to stream before scr.
func extractSenatScrutins(archivePath string, sinceYear int) ([]scrutinPayload, error) {
	posvot := make(map[string]string)
	senators := make(map[string]senatSenator)
	scrutins := make(map[string]*senatScrutinAcc)
	var order []string
	var buffered []bufferedVote
	idxCache := make(map[string]map[string]int)

	want := map[string]bool{"posvot": true, "auteur": true, "scr": true, "votsen": true}
	err := streamCopyFromDumpZip(archivePath, want, func(table string, cols, fields []string) error {
		idx, ok := idxCache[table]
		if !ok {
			idx = colIndex(cols)
			idxCache[table] = idx
		}
		switch table {
		case "posvot":
			if code := field(idx, fields, "posvotcod"); code != "" {
				posvot[code] = field(idx, fields, "posvotlib")
			}
		case "auteur":
			mat := field(idx, fields, "autmat")
			nom := field(idx, fields, "nomuse")
			if mat == "" || nom == "" {
				return nil
			}
			if _, exists := senators[mat]; !exists {
				senators[mat] = senatSenator{
					name:  strings.TrimSpace(field(idx, fields, "prenom") + " " + nom),
					group: field(idx, fields, "grpapp"),
				}
			}
		case "scr":
			year := field(idx, fields, "sesann")
			if !yearInScope(year, sinceYear) {
				return nil
			}
			num := field(idx, fields, "scrnum")
			objet := field(idx, fields, "scrint")
			date := senatDate(field(idx, fields, "scrdat"))
			if num == "" || objet == "" || date == "" {
				return nil
			}
			key := year + "-" + num
			if _, exists := scrutins[key]; !exists {
				scrutins[key] = &senatScrutinAcc{sesann: year, scrnum: num, objet: objet, date: date}
				order = append(order, key)
			}
		case "votsen":
			// Bound the buffer by the same session-year filter using the year on the
			// vote row itself, so an out-of-scope vote is dropped here (before scr is
			// even needed) rather than inflating memory.
			year := field(idx, fields, "sesann")
			if !yearInScope(year, sinceYear) {
				return nil
			}
			mat := field(idx, fields, "senmat")
			code := field(idx, fields, "posvotcod")
			if mat == "" || code == "" {
				return nil
			}
			buffered = append(buffered, bufferedVote{key: year + "-" + field(idx, fields, "scrnum"), matricule: mat, posvotcod: code})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Resolve the buffered votes now that every table is loaded. A vote whose scrutin
	// is absent from scr, or whose position code is unknown, is a genuine dump
	// inconsistency and is skipped; table order plays no part.
	for _, v := range buffered {
		acc, ok := scrutins[v.key]
		if !ok {
			continue
		}
		position, ok := posvot[v.posvotcod]
		if !ok || position == "" {
			continue
		}
		acc.votes = append(acc.votes, senatVoteRow{matricule: v.matricule, position: position})
	}

	payloads := make([]scrutinPayload, 0, len(order))
	for _, key := range order {
		acc := scrutins[key]
		if len(acc.votes) == 0 {
			continue // a scrutin with no recorded nominative votes carries no evidence
		}
		payload, err := buildSenatScrutinPayload(acc, senators)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, payload)
	}
	return payloads, nil
}

// buildSenatScrutinPayload renders one accumulated scrutin into a chamber-aware
// scrutins job body plus its stable id and content fingerprint (for the manifest
// diff). Senator names are resolved from the auteur map, falling back to the
// matricule when the dump has no name row.
func buildSenatScrutinPayload(acc *senatScrutinAcc, senators map[string]senatSenator) (scrutinPayload, error) {
	scrutinID := "senat-" + acc.sesann + "-" + acc.scrnum
	url := fmt.Sprintf(senatScrutinURLTemplate, acc.sesann, acc.sesann, acc.scrnum)

	votes := make([]votingrecord.SenatVote, 0, len(acc.votes))
	var fp strings.Builder
	for _, v := range acc.votes {
		s := senators[v.matricule]
		name := s.name
		if name == "" {
			name = v.matricule
		}
		votes = append(votes, votingrecord.SenatVote{
			PersonID: v.matricule, PersonName: name, Group: s.group, Position: v.position,
		})
		fp.WriteString(v.matricule)
		fp.WriteByte(':')
		fp.WriteString(v.position)
		fp.WriteByte(';')
	}

	scrutinJSON, err := json.Marshal(votingrecord.SenatScrutin{
		ScrutinID: scrutinID, Objet: acc.objet, Date: acc.date, SourceURL: url, Votes: votes,
	})
	if err != nil {
		return scrutinPayload{}, fmt.Errorf("parliament: encode senat scrutin %q: %w", scrutinID, err)
	}
	body, err := json.Marshal(scrutinsjob.ScrutinJob{
		ID: scrutinID, Chamber: string(domain.ChamberSenat), Scrutin: scrutinJSON,
	})
	if err != nil {
		return scrutinPayload{}, fmt.Errorf("parliament: encode senat scrutin job %q: %w", scrutinID, err)
	}
	return scrutinPayload{
		id:          scrutinID,
		fingerprint: fingerprint(scrutinID, acc.objet, acc.date, fp.String()),
		body:        body,
	}, nil
}

// senatDate trims the dump's "YYYY-MM-DD HH:MM:SS" timestamp to the ISO calendar
// date the voting store holds.
func senatDate(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

// yearInScope reports whether a scrutin's session year passes the volume bound. A
// sinceYear of zero or a non-numeric year admits everything.
func yearInScope(year string, sinceYear int) bool {
	if sinceYear <= 0 {
		return true
	}
	y, err := strconv.Atoi(year)
	if err != nil {
		return true
	}
	return y >= sinceYear
}
