package service

import (
	"context"
	"errors"
	"testing"
)

// compile-time proof the cascade drops into the gate's stage one unchanged.
var _ ClaimClassifier = (*CascadeClassifier)(nil)

// fakeCheckWorthiness is a model stage whose verdict and error the tests set,
// and which counts calls so a test can prove the model was or was not consulted.
type fakeCheckWorthiness struct {
	worthy bool
	err    error
	calls  int
}

func (f *fakeCheckWorthiness) CheckWorthy(context.Context, string) (bool, error) {
	f.calls++
	return f.worthy, f.err
}

func TestCascadeHeuristicRejectSkipsModel(t *testing.T) {
	t.Parallel()
	// The heuristic rejects a non-claim, so the model is never consulted - the
	// cheap stage shields the expensive one from obvious rejects.
	model := &fakeCheckWorthiness{worthy: true}
	c := NewCascadeClassifier(stubClassifier{checkable: false}, model, discardLogger())

	got, err := c.Classify(t.Context(), "is this a question")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got {
		t.Error("expected the heuristic reject to stand")
	}
	if model.calls != 0 {
		t.Errorf("model consulted %d times, want 0 after a heuristic reject", model.calls)
	}
}

func TestCascadeModelDecidesCheckWorthy(t *testing.T) {
	t.Parallel()
	// The heuristic accepts a declarative; the model judges it check-worthy.
	model := &fakeCheckWorthiness{worthy: true}
	c := NewCascadeClassifier(stubClassifier{checkable: true}, model, discardLogger())

	got, err := c.Classify(t.Context(), "Unemployment fell to four percent last quarter.")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !got {
		t.Error("expected a check-worthy claim to pass")
	}
	if model.calls != 1 {
		t.Errorf("model consulted %d times, want 1", model.calls)
	}
}

func TestCascadeModelRejectsCasualDeclarative(t *testing.T) {
	t.Parallel()
	// The heuristic accepts a grammatically-valid declarative the word-list
	// cannot reject; the model recognizes it as casual small talk and skips it.
	model := &fakeCheckWorthiness{worthy: false}
	c := NewCascadeClassifier(stubClassifier{checkable: true}, model, discardLogger())

	got, err := c.Classify(t.Context(), "I had coffee this morning.")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got {
		t.Error("expected the model to skip a casual personal declarative")
	}
	if model.calls != 1 {
		t.Errorf("model consulted %d times, want 1", model.calls)
	}
}

func TestCascadeModelErrorDegradesToHeuristic(t *testing.T) {
	t.Parallel()
	// A model outage must not disable fact-checking: the heuristic already
	// accepted the segment, so the cascade keeps that positive decision and
	// returns no error rather than skipping all speech.
	model := &fakeCheckWorthiness{err: errors.New("provider down")}
	c := NewCascadeClassifier(stubClassifier{checkable: true}, model, discardLogger())

	got, err := c.Classify(t.Context(), "The bridge opened in nineteen thirty seven.")
	if err != nil {
		t.Fatalf("Classify returned an error on model failure, want graceful degrade: %v", err)
	}
	if !got {
		t.Error("expected the heuristic decision to stand on model failure")
	}
}

func TestCascadeHeuristicErrorPropagates(t *testing.T) {
	t.Parallel()
	// A failure from the first stage is a real error, not a skip, and the model
	// is never reached.
	sentinel := errors.New("heuristic down")
	model := &fakeCheckWorthiness{worthy: true}
	c := NewCascadeClassifier(stubClassifier{checkable: true, err: sentinel}, model, discardLogger())

	_, err := c.Classify(t.Context(), "a claim")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Classify err = %v, want wrapping %v", err, sentinel)
	}
	if model.calls != 0 {
		t.Errorf("model consulted %d times, want 0 after a heuristic error", model.calls)
	}
}
