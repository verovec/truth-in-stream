package service

import (
	"context"
	"strings"
	"unicode"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// HeuristicClassifier decides claim-worthiness with deterministic linguistic
// rules and no external dependency. It is the cheap first stage of the
// check-worthiness gate: reject everything that is plainly not a declarative
// factual assertion (questions, opinions, hypotheticals, predictions,
// greetings, filler, and fragments) so only candidates reach the expensive
// corpus-coverage stage. It biases to precision - when a segment does not look
// like a clean factual claim it is rejected - because a wrongly-skipped claim
// is cheaper than a verdict on un-checkable speech.
//
// The rejection lexicons are locale-keyed: the English lists always apply, and
// the French locale adds the French lists on top, so a French session rejects
// its own filler for free while the default locale behaves exactly as before.
//
// A model-based classifier can replace this behind the ClaimClassifier
// interface if accuracy ever demands it; the gate does not care which is wired.
type HeuristicClassifier struct {
	minWords int
	leading  map[string]struct{}
	phrases  []string
}

// NewHeuristicClassifier builds a classifier that rejects any segment with
// fewer than minWords words as an incomplete fragment, using the rejection
// lexicons of the given locale.
func NewHeuristicClassifier(minWords int, locale domain.Locale) *HeuristicClassifier {
	c := &HeuristicClassifier{minWords: minWords, leading: leadingNonClaim, phrases: nonClaimPhrases}
	if locale.IsFrench() {
		merged := make(map[string]struct{}, len(leadingNonClaim)+len(leadingNonClaimFrench))
		for w := range leadingNonClaim {
			merged[w] = struct{}{}
		}
		for w := range leadingNonClaimFrench {
			merged[w] = struct{}{}
		}
		c.leading = merged
		c.phrases = make([]string, 0, len(nonClaimPhrases)+len(nonClaimPhrasesFrench))
		c.phrases = append(append(c.phrases, nonClaimPhrases...), nonClaimPhrasesFrench...)
	}
	return c
}

// leadingNonClaim holds first words that mark a segment as a question,
// greeting, filler, or hypothetical rather than an assertion. Interrogative
// wh-words and yes/no auxiliaries flag questions; the interjections flag
// greetings and filler; "if" flags a hypothetical.
var leadingNonClaim = map[string]struct{}{
	// Interrogatives.
	"who": {}, "what": {}, "whats": {}, "when": {}, "where": {}, "why": {},
	"how": {}, "whom": {}, "whose": {}, "which": {}, "whos": {},
	// Yes/no question auxiliaries (also lead most imperatives).
	"is": {}, "are": {}, "was": {}, "were": {}, "am": {}, "do": {}, "does": {},
	"did": {}, "can": {}, "could": {}, "would": {}, "will": {}, "shall": {},
	"should": {}, "have": {}, "has": {}, "had": {}, "may": {}, "might": {},
	"must": {},
	// Greetings, interjections, and filler.
	"hi": {}, "hello": {}, "hey": {}, "hiya": {}, "yo": {}, "thanks": {},
	"thank": {}, "please": {}, "welcome": {}, "bye": {}, "goodbye": {},
	"cheers": {}, "ok": {}, "okay": {}, "yeah": {}, "yep": {}, "yup": {},
	"nope": {}, "nah": {}, "um": {}, "uh": {}, "hmm": {}, "oh": {},
	// Hypothetical.
	"if": {},
}

// leadingNonClaimFrench holds the French leading markers, in folded letters-only
// form (see wordLetters): apostrophes and hyphens vanish, so "Est-ce" looks up
// as "estce" and "D'accord" as "daccord". Ambiguous openers that routinely
// precede a real claim are deliberately absent, because a false reject silently
// drops a claim: "alors", "bon", "donc", "ecoutez", "oui", "non", and also
// "que"/"qui"/"quoi"/"quand", which open declarative constructions in spoken
// French ("Quand on regarde les chiffres, ...", "Que X soit vrai est ...").
var leadingNonClaimFrench = map[string]struct{}{
	// Interrogatives.
	"pourquoi": {}, "comment": {}, "combien": {}, "ou": {},
	"quel": {}, "quelle": {}, "quels": {}, "quelles": {},
	// Est-ce que family and subject-verb inversions.
	"estce": {}, "questce": {}, "estil": {}, "estelle": {}, "sontils": {},
	"sontelles": {}, "atil": {}, "atelle": {}, "ontils": {}, "ontelles": {},
	"yatil": {}, "fautil": {}, "peuton": {}, "doiton": {}, "vatil": {},
	"vatelle": {},
	// Greetings, interjections, and filler.
	"bonjour": {}, "bonsoir": {}, "salut": {}, "coucou": {}, "merci": {},
	"bienvenue": {}, "bravo": {}, "euh": {}, "heu": {}, "hein": {}, "bah": {},
	"ben": {}, "ouais": {}, "voila": {}, "daccord": {},
	// Hypothetical.
	"si": {}, "sil": {},
}

// nonClaimPhrases holds multi-word markers of opinion, hedging, or prediction
// found anywhere in the segment. Each is matched on word boundaries, so
// "might" never fires inside "mighty".
var nonClaimPhrases = []string{
	// Opinion and subjectivity.
	"i think", "i believe", "i feel", "i guess", "i suppose", "i reckon",
	"in my opinion", "in my view", "if you ask me", "imo", "imho",
	// Hedging and prediction.
	"maybe", "perhaps", "probably", "possibly", "likely", "might",
	"could be", "going to", "gonna", "i bet", "i predict", "i expect",
	"i hope", "i wish",
}

// nonClaimPhrasesFrench holds the French phrase markers in folded scan form
// (see scanText): diacritics dropped and apostrophes collapsed to spaces, so
// "j'espère" is listed as "j espere". "peut etre" is deliberately absent: the
// folded text cannot tell "peut-être" (maybe) from "peut être" (can be), and a
// false reject on the verb form would drop a real claim. Bare "selon" is also
// absent - only "selon moi" is subjective; "selon l'INSEE" attributes a fact.
var nonClaimPhrasesFrench = []string{
	// Opinion and subjectivity.
	"je pense", "je crois", "je trouve que", "je suppose", "j estime que",
	"a mon avis", "selon moi", "d apres moi", "a mon sens", "pour ma part",
	"il me semble", "j ai l impression", "je suis convaincu", "je suis persuade",
	"personnellement",
	// Hedging and prediction.
	"sans doute", "probablement", "surement", "vraisemblablement", "apparemment",
	"on dirait", "il parait que", "soi disant", "je parie", "je predis",
	"j espere", "je souhaite", "j imagine", "je m attends a",
	// Questions and procedural politeness. The farewell phrases match anywhere
	// in the segment: in the broadcast register they only occur as sign-offs,
	// not inside factual assertions, so anywhere-matching is precision-safe.
	"est ce que", "s il vous plait", "je vous en prie", "au revoir",
	"a bientot", "bonne journee", "bonne soiree",
}

// Classify reports whether text is a checkable declarative factual assertion.
// The judgment is deterministic and local, so the context is unused and the
// error is always nil; both exist to satisfy the ClaimClassifier interface a
// model-backed classifier also implements.
func (c *HeuristicClassifier) Classify(_ context.Context, text string) (bool, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false, nil
	}
	if strings.HasSuffix(trimmed, "?") {
		return false, nil
	}

	fields := strings.Fields(trimmed)
	if len(fields) < c.minWords {
		return false, nil
	}
	if _, bad := c.leading[wordLetters(fields[0])]; bad {
		return false, nil
	}
	// Inversion markers can split across tokens ("Y a-t-il", "Est ce que"), so
	// the joined first two tokens get the same lookup as the first alone.
	if len(fields) >= 2 {
		if _, bad := c.leading[wordLetters(fields[0])+wordLetters(fields[1])]; bad {
			return false, nil
		}
	}

	scan := scanText(trimmed)
	for _, phrase := range c.phrases {
		if strings.Contains(scan, " "+phrase+" ") {
			return false, nil
		}
	}
	return true, nil
}

