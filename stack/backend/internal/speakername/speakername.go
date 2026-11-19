// Package speakername maps the diarized speaker labels of a transcript to the
// people behind them. The decision is deterministic: it scores every (label,
// name) pair from three transcript signals - handoffs, vocative adjacency and
// self-reference - fuses them under a confidence floor, and leaves a label
// unnamed rather than guess. A generative model is used only at the edges (to
// list the participant names and to break a tie the signals leave open), and
// everything it returns is validated against the transcript here.
package speakername

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ConfidenceFloor is the confidence below which a label stays unnamed: a wrong
// name on a statement is worse than a letter.
const ConfidenceFloor = 0.6

const (
	weightSelfReference = 4.0
	weightHandoff       = 3.0
	weightVocative      = 1.0
	// strongEvidence is the score at which evidence volume stops limiting
	// confidence: two handoffs, or a self-introduction backed by two
	// vocatives. A single handoff alone never clears the floor.
	strongEvidence = 6.0
	// handoffTailWords bounds how close to the end of a turn a name must sit
	// to count as handing the floor to the next voice.
	handoffTailWords    = 8
	excerptContextWords = 8
	maxEvidence         = 5
	maxContenders       = 3
	// contenderMinScore and contenderRatio decide which names an unnamed label
	// offers to the tie-break: enough evidence to be plausible, and close
	// enough to the label's best candidate.
	contenderMinScore = 2.0
	contenderRatio    = 0.5
	minTieBreakQuote  = 12
)

// Turn is one uninterrupted stretch of speech by a single diarized label.
type Turn struct {
	Label string
	Start time.Duration
	End   time.Duration
	Text  string
}

// Participant is a name a model proposed, with the transcript or title span it
// was read from. It is untrusted until Ground accepts it.
type Participant struct {
	Name  string
	Quote string
}

// Proposal is the naming decision for one label. Name is empty when the label
// stays unnamed; Contenders then lists the names a tie-break may choose from.
type Proposal struct {
	Label      string
	Name       string
	Role       domain.SpeakerRole
	Confidence float64
	Method     domain.SpeakerMethod
	Evidence   []domain.SpeakerEvidence
	Contenders []string
}

// Turns rebuilds speaker turns from finalized transcript segments, in order:
// consecutive segments of one label merge into a single turn. A segment with no
// label or no text carries no attribution and is skipped.
func Turns(segments []domain.Segment) []Turn {
	turns := make([]Turn, 0, len(segments))
	for _, seg := range segments {
		text := strings.TrimSpace(seg.Text)
		if seg.Speaker == "" || text == "" {
			continue
		}
		if n := len(turns); n > 0 && turns[n-1].Label == seg.Speaker {
			turns[n-1].End = seg.End
			turns[n-1].Text += " " + text
			continue
		}
		turns = append(turns, Turn{Label: seg.Speaker, Start: seg.Start, End: seg.End, Text: text})
	}
	return turns
}

// Ground keeps the proposed participants the transcript or the title actually
// supports: the quote must be an exact substring of the title or of a turn, and
// must contain the name. A name known only from model memory is discarded.
// Names are returned as heard, deduplicated, and a name wholly contained in a
// longer accepted name ("Durand" beside "Marine Durand") is dropped as the same
// person.
func Ground(participants []Participant, title string, turns []Turn) []string {
	var names []string
	var folded [][]string
	for _, p := range participants {
		name := strings.TrimSpace(p.Name)
		words := foldedWords(name)
		if len(words) == 0 || !quoteSupports(p.Quote, words, title, turns) {
			continue
		}
		if slices.ContainsFunc(folded, func(w []string) bool { return slices.Equal(w, words) }) {
			continue
		}
		names = append(names, name)
		folded = append(folded, words)
	}
	kept := make([]string, 0, len(names))
	for i, name := range names {
		contained := false
		for j := range names {
			if i != j && len(folded[j]) > len(folded[i]) && containsWords(folded[j], folded[i]) {
				contained = true
				break
			}
		}
		if !contained {
			kept = append(kept, name)
		}
	}
	return kept
}

func quoteSupports(quote string, nameWords []string, title string, turns []Turn) bool {
	if strings.TrimSpace(quote) == "" || !containsWords(foldedWords(quote), nameWords) {
		return false
	}
	if strings.Contains(title, quote) {
		return true
	}
	return slices.ContainsFunc(turns, func(t Turn) bool { return strings.Contains(t.Text, quote) })
}

