package service

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestHeuristicClassifierFrench(t *testing.T) {
	t.Parallel()
	c := NewHeuristicClassifier(defaultTestMinWords, domain.LocaleFrench)

	tests := []struct {
		name string
		text string
		want bool
	}{
		// Declarative factual assertions still pass.
		{"plain fact", "Le chomage a atteint sept virgule cinq pour cent au deuxieme trimestre.", true},
		{"attributed fact", "Le deficit public represente 5,5 % du PIB selon l'INSEE.", true},
		{"historical fact", "La France a aboli la peine de mort en 1981.", true},
		{"quantified commitment", "Nous allons creer 200 000 emplois d'ici 2027.", true},
		{"bare selon is not an opinion marker", "Selon l'INSEE, l'inflation a ralenti a 1,2 % en juillet.", true},

		// Questions.
		{"question mark", "Le chomage a-t-il vraiment baisse ?", false},
		{"wh question no mark", "Pourquoi le gouvernement a choisi cette reforme", false},
		{"est-ce que leading", "Est-ce que la France va mieux qu'il y a cinq ans", false},
		{"inverted aux leading", "A-t-il augmente les impots depuis son election", false},
		{"combien leading", "Combien coute cette mesure aux contribuables francais", false},

		// Opinions and subjective statements.
		{"je pense", "Je pense que cette reforme est une erreur historique.", false},
		{"a mon avis", "A mon avis les taxes sont beaucoup trop hautes.", false},
		{"selon moi", "Selon moi la majorite a perdu le contact avec le pays.", false},
		{"il me semble", "Il me semble que ce debat est totalement sterile.", false},
		{"accented personnellement", "Personnellement je ne voterai jamais ce texte de loi.", false},

		// Hedges and predictions.
		{"sans doute", "Le budget sera sans doute adopte l'an prochain.", false},
		{"accented surement", "Le budget passera sûrement l'an prochain sans encombre.", false},
		{"probablement", "La croissance sera probablement revue a la baisse.", false},
		{"je parie", "Je parie que la motion de censure echouera encore.", false},
		{"leading si hypothetical", "Si les taux montent les menages seront etrangles.", false},

		// Greetings, phatic openers, and procedural filler.
		{"bonjour", "Bonjour a tous et bienvenue sur le plateau.", false},
		{"merci", "Merci beaucoup d'etre venu ce soir sur notre antenne.", false},
		{"elided d'accord leading", "D'accord on va passer a la question suivante.", false},
		{"euh filler", "Euh je ne sais pas trop quoi repondre.", false},
		{"au revoir phrase", "On se dit au revoir et a demain sur cette chaine.", false},

		// Split inversion markers.
		{"y a-t-il split tokens", "Y a-t-il encore des marges budgetaires pour cette reforme", false},
		{"est ce que unhyphenated", "Est ce que la dette a vraiment double en dix ans", false},

		// Precision guards: ambiguous French markers must NOT reject.
		{"peut etre as verb", "Il peut etre condamne a dix ans de prison pour ces faits.", true},
		{"alors que opener", "Alors que le chomage baisse, les inegalites se creusent fortement.", true},
		{"bon nombre opener", "Bon nombre de deputes ont vote contre ce texte de loi.", true},
		{"quand opener declarative", "Quand on regarde les chiffres, le chomage a bien baisse.", true},
		{"que subordinate subject", "Que le chomage ait recule est incontestable selon ces donnees.", true},
		{"qui dislocation", "Qui a gagne au final, c'est le contribuable francais.", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := c.Classify(t.Context(), tc.text)
			if err != nil {
				t.Fatalf("Classify(%q): %v", tc.text, err)
			}
			if got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestHeuristicClassifierFrenchDiacriticsAndElision(t *testing.T) {
	t.Parallel()
	c := NewHeuristicClassifier(defaultTestMinWords, domain.LocaleFrench)

	// The same marker must fire with and without diacritics, and through
	// apostrophe elision, because live transcription is inconsistent about both.
	rejected := []string{
		"Le budget passera sûrement l'an prochain sans encombre.",
		"Le budget passera surement l'an prochain sans encombre.",
		"J'espère que la France gagnera ce match decisif.",
		"J espere que la France gagnera ce match decisif.",
		"Où en est la reforme des retraites aujourd'hui",
	}
	for _, text := range rejected {
		got, err := c.Classify(t.Context(), text)
		if err != nil {
			t.Fatalf("Classify(%q): %v", text, err)
		}
		if got {
			t.Errorf("Classify(%q) = true, want false", text)
		}
	}
}

func TestHeuristicClassifierEnglishLocaleUnchanged(t *testing.T) {
	t.Parallel()
	// The default locale must not learn the French lexicons: French filler
	// passes stage one in an English session exactly as before this card.
	c := NewHeuristicClassifier(defaultTestMinWords, domain.LocaleEnglish)

	frenchFiller := "Bonjour a tous et bienvenue sur le plateau."
	got, err := c.Classify(t.Context(), frenchFiller)
	if err != nil {
		t.Fatalf("Classify(%q): %v", frenchFiller, err)
	}
	if !got {
		t.Errorf("Classify(%q) = false with the English locale, want true (French lists must be locale-gated)", frenchFiller)
	}

	english := "I think the movie was overrated."
	got, err = c.Classify(t.Context(), english)
	if err != nil {
		t.Fatalf("Classify(%q): %v", english, err)
	}
	if got {
		t.Errorf("Classify(%q) = true, want false (English behavior regressed)", english)
	}
}

// TestHeuristicClassifierFrenchRejectRate records the deterministic-reject rate
// on a fixed sample of French debate filler, before (English locale) and after
// (French locale) this card, so the gain is measured rather than asserted.
func TestHeuristicClassifierFrenchRejectRate(t *testing.T) {
	t.Parallel()
	sample := []string{
		"Bonjour a tous et bienvenue dans ce debat.",
		"Merci d'etre avec nous ce soir.",
		"Est-ce que vous pouvez repondre a la question",
		"Pourquoi refusez vous de repondre clairement",
		"Je pense que vous vous trompez completement.",
		"A mon avis ce texte ne passera jamais.",
		"Il me semble que vous exagerez un peu.",
		"Le budget sera sans doute rejete cette fois.",
		"La gauche va probablement s'y opposer aussi.",
		"Euh je voudrais juste ajouter une chose.",
		"D'accord on passe au sujet suivant.",
		"Combien de temps il nous reste exactement",
	}

	count := func(locale domain.Locale) int {
		c := NewHeuristicClassifier(defaultTestMinWords, locale)
		n := 0
		for _, text := range sample {
			ok, err := c.Classify(t.Context(), text)
			if err != nil {
				t.Fatalf("Classify(%q): %v", text, err)
			}
			if !ok {
				n++
			}
		}
		return n
	}

	before, after := count(domain.LocaleEnglish), count(domain.LocaleFrench)
	t.Logf("deterministic rejects on %d French filler utterances: before=%d after=%d", len(sample), before, after)
	if after != len(sample) {
		t.Errorf("French locale rejected %d of %d filler utterances, want all", after, len(sample))
	}
	if before >= after {
		t.Errorf("French locale rejected %d, English locale %d: expected a strict improvement", after, before)
	}
}