// foldRune lowercases a rune and strips the French diacritics, so "Sûrement"
// and "surement" normalize identically. Live transcription is inconsistent
// about accents, and the lexicons are stored folded, so folding here keeps a
// marker matching either spelling. A rune table beats a text/transform pass
// because it runs inside the existing single-pass loops with no allocation.
func foldRune(r rune) rune {
	switch r = unicode.ToLower(r); r {
	case 'à', 'â', 'ä':
		return 'a'
	case 'é', 'è', 'ê', 'ë':
		return 'e'
	case 'î', 'ï':
		return 'i'
	case 'ô', 'ö':
		return 'o'
	case 'ù', 'û', 'ü':
		return 'u'
	case 'ç':
		return 'c'
	}
	return r
}

// wordLetters folds a token and drops everything but its letters, so
// "Hello,", "What's", and "Est-ce" reduce to "hello", "whats", and "estce"
// for leading-word lookup.
func wordLetters(token string) string {
	var b strings.Builder
	b.Grow(len(token))
	for _, r := range token {
		if unicode.IsLetter(r) {
			b.WriteRune(foldRune(r))
		}
	}
	return b.String()
}

// scanText normalizes text for word-boundary phrase matching: folded (see
// foldRune), every non-alphanumeric run collapsed to a single space, and padded
// with leading and trailing spaces so a phrase at either end still matches
// " phrase ". Apostrophe elision collapses too, so "j'espère" scans as
// "j espere".
func scanText(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 2)
	b.WriteByte(' ')
	prevSpace := true
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(foldRune(r))
			prevSpace = false
			continue
		}
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	if !prevSpace {
		b.WriteByte(' ')
	}
	return b.String()
}
