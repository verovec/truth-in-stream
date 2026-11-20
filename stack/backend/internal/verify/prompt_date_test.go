package verify

import (
	"strings"
	"testing"
)

func TestBuildPromptLabelsDatedPassages(t *testing.T) {
	t.Parallel()
	prompt := buildPrompt("Le chomage a baisse en 2024.", []Passage{
		{ID: "ev:1:0", Text: "Le taux de chomage recule a 7,3% fin 2024.", Date: "2024-12-31"},
		{ID: "ev:2:0", Text: "Le chomage de longue duree reste stable."},
	})

	if !strings.Contains(prompt, "[evidence_id: ev:1:0 | date: 2024-12-31]") {
		t.Errorf("dated passage header missing its date label:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[evidence_id: ev:2:0]") {
		t.Errorf("undated passage header changed:\n%s", prompt)
	}
	if strings.Contains(prompt, "ev:2:0 | date") {
		t.Errorf("undated passage grew a date label:\n%s", prompt)
	}
}
