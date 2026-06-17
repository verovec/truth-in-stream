package factcheckarchive

import (
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// verdictPhrases maps the normalized French textual ratings emitted by the
// indexed outlets (AFP Factuel, franceinfo Vrai ou Fake, Les Decodeurs) onto the
// literal accuracy axis. The Google Fact Check Tools API surfaces each outlet's
// raw alternateName string verbatim in textualRating, so the set is uncontrolled
// and the same verdict appears in several spellings/casings; mapVerdict folds
// case and accents before matching. Adding an outlet's new phrasing is a new
// entry here, not a code change anywhere else (open-closed via data-driven
// dispatch). Phrases are matched longest-first so "plutot faux" wins over the
// "faux" substring.
var verdictPhrases = []struct {
	phrase  string
	verdict domain.LiteralVerdict
}{
	// Unverifiable / inconclusive: checked first because some carry the word
	// "vrai"/"faux" inside a longer "cannot verify" phrasing.
	{"on n'a pas pu verifier", domain.LiteralUnverifiable},
	{"on na pas pu verifier", domain.LiteralUnverifiable},
	{"pas pu verifier", domain.LiteralUnverifiable},
	{"pas de preuve", domain.LiteralUnverifiable},
	{"inverifiable", domain.LiteralUnverifiable},
	{"non verifiable", domain.LiteralUnverifiable},
	{"invalidable", domain.LiteralUnverifiable},
	// Inaccurate. The negated forms "incorrect"/"inexact" are listed before the
	// accurate "correct"/"exact" so they win the first-match scan: a substring
	// test alone would map "incorrect" to accurate (it contains "correct").
	{"incorrect", domain.LiteralInaccurate},
	{"inexact", domain.LiteralInaccurate},
	{"plutot faux", domain.LiteralInaccurate},
	{"partiellement faux", domain.LiteralInaccurate},
	{"trompeur", domain.LiteralInaccurate},
	{"trompeuse", domain.LiteralInaccurate},
	{"faux", domain.LiteralInaccurate},
	{"fausse", domain.LiteralInaccurate},
	{"errone", domain.LiteralInaccurate},
	{"infonde", domain.LiteralInaccurate},
	// Accurate.
	{"plutot vrai", domain.LiteralAccurate},
	{"vrai", domain.LiteralAccurate},
	{"exact", domain.LiteralAccurate},
	{"correct", domain.LiteralAccurate},
}

// foldRating lower-cases the rating and strips the French accents that vary
// between outlets and feeds, so "Plutôt vrai", "plutot vrai", and "PLUTÔT VRAI"
// all normalize to one key.
func foldRating(rating string) string {
	rating = strings.ToLower(strings.TrimSpace(rating))
	replacer := strings.NewReplacer(
		"à", "a", "â", "a", "ä", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"î", "i", "ï", "i",
		"ô", "o", "ö", "o",
		"ù", "u", "û", "u", "ü", "u",
		"ç", "c",
	)
	return replacer.Replace(rating)
}

// mapVerdict maps an outlet's textual rating onto the literal verdict axis,
// returning ok=false when the rating is empty or unrecognized so the producer can
// skip the claim rather than store an empty verdict that would violate the
// column CHECK constraint.
func mapVerdict(rating string) (domain.LiteralVerdict, bool) {
	folded := foldRating(rating)
	if folded == "" {
		return "", false
	}
	for _, vp := range verdictPhrases {
		if strings.Contains(folded, vp.phrase) {
			return vp.verdict, true
		}
	}
	return "", false
}
