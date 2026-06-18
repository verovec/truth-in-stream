package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/checkworthy"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// modelGateToolResponse fakes the Anthropic Messages response the real
// checkworthy adapter parses: one forced record_check_worthiness tool call
// carrying the given verdict. The tool name is the adapter's wire contract,
// asserted here by an end-to-end round-trip rather than mocked away.
func modelGateToolResponse(t *testing.T, checkWorthy bool) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"check_worthy": checkWorthy, "reason": ""})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": []map[string]any{
			{"type": "tool_use", "id": "toolu_test", "name": "record_check_worthiness", "input": json.RawMessage(input)},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// modelGate wires the production composition end to end: the real heuristic and
// the real Anthropic-backed check-worthiness adapter (pointed at a fake server)
// inside a real CascadeClassifier, behind a real Gate whose coverage always
// passes so the test isolates the classify path through to the decision.
func modelGate(t *testing.T, verdict bool) *Gate {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, modelGateToolResponse(t, verdict))
	}))
	t.Cleanup(server.Close)

	model, err := checkworthy.New(
		checkworthy.Config{APIKey: "test-key"},
		llm.WithBaseURL(server.URL), llm.WithMaxRetries(0),
	)
	if err != nil {
		t.Fatalf("checkworthy.New: %v", err)
	}
	cascade := NewCascadeClassifier(NewHeuristicClassifier(defaultTestMinWords), model, discardLogger())
	return NewGate(cascade, stubCoverage{covered: true})
}

// TestGateModelSkipsCasualDeclarative is the end-to-end check of the live gate's
// new model stage: a grammatically-valid personal declarative passes the
// heuristic but the model judges it not check-worthy, so the gate skips it as a
// non-claim - the exact gap the card closes.
func TestGateModelSkipsCasualDeclarative(t *testing.T) {
	t.Parallel()
	g := modelGate(t, false)

	got, err := g.Evaluate(t.Context(), "I had coffee this morning before the meeting.")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if want := domain.Skipped(domain.SkipReasonNotAClaim); got != want {
		t.Errorf("Evaluate = %+v, want %+v", got, want)
	}
}

// TestGateModelPassesPublicClaim proves the same wiring lets a check-worthy
// public claim through to a checkable decision.
func TestGateModelPassesPublicClaim(t *testing.T) {
	t.Parallel()
	g := modelGate(t, true)

	got, err := g.Evaluate(t.Context(), "Unemployment fell to four percent last quarter.")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got != domain.Checkable() {
		t.Errorf("Evaluate = %+v, want checkable", got)
	}
}

// TestGateHeuristicOnlyUnchangedWhenModelUnconfigured is the regression guard
// for the unconfigured path: with no model wired the gate is exactly today's
// heuristic-plus-coverage gate - a question is skipped, a plain fact passes -
// and no model server is contacted.
func TestGateHeuristicOnlyUnchangedWhenModelUnconfigured(t *testing.T) {
	t.Parallel()
	g := NewGate(NewHeuristicClassifier(defaultTestMinWords), stubCoverage{covered: true})

	skip, err := g.Evaluate(t.Context(), "What is the capital of France?")
	if err != nil {
		t.Fatalf("Evaluate question: %v", err)
	}
	if want := domain.Skipped(domain.SkipReasonNotAClaim); skip != want {
		t.Errorf("question Evaluate = %+v, want %+v", skip, want)
	}

	pass, err := g.Evaluate(t.Context(), "The Eiffel Tower is in Paris.")
	if err != nil {
		t.Fatalf("Evaluate fact: %v", err)
	}
	if pass != domain.Checkable() {
		t.Errorf("fact Evaluate = %+v, want checkable", pass)
	}
}
