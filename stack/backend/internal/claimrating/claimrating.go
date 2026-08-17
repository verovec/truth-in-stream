// Package claimrating is the shared, reviewable rating-normalisation table every
// claim-corpus producer folds an outlet's heterogeneous verdict through: the
// Google Fact Check Tools API path, the DataCommons ClaimReview feed, the
// ClaimReview JSON-LD outlet reader, and the ClaimsKG seed. The mapping lives in
// ratings.json as data, not code, so adding an outlet's phrasing is a reviewable
// data edit. A textual rating is matched case- and accent-insensitively, longest
// folded phrase first, so a negated or qualified form ("incorrect", "plutôt
// faux", "mostly true") wins over the bare token it contains ("correct", "faux",
// "true"). When a rating maps to none of the phrases the numeric scale is tried,
// and failing that the caller stores the record as unverifiable rather than
// guessing a verdict — the corpus prefers "unverifiable" over a wrong borrowed
// answer.
package claimrating

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

//go:embed ratings.json
var ratingsData []byte

// table is the parsed, folded, longest-first rating table, built once at init.
var table ratingTable

type ratingFile struct {
	Phrases []struct {
		Phrase  string `json:"phrase"`
		Verdict string `json:"verdict"`
	} `json:"phrases"`
	Numeric struct {
		LowFraction  float64 `json:"low_fraction"`
		HighFraction float64 `json:"high_fraction"`
	} `json:"numeric"`
}

type foldedPhrase struct {
	folded  string
	tokens  []string
	verdict domain.LiteralVerdict
}

type ratingTable struct {
	phrases      []foldedPhrase
	lowFraction  float64
	highFraction float64
}

// negators are the words that invert a following accuracy token. A bare positive
// or negative token preceded by one of these (and not part of an explicit negated
// phrase in the table) is treated as ambiguous and rejected, so "not true"/"not
// fake" never fold to a guessed — let alone inverted — verdict.
var negators = map[string]struct{}{
	"not": {}, "no": {}, "non": {}, "ne": {}, "pas": {}, "never": {}, "sans": {},
}

func isNegator(tok string) bool {
	_, ok := negators[tok]
	return ok
}

func init() {
	t, err := load(ratingsData)
	if err != nil {
		panic(fmt.Sprintf("claimrating: %v", err))
	}
	table = t
}

// load parses and validates the embedded table: every verdict must be a valid
// literal verdict, and phrases are sorted longest-folded-first so a substring scan
// matches the most specific phrase. A malformed table panics at init rather than
// silently mis-mapping every rating.
func load(data []byte) (ratingTable, error) {
	var rf ratingFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return ratingTable{}, fmt.Errorf("parse ratings table: %w", err)
	}
	if rf.Numeric.LowFraction <= 0 || rf.Numeric.HighFraction >= 1 || rf.Numeric.LowFraction >= rf.Numeric.HighFraction {
		return ratingTable{}, fmt.Errorf("invalid numeric band low=%v high=%v", rf.Numeric.LowFraction, rf.Numeric.HighFraction)
	}
	phrases := make([]foldedPhrase, 0, len(rf.Phrases))
	for _, p := range rf.Phrases {
		folded := Fold(p.Phrase)
		if folded == "" {
			return ratingTable{}, fmt.Errorf("empty phrase in ratings table")
		}
		v := domain.LiteralVerdict(p.Verdict)
		if !v.Valid() {
			return ratingTable{}, fmt.Errorf("phrase %q has invalid verdict %q", p.Phrase, p.Verdict)
		}
		phrases = append(phrases, foldedPhrase{folded: folded, tokens: strings.Fields(folded), verdict: v})
	}
	// Longest phrase first (by token count, then character length), so a qualified or
	// negated form ("mostly false", "not true") wins over the bare token it contains.
	sort.SliceStable(phrases, func(i, j int) bool {
		if len(phrases[i].tokens) != len(phrases[j].tokens) {
			return len(phrases[i].tokens) > len(phrases[j].tokens)
		}
		return len(phrases[i].folded) > len(phrases[j].folded)
	})
	return ratingTable{phrases: phrases, lowFraction: rf.Numeric.LowFraction, highFraction: rf.Numeric.HighFraction}, nil
}

