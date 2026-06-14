package service

import (
	"context"
	"strings"
	"unicode"
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
// A model-based classifier can replace this behind the ClaimClassifier
// interface if accuracy ever demands it; the gate does not care which is wired.
type HeuristicClassifier struct {
	minWords int
}

// NewHeuristicClassifier builds a classifier that rejects any segment with
// fewer than minWords words as an incomplete fragment.
func NewHeuristicClassifier(minWords int) *HeuristicClassifier {
	return &HeuristicClassifier{minWords: minWords}
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
	if _, bad := leadingNonClaim[wordLetters(fields[0])]; bad {
		return false, nil
	}

	scan := scanText(trimmed)
	for _, phrase := range nonClaimPhrases {
		if strings.Contains(scan, " "+phrase+" ") {
			return false, nil
		}
	}
	return true, nil
}

// wordLetters lowercases a token and drops everything but its letters, so
// "Hello," and "What's" reduce to "hello" and "whats" for leading-word lookup.
func wordLetters(token string) string {
	var b strings.Builder
	b.Grow(len(token))
	for _, r := range token {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// scanText normalizes text for word-boundary phrase matching: lowercased, every
// non-alphanumeric run collapsed to a single space, and padded with leading and
// trailing spaces so a phrase at either end still matches " phrase ".
func scanText(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 2)
	b.WriteByte(' ')
	prevSpace := true
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
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
