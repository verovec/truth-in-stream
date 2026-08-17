//go:build localinference

package nli

import (
	"context"
	"os"
	"testing"
	"time"
)

// scorerFromEnv builds the real scorer from the NLI_TEST_* artifact paths,
// skipping when they are absent so the tagged suite still runs everywhere the
// native libraries exist but no artifact is checked out.
func scorerFromEnv(t *testing.T) *Scorer {
	t.Helper()
	model := os.Getenv("NLI_TEST_MODEL")
	tokenizer := os.Getenv("NLI_TEST_TOKENIZER")
	if model == "" || tokenizer == "" {
		t.Skip("NLI_TEST_MODEL and NLI_TEST_TOKENIZER not set")
	}
	s, err := New(Config{
		ModelPath:     model,
		TokenizerPath: tokenizer,
		LibraryPath:   os.Getenv("NLI_TEST_ONNX_LIBRARY"),
		Temperature:   1.8634,
		Timeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestScorerSeparatesStances(t *testing.T) {
	s := scorerFromEnv(t)
	ctx := context.Background()
	premise := "Selon l'INSEE, le taux de chômage s'établit à 7,3 pour cent de la population active, en baisse de 0,4 point sur un an."

	stances, err := s.ScoreStances(ctx, "Le chômage a baissé en France sur un an.", []string{premise})
	if err != nil {
		t.Fatalf("ScoreStances(claim): %v", err)
	}
	support := stances[0]
	stances, err = s.ScoreStances(ctx, "Le chômage n'a pas baissé en France sur un an.", []string{premise})
	if err != nil {
		t.Fatalf("ScoreStances(negation): %v", err)
	}
	refuted := stances[0]

	if support.Entailment <= support.Contradiction {
		t.Errorf("supported claim scored entail %.3f <= contradict %.3f", support.Entailment, support.Contradiction)
	}
	if refuted.Contradiction <= refuted.Entailment {
		t.Errorf("negated claim scored contradict %.3f <= entail %.3f", refuted.Contradiction, refuted.Entailment)
	}
	t.Logf("claim: entail %.3f | negation: contradict %.3f", support.Entailment, refuted.Contradiction)
}

func TestScorerUnrelatedEvidenceStaysBelowDecisionThresholds(t *testing.T) {
	s := scorerFromEnv(t)
	stances, err := s.ScoreStances(context.Background(),
		"Le budget de la défense a doublé en dix ans.",
		[]string{"Selon l'INSEE, le taux de chômage s'établit à 7,3 pour cent de la population active."})
	if err != nil {
		t.Fatalf("ScoreStances: %v", err)
	}
	// What matters for the consensus rule is that unrelated evidence never
	// crosses a decision threshold (the shipped defaults), so the claim
	// escalates; the model may still lean slightly toward either class.
	st := stances[0]
	if st.Entailment >= 0.70 || st.Contradiction >= 0.90 {
		t.Errorf("unrelated evidence crossed a decision threshold: %+v", st)
	}
}

func TestScorerCanceledContextFails(t *testing.T) {
	s := scorerFromEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ScoreStances(ctx, "La dette dépasse trois mille milliards.", []string{"Une source."}); err == nil {
		t.Error("expected an error from a canceled context")
	}
}

func TestScorerLatencyBudget(t *testing.T) {
	s := scorerFromEnv(t)
	ctx := context.Background()
	claim := "La dette publique dépasse les trois mille milliards d'euros."
	passages := []string{
		"D'après la Banque de France, la dette publique a atteint 3 100 milliards d'euros au premier trimestre.",
		"Le déficit public a atteint 5,5 pour cent du produit intérieur brut selon l'INSEE.",
		"La charge de la dette coûtera plus de cinquante milliards d'euros cette année.",
	}
	const rounds = 10
	start := time.Now()
	for i := 0; i < rounds; i++ {
		if _, err := s.ScoreStances(ctx, claim, passages); err != nil {
			t.Fatalf("ScoreStances round %d: %v", i, err)
		}
	}
	perClaim := time.Since(start) / rounds
	t.Logf("mean latency over %d claims x %d passages: %s", rounds, len(passages), perClaim)
	if perClaim > 2*time.Second {
		t.Errorf("mean stance latency %s exceeds the 2s budget", perClaim)
	}
}
