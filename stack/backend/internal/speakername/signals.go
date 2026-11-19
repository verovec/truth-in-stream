package speakername

import (
	"cmp"
	"slices"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// minPartialNameLen is the shortest single word of a multi-word name that may
// stand for the whole name ("Durand" for "Marine Durand").
const minPartialNameLen = 3

// replyHeadWords bounds how close to the start of a turn a name must sit to
// read as an answer to the previous voice.
const replyHeadWords = 4

// nameParticles are name words too common to identify anyone on their own.
var nameParticles = map[string]bool{
	"de": true, "du": true, "des": true, "le": true, "la": true, "les": true,
	"van": true, "von": true, "der": true, "den": true, "del": true, "the": true,
}

// selfReferencePrefixes are the first-person openings that, placed right before
// a name, make the turn a self-introduction.
var selfReferencePrefixes = [][]string{
	{"je", "m", "appelle"},
	{"je", "suis"},
	{"moi", "c", "est"},
	{"my", "name", "is"},
	{"i", "am"},
	{"i", "m"},
}

// closingWords mark a mention that closes the previous speaker's turn ("Merci
// Monsieur Durand") rather than handing the floor to the next one.
var closingWords = map[string]bool{"merci": true, "thank": true, "thanks": true}

// closingBridgeWords may sit between a closing word and the name it applies to.
var closingBridgeWords = map[string]bool{
	"monsieur": true, "madame": true, "mademoiselle": true, "m": true, "mme": true,
	"mr": true, "mrs": true, "ms": true, "docteur": true, "dr": true, "you": true, "beaucoup": true,
}

// nameForm is one way a participant is referred to: the full name, or a single
// distinctive word of it.
type nameForm struct {
	words   []string
	name    int
	partial bool
}

// nameIndex finds participant mentions in tokenized text.
type nameIndex struct {
	forms []nameForm
}

// newNameIndex builds the mention forms for names. A single word stands for a
// multi-word name only when no other participant shares it, so "Durand" never
// resolves when two Durands are present.
func newNameIndex(names []string) nameIndex {
	words := make([][]string, len(names))
	owners := make(map[string]int)
	for i, name := range names {
		words[i] = foldedWords(name)
		for _, w := range slices.Compact(slices.Sorted(slices.Values(words[i]))) {
			owners[w]++
		}
	}
	var forms []nameForm
	for i, ws := range words {
		if len(ws) == 0 {
			continue
		}
		forms = append(forms, nameForm{words: ws, name: i})
		if len(ws) == 1 {
			continue
		}
		for _, w := range ws {
			if len(w) < minPartialNameLen || nameParticles[w] || owners[w] != 1 {
				continue
			}
			forms = append(forms, nameForm{words: []string{w}, name: i, partial: true})
		}
	}
	slices.SortStableFunc(forms, func(x, y nameForm) int { return cmp.Compare(len(y.words), len(x.words)) })
	return nameIndex{forms: forms}
}

// mention is one occurrence of a participant's name, as the token range
// [from, to).
type mention struct {
	name     int
	from, to int
}

// mentions returns the name occurrences in tokens, longest form first at each
// position. A single-word form must be capitalized as heard, so a surname that
// is also a common word does not match mid-sentence prose.
func (ix nameIndex) mentions(tokens []token) []mention {
	var found []mention
	for i := 0; i < len(tokens); {
		matched := false
		for _, form := range ix.forms {
			if len(form.words) == 1 && !tokens[i].upper {
				continue
			}
			if hasWordsAt(tokens, i, form.words) {
				found = append(found, mention{name: form.name, from: i, to: i + len(form.words)})
				i += len(form.words)
				matched = true
				break
			}
		}
		if !matched {
			i++
		}
	}
	return found
}

// score reads every turn for name mentions and credits the label each mention
// points at:
//
//   - self-reference: the speaker introduces themself, credit the speaker;
//   - closing: the mention thanks someone, credit the previous voice;
//   - handoff: the mention ends the turn, credit the next voice;
//   - reply: the mention opens a longer turn, credit the previous voice, the
//     one being answered;
//   - vocative adjacency: any other mention, credit both neighboring voices.
//
// The speaker of a mention is never credited for merely uttering a name:
// diarization bleeds a moderator's sentence into a candidate's turn, which
// makes raw utterance counts misleading.
func score(turns []Turn, ix nameIndex) *scoreboard {
	board := &scoreboard{scores: make(map[pairKey]float64), evidence: make(map[pairKey][]weightedEvidence)}
	for i, turn := range turns {
		tokens := tokenize(turn.Text)
		prev, next := neighborLabel(turns, i, -1), neighborLabel(turns, i, 1)
		for _, m := range ix.mentions(tokens) {
			ev := domain.SpeakerEvidence{Quote: excerpt(turn.Text, tokens, m.from, m.to), At: turn.Start}
			switch {
			case isSelfReference(tokens, m.from):
				ev.Kind = domain.SpeakerEvidenceSelfReference
				board.add(turn.Label, m.name, weightSelfReference, ev)
			case isClosing(tokens, m.from):
				ev.Kind = domain.SpeakerEvidenceVocative
				if prev != "" {
					board.add(prev, m.name, weightVocative, ev)
				}
			case len(tokens)-m.to <= handoffTailWords && next != "":
				ev.Kind = domain.SpeakerEvidenceHandoff
				board.add(next, m.name, weightHandoff, ev)
			case m.from < replyHeadWords && prev != "":
				ev.Kind = domain.SpeakerEvidenceVocative
				board.add(prev, m.name, weightVocative, ev)
			default:
				ev.Kind = domain.SpeakerEvidenceVocative
				if prev != "" {
					board.add(prev, m.name, weightVocative, ev)
				}
				if next != "" && next != prev {
					board.add(next, m.name, weightVocative, ev)
				}
			}
		}
	}
	return board
}

// neighborLabel returns the label of the turn at offset from i, or "" when
// there is none or it is the same voice.
func neighborLabel(turns []Turn, i, offset int) string {
	j := i + offset
	if j < 0 || j >= len(turns) || turns[j].Label == turns[i].Label {
		return ""
	}
	return turns[j].Label
}

func isSelfReference(tokens []token, from int) bool {
	for _, prefix := range selfReferencePrefixes {
		if hasWordsAt(tokens, from-len(prefix), prefix) {
			return true
		}
	}
	return false
}

// isClosing reports whether a closing word precedes the mention, allowing
// bridge words in between ("Merci beaucoup Madame Martin").
func isClosing(tokens []token, from int) bool {
	for i := from - 1; i >= 0; i-- {
		if closingWords[tokens[i].norm] {
			return true
		}
		if !closingBridgeWords[tokens[i].norm] {
			return false
		}
	}
	return false
}
