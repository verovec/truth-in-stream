package speakername

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// toolUseResponse builds a minimal Messages response carrying one forced tool
// call, so a test fakes the model without the network.
func toolUseResponse(t *testing.T, tool string, input map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-haiku-4-5-20251001",
		"content": []map[string]any{
			{"type": "tool_use", "id": "toolu_test", "name": tool, "input": input},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// newTestModel points a Model at a fake provider and records the request body.
func newTestModel(t *testing.T, status int, response string, sent *string) *Model {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sent != nil {
			raw, _ := io.ReadAll(r.Body)
			*sent = string(raw)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	m, err := NewModel(Config{Provider: llm.ProviderAnthropic, APIKey: "test-key"}, llm.WithBaseURL(server.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	return m
}

func TestNewModelRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := NewModel(Config{Provider: llm.ProviderAnthropic}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestModelParticipants(t *testing.T) {
	t.Parallel()
	var sent string
	m := newTestModel(t, http.StatusOK, toolUseResponse(t, participantsTool, map[string]any{
		"participants": []map[string]any{
			{"name": "Marine Durand", "quote": "je reçois Marine Durand"},
			{"name": "Thomas", "quote": "Thomas, à vous"},
		},
	}), &sent)

	got, err := m.Participants(t.Context(), "Le grand débat", turnsOf("F: Bonsoir, je reçois Marine Durand.", "A: Bonsoir."))
	if err != nil {
		t.Fatalf("Participants: %v", err)
	}
	want := []Participant{{Name: "Marine Durand", Quote: "je reçois Marine Durand"}, {Name: "Thomas", Quote: "Thomas, à vous"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("participants mismatch (-want +got):\n%s", diff)
	}
	for _, fragment := range []string{"Le grand débat", "[F] Bonsoir, je reçois Marine Durand.", participantsTool} {
		if !strings.Contains(sent, fragment) {
			t.Errorf("request does not carry %q", fragment)
		}
	}
}

func TestModelBreakTie(t *testing.T) {
	t.Parallel()
	var sent string
	m := newTestModel(t, http.StatusOK, toolUseResponse(t, tieBreakTool, map[string]any{
		"name": " Marine Durand ", "quote": "J'ai été maire douze ans.",
	}), &sent)

	turns := turnsOf("F: Une question.", "A: J'ai été maire douze ans.", "B: Pas moi.")
	name, quote, err := m.BreakTie(t.Context(), "A", []string{"Marine Durand", "Paul Lefèvre"}, turns)
	if err != nil {
		t.Fatalf("BreakTie: %v", err)
	}
	if name != "Marine Durand" || quote != "J'ai été maire douze ans." {
		t.Errorf("BreakTie = (%q, %q)", name, quote)
	}
	if !strings.Contains(sent, "Marine Durand; Paul Lefèvre") || !strings.Contains(sent, "J'ai été maire douze ans.") {
		t.Errorf("request lacks the contenders or the label's turns: %s", sent)
	}
	if strings.Contains(sent, "Pas moi.") {
		t.Errorf("request carries another label's turn")
	}
}

func TestModelErrorsAreWrapped(t *testing.T) {
	t.Parallel()
	m := newTestModel(t, http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error","message":"boom"}}`, nil)
	if _, err := m.Participants(t.Context(), "t", turnsOf("A: Bonsoir.")); err == nil || !strings.Contains(err.Error(), "listing participants") {
		t.Errorf("Participants error = %v, want a wrapped failure", err)
	}
	if _, _, err := m.BreakTie(t.Context(), "A", []string{"x"}, turnsOf("A: Bonsoir.")); err == nil || !strings.Contains(err.Error(), "breaking tie for A") {
		t.Errorf("BreakTie error = %v, want a wrapped failure", err)
	}
}

func TestNameBearingExcerpts(t *testing.T) {
	t.Parallel()
	filler := strings.Repeat("nous devons agir pour le pays et pour nos enfants ", 6)
	turns := make([]Turn, 0, introTurns+3)
	for range introTurns {
		turns = append(turns, Turn{Label: "F", Text: "Bonsoir."})
	}
	turns = append(turns,
		Turn{Label: "A", Text: "Rien à signaler ici. " + filler},
		Turn{Label: "B", Text: filler + "comme le dit Madame Durand depuis des mois " + filler},
		Turn{Label: "F", Text: "Thomas, à vous."},
	)

	got := nameBearingExcerpts(turns, promptBudget)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != introTurns+2 {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), introTurns+2, got)
	}
	window := strings.TrimPrefix(lines[introTurns], "[B] ")
	if !strings.Contains(window, "Madame Durand") || !strings.Contains(turns[introTurns+1].Text, window) {
		t.Errorf("window %q is not a verbatim span around the name", window)
	}
	if len(window) >= len(turns[introTurns+1].Text) {
		t.Errorf("window was not narrowed around the name")
	}
	if lines[introTurns+1] != "[F] Thomas, à vous" {
		t.Errorf("sentence-opening vocative line = %q", lines[introTurns+1])
	}

	if short := nameBearingExcerpts(turns, 40); len(short) > 40 {
		t.Errorf("excerpt of %d bytes exceeds the budget", len(short))
	}
}

func TestClip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in string
		limit    int
		want     string
	}{
		{name: "short text is unchanged", in: "Bonsoir à tous", limit: 40, want: "Bonsoir à tous"},
		{name: "long text is cut on a word boundary", in: "Bonsoir à tous les Français", limit: 16, want: "Bonsoir à tous"},
		{name: "a single oversized word yields nothing", in: "anticonstitutionnellement", limit: 5, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := clip(tc.in, tc.limit); got != tc.want {
				t.Errorf("clip = %q, want %q", got, tc.want)
			}
		})
	}
}
