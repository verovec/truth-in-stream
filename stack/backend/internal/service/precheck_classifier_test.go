package service

import "testing"

func TestHeuristicClassifierCheckable(t *testing.T) {
	t.Parallel()
	c := NewHeuristicClassifier(defaultTestMinWords)

	tests := []struct {
		name string
		text string
		want bool
	}{
		// Declarative factual assertions: the only thing that passes.
		{"plain fact", "The Eiffel Tower is in Paris.", true},
		{"numeric fact", "Water boils at one hundred degrees celsius at sea level.", true},
		{"historical fact", "The Berlin Wall fell in nineteen eighty nine.", true},
		{"fact starting with the", "The human body has two hundred and six bones.", true},

		// Questions.
		{"question mark", "Is the earth flat?", false},
		{"wh question", "What is the capital of France?", false},
		{"how question no mark", "How does a vaccine work", false},
		{"leading auxiliary", "Did the company double its revenue", false},

		// Opinions and subjective statements.
		{"i think", "I think the movie was overrated.", false},
		{"in my opinion", "In my opinion taxes are too high.", false},
		{"i believe", "I believe she is the best candidate.", false},

		// Hypotheticals and predictions.
		{"leading if", "If interest rates rise the market will fall.", false},
		{"prediction gonna", "The team is gonna win the championship.", false},
		{"hedge maybe", "Maybe the budget will pass next year.", false},
		{"prediction going to", "Prices are going to climb again.", false},

		// Greetings and filler.
		{"greeting", "Hello everyone welcome to the show.", false},
		{"thanks", "Thanks so much for joining us today.", false},
		{"interjection", "Um I am not really sure about that.", false},

		// Too-short or incomplete fragments.
		{"fragment", "The economy.", false},
		{"two words", "Big news.", false},
		{"empty", "", false},
		{"whitespace", "   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := c.Classify(tc.text); got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestHeuristicClassifierMinWordsConfigurable(t *testing.T) {
	t.Parallel()
	// A higher minimum rejects a sentence a lower minimum would accept, proving
	// the threshold is honored rather than hard-coded.
	short := "Cats are mammals."
	if !NewHeuristicClassifier(3).Classify(short) {
		t.Errorf("with minWords=3, Classify(%q) = false, want true", short)
	}
	if NewHeuristicClassifier(5).Classify(short) {
		t.Errorf("with minWords=5, Classify(%q) = true, want false", short)
	}
}

const defaultTestMinWords = 4