// accentFolder strips the French accents (and a few common Latin-1 variants) that
// differ between outlets, so one verdict spelled several ways folds to one key.
var accentFolder = strings.NewReplacer(
	"à", "a", "â", "a", "ä", "a", "á", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "í", "i",
	"ô", "o", "ö", "o", "ó", "o",
	"ù", "u", "û", "u", "ü", "u", "ú", "u",
	"ç", "c", "ñ", "n",
	"-", " ", "_", " ",
)

// Fold lower-cases, strips accents, collapses hyphens/underscores to spaces, and
// squeezes whitespace, so "Plutôt vrai", "plutot-vrai", and "PLUTÔT  VRAI" all
// normalise to one key. It is exported so the table's data can be authored in
// readable form and callers fold their input identically.
func Fold(rating string) string {
	rating = accentFolder.Replace(strings.ToLower(strings.TrimSpace(rating)))
	return strings.Join(strings.Fields(rating), " ")
}

// Lookup maps an outlet's textual rating onto the literal accuracy axis, returning
// ok=false when the rating is empty or matches no phrase. Matching is on WHOLE
// tokens (not naive substrings), longest phrase first, so a qualified form wins over
// the token it contains and "true" never matches inside "untrue". When a matched
// bare token is negated by a preceding negator ("not true", "not fake") and the
// phrase is not itself an explicit negated entry, the rating is rejected as
// ambiguous (ok=false) rather than folded to a guessed or inverted verdict — the
// caller then stores it as unverifiable.
func Lookup(rating string) (domain.LiteralVerdict, bool) {
	tokens := strings.Fields(Fold(rating))
	if len(tokens) == 0 {
		return "", false
	}
	for _, p := range table.phrases {
		at := tokenIndex(tokens, p.tokens)
		if at < 0 {
			continue
		}
		// Negation guard: a bare token whose immediately preceding token is a negator
		// (and whose own phrase does not begin with that negator) is an inverted or
		// double-negated rating; reject the whole rating rather than risk the inverse.
		if at > 0 && isNegator(tokens[at-1]) && !isNegator(p.tokens[0]) {
			return "", false
		}
		return p.verdict, true
	}
	return "", false
}

// tokenIndex returns the start index of the first occurrence of sub as a contiguous
// run of whole tokens in tokens, or -1. Whole-token matching is what makes "true"
// not match inside "untrue" and "correct" not match inside "incorrect".
func tokenIndex(tokens, sub []string) int {
	if len(sub) == 0 || len(sub) > len(tokens) {
		return -1
	}
	for i := 0; i+len(sub) <= len(tokens); i++ {
		match := true
		for j := range sub {
			if tokens[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// NumericRating is one record's optional numeric scale. In the schema.org
// ClaimReview convention a lower Value is more false; Best and Worst bound the
// scale. The *Set flags distinguish an absent field from a zero value.
type NumericRating struct {
	Value, Best, Worst          float64
	ValueSet, BestSet, WorstSet bool
}

// MapNumeric maps the numeric scale onto the accuracy axis, returning ok=false
// when there is no usable scale or the value falls in the ambiguous middle band.
// Worst defaults to 1 (the common scale floor) when absent; a degenerate or
// inverted scale (best <= worst) is treated as unmapped.
func MapNumeric(n NumericRating) (domain.LiteralVerdict, bool) {
	if !n.ValueSet || !n.BestSet {
		return "", false
	}
	worst := 1.0
	if n.WorstSet {
		worst = n.Worst
	}
	if n.Best <= worst {
		return "", false
	}
	frac := (n.Value - worst) / (n.Best - worst)
	switch {
	case frac <= table.lowFraction:
		return domain.LiteralInaccurate, true
	case frac >= table.highFraction:
		return domain.LiteralAccurate, true
	default:
		return "", false
	}
}

// Normalize maps a record's rating onto the literal accuracy axis: the textual
// rating first, then the numeric scale, and finally the conservative unverifiable
// fallback. The bool reports whether either path mapped the rating, so a caller
// can count the unverifiable fallbacks. An unmapped rating is stored as
// unverifiable, never guessed.
func Normalize(textual string, numeric NumericRating) (domain.LiteralVerdict, bool) {
	if v, ok := Lookup(textual); ok {
		return v, true
	}
	if v, ok := MapNumeric(numeric); ok {
		return v, true
	}
	return domain.LiteralUnverifiable, false
}