func containsWords(haystack, needle []string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

// Resolve decides a name and a role for every label in turns, given the
// grounded participant names. Proposals are ordered by label.
func Resolve(turns []Turn, names []string) []Proposal {
	roles := inferRoles(turns)
	board := score(turns, newNameIndex(names))

	labels := make([]string, 0, len(roles))
	for label := range roles {
		labels = append(labels, label)
	}
	slices.Sort(labels)

	held := board.assign()
	confident := make(map[int]bool, len(held))
	proposals := make([]Proposal, 0, len(labels))
	for _, label := range labels {
		p := Proposal{Label: label, Role: roles[label], Method: domain.SpeakerMethodNone}
		if name, ok := held[label]; ok {
			if conf := board.confidence(label, name); conf >= ConfidenceFloor {
				p.Name = names[name]
				p.Confidence = conf
				p.Method = domain.SpeakerMethodSignals
				p.Evidence = board.topEvidence(label, name)
				confident[name] = true
			}
		}
		proposals = append(proposals, p)
	}
	for i := range proposals {
		if proposals[i].Name == "" {
			proposals[i].Contenders = board.contenders(proposals[i].Label, names, confident)
		}
	}
	return proposals
}

// ApplyTieBreak names an unnamed label from a model's tie-break decision, and
// reports whether the decision was accepted. It is accepted only when the name
// is one of the label's contenders, no other label already holds it, and the
// quote is an exact substring of one of that label's own turns - the only text
// the model was shown, and the only speech that can identify the speaker. The result sits exactly at
// the confidence floor: a tie-break is never stronger than the signals.
func ApplyTieBreak(proposals []Proposal, turns []Turn, label, name, quote string) bool {
	target := slices.IndexFunc(proposals, func(p Proposal) bool { return p.Label == label })
	if target < 0 || proposals[target].Name != "" || !slices.Contains(proposals[target].Contenders, name) {
		return false
	}
	if slices.ContainsFunc(proposals, func(p Proposal) bool { return p.Name == name }) {
		return false
	}
	if len(strings.TrimSpace(quote)) < minTieBreakQuote {
		return false
	}
	source := slices.IndexFunc(turns, func(t Turn) bool { return t.Label == label && strings.Contains(t.Text, quote) })
	if source < 0 {
		return false
	}
	p := &proposals[target]
	p.Name = name
	p.Confidence = ConfidenceFloor
	p.Method = domain.SpeakerMethodTieBreak
	p.Evidence = []domain.SpeakerEvidence{{Kind: domain.SpeakerEvidenceTieBreak, Quote: quote, At: turns[source].Start}}
	p.Contenders = nil
	return true
}

type pairKey struct {
	label string
	name  int
}

type weightedEvidence struct {
	weight   float64
	evidence domain.SpeakerEvidence
}

// scoreboard accumulates, per (label, name) pair, the fused signal score and
// the evidence behind it.
type scoreboard struct {
	scores   map[pairKey]float64
	evidence map[pairKey][]weightedEvidence
}

func (b *scoreboard) add(label string, name int, weight float64, ev domain.SpeakerEvidence) {
	key := pairKey{label: label, name: name}
	b.scores[key] += weight
	b.evidence[key] = append(b.evidence[key], weightedEvidence{weight: weight, evidence: ev})
}

// assign gives each name to at most one label and each label at most one name,
// strongest pair first, so two voices can never share a person.
func (b *scoreboard) assign() map[string]int {
	pairs := make([]pairKey, 0, len(b.scores))
	for key := range b.scores {
		pairs = append(pairs, key)
	}
	slices.SortFunc(pairs, func(x, y pairKey) int {
		return cmp.Or(
			cmp.Compare(b.scores[y], b.scores[x]),
			cmp.Compare(x.label, y.label),
			cmp.Compare(x.name, y.name),
		)
	})
	held := make(map[string]int)
	taken := make(map[int]bool)
	for _, key := range pairs {
		if _, ok := held[key.label]; ok || taken[key.name] {
			continue
		}
		held[key.label] = key.name
		taken[key.name] = true
	}
	return held
}

// confidence is the pair's lead over its strongest rival - the best other name
// for this label, or the best other label for this name - scaled down while the
// evidence is thin. A close rival on either side is a contradiction and drives
// it to zero; diffuse adjacency noise, which a moderator neighboring every
// turn always collects, does not.
func (b *scoreboard) confidence(label string, name int) float64 {
	s := b.scores[pairKey{label: label, name: name}]
	var rival float64
	for key, v := range b.scores {
		if (key.label == label) != (key.name == name) {
			rival = max(rival, v)
		}
	}
	return (1 - rival/s) * min(1, s/strongEvidence)
}

// topEvidence returns the pair's strongest evidence, heaviest signal first and
// chronological within a signal.
func (b *scoreboard) topEvidence(label string, name int) []domain.SpeakerEvidence {
	all := slices.Clone(b.evidence[pairKey{label: label, name: name}])
	slices.SortStableFunc(all, func(x, y weightedEvidence) int {
		return cmp.Or(cmp.Compare(y.weight, x.weight), cmp.Compare(x.evidence.At, y.evidence.At))
	})
	out := make([]domain.SpeakerEvidence, 0, min(len(all), maxEvidence))
	for _, w := range all[:min(len(all), maxEvidence)] {
		out = append(out, w.evidence)
	}
	return out
}

// contenders lists the names an unnamed label could still be, strongest first,
// excluding names another label already holds with confidence.
func (b *scoreboard) contenders(label string, names []string, confident map[int]bool) []string {
	type candidate struct {
		name  int
		score float64
	}
	var candidates []candidate
	var top float64
	for key, s := range b.scores {
		if key.label != label || confident[key.name] {
			continue
		}
		candidates = append(candidates, candidate{name: key.name, score: s})
		top = max(top, s)
	}
	slices.SortFunc(candidates, func(x, y candidate) int {
		return cmp.Or(cmp.Compare(y.score, x.score), cmp.Compare(x.name, y.name))
	})
	var out []string
	for _, c := range candidates {
		if c.score < contenderMinScore || c.score < contenderRatio*top || len(out) == maxContenders {
			break
		}
		out = append(out, names[c.name])
	}
	return out
}
