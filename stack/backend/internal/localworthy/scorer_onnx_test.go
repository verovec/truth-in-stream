//go:build localworthy

package localworthy

import (
	"context"
	"os"
	"testing"
	"time"
)

// scorerFromEnv builds the real scorer from the LOCALWORTHY_TEST_* artifact
// paths, skipping when they are absent so the tagged suite still runs
// everywhere the native libraries exist but no artifact is checked out.
func scorerFromEnv(t *testing.T) *Scorer {
	t.Helper()
	model := os.Getenv("LOCALWORTHY_TEST_MODEL")
	tokenizer := os.Getenv("LOCALWORTHY_TEST_TOKENIZER")
	if model == "" || tokenizer == "" {
		t.Skip("LOCALWORTHY_TEST_MODEL and LOCALWORTHY_TEST_TOKENIZER not set")
	}
	s, err := New(Config{
		ModelPath:     model,
		TokenizerPath: tokenizer,
		LibraryPath:   os.Getenv("LOCALWORTHY_TEST_ONNX_LIBRARY"),
		Timeout:       2 * time.Second,
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

func TestScorerSeparatesClaimsFromChatter(t *testing.T) {
	s := scorerFromEnv(t)
	ctx := context.Background()

	claim, err := s.Score(ctx, "Le chômage a baissé de deux points depuis le début du quinquennat.")
	if err != nil {
		t.Fatalf("Score claim: %v", err)
	}
	chatter, err := s.Score(ctx, "Nous porterons toujours la voix des oubliés de la République.")
	if err != nil {
		t.Fatalf("Score chatter: %v", err)
	}
	if claim <= 0 || claim > 1 || chatter < 0 || chatter > 1 {
		t.Fatalf("scores outside [0, 1]: claim %v, chatter %v", claim, chatter)
	}
	if claim <= chatter {
		t.Errorf("claim scored %v, chatter %v; expected the claim to score higher", claim, chatter)
	}
}

func TestScorerTimeoutFailsOpen(t *testing.T) {
	s := scorerFromEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Score(ctx, "La dette publique dépasse les trois mille milliards d'euros."); err == nil {
		t.Error("expected an error from a canceled context")
	}
}

func TestScorerLatencyBudget(t *testing.T) {
	s := scorerFromEnv(t)
	ctx := context.Background()
	const rounds = 20
	start := time.Now()
	for i := 0; i < rounds; i++ {
		if _, err := s.Score(ctx, "Le budget de la défense a été porté à quarante-sept milliards d'euros."); err != nil {
			t.Fatalf("Score round %d: %v", i, err)
		}
	}
	perCall := time.Since(start) / rounds
	t.Logf("mean latency over %d calls: %s", rounds, perCall)
	if perCall > 300*time.Millisecond {
		t.Errorf("mean inference latency %s exceeds the 300ms live budget", perCall)
	}
}
