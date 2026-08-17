package service

import (
	"context"
	"errors"
	"testing"
)

// compile-time proof the banded cascade drops into the gate's stage one unchanged.
var _ ClaimClassifier = (*BandedCascadeClassifier)(nil)

// fakeLocalScorer is a local stage whose score and error the tests set, and
// which counts calls so a test can prove the scorer was or was not consulted.
type fakeLocalScorer struct {
	score float64
	err   error
	calls int
}

func (f *fakeLocalScorer) Score(context.Context, string) (float64, error) {
	f.calls++
	return f.score, f.err
}

func TestBandedCascadeRouting(t *testing.T) {
	t.Parallel()
	const low, high = 0.3, 0.7
	scorerErr := errors.New("inference timed out")
	modelErr := errors.New("provider unavailable")

	tests := []struct {
		name       string
		heuristic  bool
		scorer     fakeLocalScorer
		model      *fakeCheckWorthiness
		want       bool
		scoreCalls int
		modelCalls int
	}{
		{
			name:      "heuristic reject skips scorer and model",
			heuristic: false,
			scorer:    fakeLocalScorer{score: 0.99},
			model:     &fakeCheckWorthiness{worthy: true},
			want:      false,
		},
		{
			name:       "score below band rejects without model call",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: 0.1},
			model:      &fakeCheckWorthiness{worthy: true},
			want:       false,
			scoreCalls: 1,
		},
		{
			name:       "score above band accepts without model call",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: 0.9},
			model:      &fakeCheckWorthiness{worthy: false},
			want:       true,
			scoreCalls: 1,
		},
		{
			name:       "score at high bound accepts, band is half-open",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: high},
			model:      &fakeCheckWorthiness{worthy: false},
			want:       true,
			scoreCalls: 1,
		},
		{
			name:       "score at low bound is in band and routes to model",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: low},
			model:      &fakeCheckWorthiness{worthy: false},
			want:       false,
			scoreCalls: 1,
			modelCalls: 1,
		},
		{
			name:       "in-band model accept stands",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: 0.5},
			model:      &fakeCheckWorthiness{worthy: true},
			want:       true,
			scoreCalls: 1,
			modelCalls: 1,
		},
		{
			name:       "in-band model reject stands",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: 0.5},
			model:      &fakeCheckWorthiness{worthy: false},
			want:       false,
			scoreCalls: 1,
			modelCalls: 1,
		},
		{
			name:       "in-band model error keeps heuristic positive",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: 0.5},
			model:      &fakeCheckWorthiness{err: modelErr},
			want:       true,
			scoreCalls: 1,
			modelCalls: 1,
		},
		{
			name:       "in-band without model accepts like heuristic-only wiring",
			heuristic:  true,
			scorer:     fakeLocalScorer{score: 0.5},
			want:       true,
			scoreCalls: 1,
		},
		{
			name:       "scorer error falls back to model verdict",
			heuristic:  true,
			scorer:     fakeLocalScorer{err: scorerErr},
			model:      &fakeCheckWorthiness{worthy: false},
			want:       false,
			scoreCalls: 1,
			modelCalls: 1,
		},
		{
			name:       "scorer error without model keeps heuristic positive",
			heuristic:  true,
			scorer:     fakeLocalScorer{err: scorerErr},
			want:       true,
			scoreCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var model CheckWorthinessClassifier
			if tt.model != nil {
				model = tt.model
			}
			c := NewBandedCascadeClassifier(stubClassifier{checkable: tt.heuristic}, &tt.scorer, model, low, high, discardLogger())

			got, err := c.Classify(t.Context(), "Le chômage a baissé de deux points l'an dernier.")
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got != tt.want {
				t.Errorf("Classify = %v, want %v", got, tt.want)
			}
			if tt.scorer.calls != tt.scoreCalls {
				t.Errorf("scorer consulted %d times, want %d", tt.scorer.calls, tt.scoreCalls)
			}
			if tt.model != nil && tt.model.calls != tt.modelCalls {
				t.Errorf("model consulted %d times, want %d", tt.model.calls, tt.modelCalls)
			}
		})
	}
}

func TestBandedCascadeHeuristicError(t *testing.T) {
	t.Parallel()
	// A heuristic error propagates: the caller decides what a broken stage one
	// means, exactly as in the two-stage cascade.
	heuristicErr := errors.New("classifier broken")
	scorer := &fakeLocalScorer{score: 0.9}
	c := NewBandedCascadeClassifier(errClassifier{err: heuristicErr}, scorer, nil, 0.3, 0.7, discardLogger())

	if _, err := c.Classify(t.Context(), "peu importe"); !errors.Is(err, heuristicErr) {
		t.Fatalf("Classify error = %v, want the heuristic error", err)
	}
	if scorer.calls != 0 {
		t.Errorf("scorer consulted %d times, want 0 after a heuristic error", scorer.calls)
	}
}

// errClassifier is a stage-one stub that always fails.
type errClassifier struct{ err error }

func (e errClassifier) Classify(context.Context, string) (bool, error) {
	return false, e.err
}
