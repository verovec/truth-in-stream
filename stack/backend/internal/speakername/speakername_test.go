package speakername

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func turnsOf(lines ...string) []Turn {
	turns := make([]Turn, 0, len(lines))
	for i, line := range lines {
		label, text, _ := strings.Cut(line, ": ")
		start := time.Duration(i) * 10 * time.Second
		turns = append(turns, Turn{Label: label, Start: start, End: start + 10*time.Second, Text: text})
	}
	return turns
}

func proposalFor(t *testing.T, proposals []Proposal, label string) Proposal {
	t.Helper()
	for _, p := range proposals {
		if p.Label == label {
			return p
		}
	}
	t.Fatalf("no proposal for label %q", label)
	return Proposal{}
}

func TestTurns(t *testing.T) {
	t.Parallel()
	seg := func(speaker string, start, end int, text string) domain.Segment {
		return domain.Segment{Speaker: speaker, Start: time.Duration(start) * time.Second, End: time.Duration(end) * time.Second, Text: text}
	}
	tests := []struct {
		name     string
		segments []domain.Segment
		want     []Turn
	}{
		{name: "empty", segments: nil, want: []Turn{}},
		{
			name:     "consecutive segments of one label merge",
			segments: []domain.Segment{seg("A", 0, 4, "Bonsoir."), seg("A", 4, 9, "Je commence."), seg("B", 9, 12, "Merci.")},
			want: []Turn{
				{Label: "A", Start: 0, End: 9 * time.Second, Text: "Bonsoir. Je commence."},
				{Label: "B", Start: 9 * time.Second, End: 12 * time.Second, Text: "Merci."},
			},
		},
		{
			name:     "unlabelled and blank segments are skipped",
			segments: []domain.Segment{seg("", 0, 2, "Musique"), seg("A", 2, 4, "  "), seg("A", 4, 6, "Oui.")},
			want:     []Turn{{Label: "A", Start: 4 * time.Second, End: 6 * time.Second, Text: "Oui."}},
		},
		{
			name:     "a label returning after another voice opens a new turn",
			segments: []domain.Segment{seg("A", 0, 2, "Un."), seg("B", 2, 4, "Deux."), seg("A", 4, 6, "Trois.")},
			want: []Turn{
				{Label: "A", Start: 0, End: 2 * time.Second, Text: "Un."},
				{Label: "B", Start: 2 * time.Second, End: 4 * time.Second, Text: "Deux."},
				{Label: "A", Start: 4 * time.Second, End: 6 * time.Second, Text: "Trois."},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, Turns(tc.segments)); diff != "" {
				t.Errorf("Turns mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGround(t *testing.T) {
	t.Parallel()
	turns := turnsOf(
		"F: Bonsoir, je reçois ce soir Marine Durand et Paul Lefèvre.",
		"A: Merci de m'accueillir, Thomas.",
	)
	const title = "Le grand débat avec Karim Benali"
	tests := []struct {
		name         string
		participants []Participant
		want         []string
	}{
		{
			name:         "a name quoted from a turn is kept",
			participants: []Participant{{Name: "Marine Durand", Quote: "je reçois ce soir Marine Durand"}},
			want:         []string{"Marine Durand"},
		},
		{
			name:         "a name quoted from the title is kept",
			participants: []Participant{{Name: "Karim Benali", Quote: "avec Karim Benali"}},
			want:         []string{"Karim Benali"},
		},
		{
			name:         "a quote absent from the transcript is discarded",
			participants: []Participant{{Name: "Jean Dupont", Quote: "je reçois Jean Dupont"}},
			want:         []string{},
		},
		{
			name:         "a paraphrased quote is discarded",
			participants: []Participant{{Name: "Marine Durand", Quote: "Je reçois ce soir Marine Durand"}},
			want:         []string{},
		},
		{
			name:         "a real quote that does not contain the name is discarded",
			participants: []Participant{{Name: "Sophie Garnier", Quote: "Bonsoir, je reçois ce soir"}},
			want:         []string{},
		},
		{
			name:         "an empty name or quote is discarded",
			participants: []Participant{{Name: "", Quote: "Bonsoir"}, {Name: "Thomas", Quote: " "}},
			want:         []string{},
		},
		{
			name:         "a first name heard alone is kept as heard",
			participants: []Participant{{Name: "Thomas", Quote: "Merci de m'accueillir, Thomas."}},
			want:         []string{"Thomas"},
		},
		{
			name: "duplicates and names contained in a longer name collapse",
			participants: []Participant{
				{Name: "Durand", Quote: "Marine Durand"},
				{Name: "Marine Durand", Quote: "Marine Durand"},
				{Name: "marine durand", Quote: "Marine Durand"},
				{Name: "Paul Lefèvre", Quote: "Paul Lefèvre"},
			},
			want: []string{"Marine Durand", "Paul Lefèvre"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, Ground(tc.participants, title, turns)); diff != "" {
				t.Errorf("Ground mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScoreSignals(t *testing.T) {
	t.Parallel()
	names := []string{"Marine Durand", "Paul Lefèvre"}
	const durand, lefevre = 0, 1
	tests := []struct {
		name  string
		turns []Turn
		want  map[pairKey]float64
	}{
		{
			name:  "handoff credits the next voice",
			turns: turnsOf("F: Nous passons au sujet suivant. Madame Durand, vous avez deux minutes.", "A: Merci."),
			want:  map[pairKey]float64{{label: "A", name: durand}: weightHandoff},
		},
		{
			name:  "handoff matches a full name and folds accents",
			turns: turnsOf("F: La parole est à Paul Lefevre.", "B: Bonsoir."),
			want:  map[pairKey]float64{{label: "B", name: lefevre}: weightHandoff},
		},
		{
			name:  "a name ending the last turn hands off to nobody",
			turns: turnsOf("A: Bonsoir.", "F: La parole est à Madame Durand."),
			want:  map[pairKey]float64{{label: "A", name: durand}: weightVocative},
		},
		{
			name: "a name opening a turn credits the voice being answered",
			turns: turnsOf(
				"A: Voilà ma position.",
				"B: Madame Durand, ce que vous dites est inexact et je vais le démontrer devant tout le monde ce soir.",
				"F: Poursuivez.",
			),
			want: map[pairKey]float64{{label: "A", name: durand}: weightVocative},
		},
		{
			name: "vocative adjacency credits both neighboring voices",
			turns: turnsOf(
				"A: Voilà ma position.",
				"B: Ce que vous dites est inexact, je le répète, Madame Durand, et je vais le démontrer devant tout le monde ce soir.",
				"F: Poursuivez.",
			),
			want: map[pairKey]float64{{label: "A", name: durand}: weightVocative, {label: "F", name: durand}: weightVocative},
		},
		{
			name: "vocative adjacency credits a voice once when it surrounds the mention",
			turns: turnsOf(
				"A: Voilà ma position.",
				"B: Ce que vous dites est inexact, je le répète, Madame Durand, et je vais le démontrer devant tout le monde ce soir.",
				"A: Non.",
			),
			want: map[pairKey]float64{{label: "A", name: durand}: weightVocative},
		},
		{
			name:  "closing credits the previous voice, not the next",
			turns: turnsOf("A: J'ai terminé.", "F: Merci beaucoup Madame Durand.", "B: Bonsoir."),
			want:  map[pairKey]float64{{label: "A", name: durand}: weightVocative},
		},
		{
			name:  "self-reference credits the speaker",
			turns: turnsOf("A: Bonsoir, je m'appelle Marine Durand et je suis candidate.", "F: Merci."),
			want:  map[pairKey]float64{{label: "A", name: durand}: weightSelfReference},
		},
		{
			name:  "uttering a name with no neighboring voice credits nobody",
			turns: turnsOf("A: Je crois que Paul Lefèvre se trompe lourdement sur ce sujet depuis plusieurs années déjà, voilà."),
			want:  map[pairKey]float64{},
		},
		{
			name:  "a lowercase single word is not a name",
			turns: turnsOf("F: Il a parlé durand toute la soirée.", "A: Oui."),
			want:  map[pairKey]float64{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := score(tc.turns, newNameIndex(names)).scores
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(pairKey{})); diff != "" {
				t.Errorf("scores mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScoreNeverCreditsTheSpeakerForUtteringAName(t *testing.T) {
	t.Parallel()
	turns := turnsOf(
		"F: Bonsoir.",
		"A: Paul Lefèvre, Paul Lefèvre, toujours Paul Lefèvre, on ne parle que de lui dans cette campagne électorale interminable.",
		"F: Poursuivons.",
	)
	for key := range score(turns, newNameIndex([]string{"Paul Lefèvre"})).scores {
		if key.label == "A" {
			t.Errorf("the speaker was credited for uttering a name: %+v", key)
		}
	}
}

func TestNameIndexSharedWordIsAmbiguous(t *testing.T) {
	t.Parallel()
	ix := newNameIndex([]string{"Marine Durand", "Louis Durand"})
	turns := turnsOf("F: Monsieur Durand, vous avez la parole.", "A: Merci.")
	if got := score(turns, ix).scores; len(got) != 0 {
		t.Errorf("a surname shared by two participants must not resolve, got %v", got)
	}
	turns = turnsOf("F: Louis Durand, vous avez la parole.", "A: Merci.")
	want := map[pairKey]float64{{label: "A", name: 1}: weightHandoff}
	if diff := cmp.Diff(want, score(turns, ix).scores, cmp.AllowUnexported(pairKey{})); diff != "" {
		t.Errorf("full name must still resolve (-want +got):\n%s", diff)
	}
}

func TestResolveFusion(t *testing.T) {
	t.Parallel()
	const handoffDurand = "F: Madame Durand, vous avez deux minutes."
	const handoffLefevre = "F: Monsieur Lefèvre, vous avez deux minutes."
	names := []string{"Marine Durand", "Paul Lefèvre"}
	tests := []struct {
		name  string
		turns []Turn
		want  map[string]string
	}{
		{
			name:  "repeated agreeing handoffs name the label",
			turns: turnsOf(handoffDurand, "A: Oui.", handoffDurand, "A: Non.", handoffLefevre, "B: Oui.", handoffLefevre, "B: Non."),
			want:  map[string]string{"A": "Marine Durand", "B": "Paul Lefèvre", "F": ""},
		},
		{
			name:  "a single handoff is insufficient evidence",
			turns: turnsOf(handoffDurand, "A: Oui."),
			want:  map[string]string{"A": "", "F": ""},
		},
		{
			name:  "a label pointed at by two names is left unnamed",
			turns: turnsOf(handoffDurand, "A: Oui.", handoffDurand, "A: Non.", handoffLefevre, "A: Oui.", handoffLefevre, "A: Non."),
			want:  map[string]string{"A": "", "F": ""},
		},
		{
			name:  "a name pointing at two labels names neither",
			turns: turnsOf(handoffDurand, "A: Oui.", handoffDurand, "A: Non.", handoffDurand, "B: Oui.", handoffDurand, "B: Non."),
			want:  map[string]string{"A": "", "B": "", "F": ""},
		},
		{
			name:  "no evidence leaves every label unnamed",
			turns: turnsOf("A: Bonsoir.", "B: Bonsoir."),
			want:  map[string]string{"A": "", "B": ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := make(map[string]string)
			for _, p := range Resolve(tc.turns, names) {
				got[p.Label] = p.Name
				if p.Name == "" && (p.Confidence != 0 || p.Method != domain.SpeakerMethodNone || len(p.Evidence) != 0) {
					t.Errorf("unnamed label %s carries a decision: %+v", p.Label, p)
				}
				if p.Name != "" && p.Confidence < ConfidenceFloor {
					t.Errorf("label %s named below the floor: %v", p.Label, p.Confidence)
				}
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("names mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolveEvidenceIsVerbatimAndBounded(t *testing.T) {
	t.Parallel()
	f := newDebateFixture()
	for _, p := range Resolve(f.turns, f.names) {
		if len(p.Evidence) > maxEvidence {
			t.Errorf("label %s: %d evidence items, want at most %d", p.Label, len(p.Evidence), maxEvidence)
		}
		for _, ev := range p.Evidence {
			found := false
			for _, turn := range f.turns {
				if turn.Start == ev.At && strings.Contains(turn.Text, ev.Quote) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("label %s: evidence %q at %s is not a verbatim span of the turn at that time", p.Label, ev.Quote, ev.At)
			}
		}
	}
}

func TestResolveReferenceDebate(t *testing.T) {
	t.Parallel()
	f := newDebateFixture()
	proposals := Resolve(f.turns, f.names)

	if len(proposals) != 8 {
		t.Fatalf("got %d proposals, want 8", len(proposals))
	}
	wantRoles := map[string]domain.SpeakerRole{
		"A": domain.SpeakerRolePrincipal, "B": domain.SpeakerRolePrincipal, "C": domain.SpeakerRolePrincipal,
		"D": domain.SpeakerRolePrincipal, "E": domain.SpeakerRolePrincipal,
		"F": domain.SpeakerRoleModerator, "G": domain.SpeakerRoleModerator,
		"H": domain.SpeakerRoleGuest,
	}
	for _, p := range proposals {
		if p.Role != wantRoles[p.Label] {
			t.Errorf("label %s: role %q, want %q", p.Label, p.Role, wantRoles[p.Label])
		}
		if p.Name != "" && p.Name != f.want[p.Label] {
			t.Errorf("label %s: wrong name %q, want %q", p.Label, p.Name, f.want[p.Label])
		}
		if p.Role != domain.SpeakerRoleGuest && p.Name != f.want[p.Label] {
			t.Errorf("label %s (%s): named %q (confidence %.2f), want %q", p.Label, p.Role, p.Name, p.Confidence, f.want[p.Label])
		}
	}
}

func TestApplyTieBreak(t *testing.T) {
	t.Parallel()
	turns := turnsOf(
		"F: Madame Durand, vous avez deux minutes.",
		"A: J'ai été maire de ma commune pendant douze ans.",
		"F: Monsieur Lefèvre, vous avez deux minutes.",
		"A: Je poursuis.",
	)
	const quote = "J'ai été maire de ma commune pendant douze ans."
	base := func() []Proposal {
		return []Proposal{
			{Label: "A", Method: domain.SpeakerMethodNone, Contenders: []string{"Marine Durand", "Paul Lefèvre"}},
			{Label: "B", Name: "Sophie Garnier", Method: domain.SpeakerMethodSignals, Confidence: 0.9},
		}
	}
	tests := []struct {
		name               string
		label, pick, quote string
		mutate             func([]Proposal)
		want               bool
	}{
		{name: "a contender with a verbatim quote is accepted", label: "A", pick: "Marine Durand", quote: quote, want: true},
		{name: "a name outside the contenders is rejected", label: "A", pick: "Karim Benali", quote: quote},
		{name: "a quote absent from the transcript is rejected", label: "A", pick: "Marine Durand", quote: "J'ai été maire pendant douze ans."},
		{name: "a trivially short quote is rejected", label: "A", pick: "Marine Durand", quote: "Je"},
		{name: "a quote from another voice is rejected", label: "A", pick: "Marine Durand", quote: "Madame Durand, vous avez deux minutes."},
		{name: "an already named label is rejected", label: "B", pick: "Marine Durand", quote: quote},
		{name: "an unknown label is rejected", label: "Z", pick: "Marine Durand", quote: quote},
		{
			name: "a name another label holds is rejected", label: "A", pick: "Marine Durand", quote: quote,
			mutate: func(ps []Proposal) { ps[1].Name = "Marine Durand" },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			proposals := base()
			if tc.mutate != nil {
				tc.mutate(proposals)
			}
			before := proposalFor(t, proposals, "A")
			got := ApplyTieBreak(proposals, turns, tc.label, tc.pick, tc.quote)
			if got != tc.want {
				t.Fatalf("ApplyTieBreak = %v, want %v", got, tc.want)
			}
			after := proposalFor(t, proposals, "A")
			if !tc.want {
				if diff := cmp.Diff(before, after); diff != "" {
					t.Errorf("a rejected tie-break changed the proposal:\n%s", diff)
				}
				return
			}
			want := Proposal{
				Label: "A", Name: "Marine Durand", Confidence: ConfidenceFloor, Method: domain.SpeakerMethodTieBreak,
				Evidence: []domain.SpeakerEvidence{{Kind: domain.SpeakerEvidenceTieBreak, Quote: quote, At: 10 * time.Second}},
			}
			if diff := cmp.Diff(want, after); diff != "" {
				t.Errorf("accepted tie-break mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolveOffersContendersForAnOpenTie(t *testing.T) {
	t.Parallel()
	turns := turnsOf(
		"F: Madame Durand, vous avez deux minutes.", "A: Oui.",
		"F: Madame Durand, vous avez deux minutes.", "A: Non.",
		"F: Monsieur Lefèvre, vous avez deux minutes.", "A: Oui.",
		"F: Monsieur Lefèvre, vous avez deux minutes.", "A: Non.",
	)
	got := proposalFor(t, Resolve(turns, []string{"Marine Durand", "Paul Lefèvre"}), "A")
	if diff := cmp.Diff([]string{"Marine Durand", "Paul Lefèvre"}, got.Contenders); diff != "" {
		t.Errorf("contenders mismatch (-want +got):\n%s", diff)
	}
}

func TestInferRoles(t *testing.T) {
	t.Parallel()
	turn := func(label string, start, length int) Turn {
		return Turn{Label: label, Start: time.Duration(start) * time.Second, End: time.Duration(start+length) * time.Second, Text: "x"}
	}
	tests := []struct {
		name  string
		turns []Turn
		want  map[string]domain.SpeakerRole
	}{
		{
			name:  "a single speaker is the principal",
			turns: []Turn{turn("A", 0, 60)},
			want:  map[string]domain.SpeakerRole{"A": domain.SpeakerRolePrincipal},
		},
		{
			name:  "short turns and less time read as moderation",
			turns: []Turn{turn("F", 0, 10), turn("A", 10, 120), turn("F", 130, 10), turn("B", 140, 110), turn("F", 250, 10)},
			want:  map[string]domain.SpeakerRole{"A": domain.SpeakerRolePrincipal, "B": domain.SpeakerRolePrincipal, "F": domain.SpeakerRoleModerator},
		},
		{
			name:  "a marginal voice is a guest",
			turns: []Turn{turn("A", 0, 200), turn("H", 200, 15), turn("A", 215, 200)},
			want:  map[string]domain.SpeakerRole{"A": domain.SpeakerRolePrincipal, "H": domain.SpeakerRoleGuest},
		},
		{
			name:  "a brief interviewer taking every other turn is a moderator, not a guest",
			turns: []Turn{turn("F", 0, 5), turn("A", 5, 100), turn("F", 105, 5), turn("A", 110, 100)},
			want:  map[string]domain.SpeakerRole{"A": domain.SpeakerRolePrincipal, "F": domain.SpeakerRoleModerator},
		},
		{
			name:  "a quieter voice with long turns stays a principal",
			turns: []Turn{turn("A", 0, 100), turn("B", 100, 60), turn("A", 160, 100)},
			want:  map[string]domain.SpeakerRole{"A": domain.SpeakerRolePrincipal, "B": domain.SpeakerRolePrincipal},
		},
		{
			name:  "no timing yields unknown roles",
			turns: []Turn{{Label: "A", Text: "x"}, {Label: "B", Text: "y"}},
			want:  map[string]domain.SpeakerRole{"A": domain.SpeakerRoleUnknown, "B": domain.SpeakerRoleUnknown},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, inferRoles(tc.turns)); diff != "" {
				t.Errorf("roles mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
