package claimtype

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_claim_type tool call with the given input, so a test can fake the
// model's classification without the network.
func toolUseResponse(t *testing.T, input map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"role":  "assistant",
		"model": defaultModel,
		"content": []map[string]any{
			{"type": "tool_use", "id": "toolu_test", "name": toolName, "input": json.RawMessage(raw)},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// newTestClient points a Client at a fake Anthropic server, so request shaping
// and response parsing are exercised without hitting the real API.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{APIKey: "test-key"}, option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// typeResponder returns a handler that always answers with the given claim type
// as a record_claim_type tool call.
func typeResponder(t *testing.T, claimType string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claim_type": claimType}))
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

// TestClassifyFrenchExamples drives realistic French political claims through
// the real Classify path against a faked model verdict, covering every type in
// the enum and confirming opinion and promise come back as their own distinct
// types (so routing can hold them away from live verification).
func TestClassifyFrenchExamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		claim    string
		modelOut string
		want     Type
	}{
		{
			name:     "statistic",
			claim:    "Le taux de chômage est tombé à 7,1 % au deuxième trimestre 2024.",
			modelOut: "statistic",
			want:     Statistic,
		},
		{
			name:     "voting record",
			claim:    "La députée Sandrine Rousseau a voté contre la réforme des retraites en 2023.",
			modelOut: "voting-record",
			want:     VotingRecord,
		},
		{
			name:     "attribution",
			claim:    "Le ministre a déclaré que l'inflation serait maîtrisée avant la fin de l'année.",
			modelOut: "attribution",
			want:     Attribution,
		},
		{
			name:     "causal",
			claim:    "La hausse du SMIC a entraîné une augmentation du chômage des jeunes.",
			modelOut: "causal",
			want:     Causal,
		},
		{
			name:     "comparative",
			claim:    "La France dépense plus pour sa santé que l'Allemagne rapporté au PIB.",
			modelOut: "comparative",
			want:     Comparative,
		},
		{
			name:     "promise is separated",
			claim:    "Nous créerons un million d'emplois d'ici la fin du quinquennat.",
			modelOut: "promise",
			want:     Promise,
		},
		{
			name:     "opinion is separated",
			claim:    "La politique économique du gouvernement est un désastre absolu.",
			modelOut: "opinion",
			want:     Opinion,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, typeResponder(t, tt.modelOut))
			got := c.Classify(context.Background(), tt.claim)
			if got != tt.want {
				t.Errorf("Classify(%q) = %q, want %q", tt.claim, got, tt.want)
			}
		})
	}
}

func TestClassifyForcesStructuredToolCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claim_type": "statistic"}))
	})

	c.Classify(context.Background(), "Le déficit a doublé en 2023.")

	if captured["model"] != defaultModel {
		t.Errorf("model = %v, want %s", captured["model"], defaultModel)
	}
	if captured["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", captured["temperature"])
	}
	if captured["max_tokens"] != float64(maxTokens) {
		t.Errorf("max_tokens = %v, want %d", captured["max_tokens"], maxTokens)
	}
	choice, ok := captured["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "tool" || choice["name"] != toolName {
		t.Errorf("tool_choice = %v, want forced %s", captured["tool_choice"], toolName)
	}
}

func TestClassifySendsClaimText(t *testing.T) {
	t.Parallel()
	var captured struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claim_type": "attribution"}))
	})

	claim := "Le maire a affirmé que la ville était endettée à hauteur de dix millions."
	c.Classify(context.Background(), claim)

	if len(captured.Messages) == 0 || len(captured.Messages[0].Content) == 0 {
		t.Fatal("expected a user message with content")
	}
	if user := captured.Messages[0].Content[0].Text; !strings.Contains(user, claim) {
		t.Errorf("user message missing the claim text; got %q", user)
	}
}

func TestClassifyBlankInputReturnsDefaultWithoutCallingModel(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "   ", "\t\n "} {
		t.Run("blank "+text, func(t *testing.T) {
			t.Parallel()
			called := false
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claim_type": "statistic"}))
			})

			if got := c.Classify(context.Background(), text); got != DefaultType {
				t.Errorf("Classify(blank) = %q, want default %q", got, DefaultType)
			}
			if called {
				t.Error("Classify called the model on a blank claim; want a short-circuit")
			}
		})
	}
}

func TestClassifyTransportErrorDegradesToDefault(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	if got := c.Classify(context.Background(), "Le déficit a doublé en 2023."); got != DefaultType {
		t.Errorf("Classify on transport error = %q, want safe default %q", got, DefaultType)
	}
}

func TestClassifyMissingToolCallDegradesToDefault(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	if got := c.Classify(context.Background(), "Le déficit a doublé en 2023."); got != DefaultType {
		t.Errorf("Classify on missing tool call = %q, want safe default %q", got, DefaultType)
	}
}

func TestClassifyUnknownTypeDegradesToDefault(t *testing.T) {
	t.Parallel()
	// The forced-tool enum should keep this from happening, but a model that
	// returns a value outside the enum must not produce a bogus Type - it
	// degrades to the safe default like any other failure.
	c := newTestClient(t, typeResponder(t, "rhetorical-question"))

	if got := c.Classify(context.Background(), "Le déficit a doublé en 2023."); got != DefaultType {
		t.Errorf("Classify on unknown type = %q, want safe default %q", got, DefaultType)
	}
}

func TestVerifiableSeparatesOpinionAndPromise(t *testing.T) {
	t.Parallel()
	verifiable := map[Type]bool{
		Statistic:    true,
		VotingRecord: true,
		Attribution:  true,
		Causal:       true,
		Comparative:  true,
		Promise:      false,
		Opinion:      false,
	}
	for typ, want := range verifiable {
		if got := typ.Verifiable(); got != want {
			t.Errorf("%q.Verifiable() = %v, want %v", typ, got, want)
		}
	}
}
