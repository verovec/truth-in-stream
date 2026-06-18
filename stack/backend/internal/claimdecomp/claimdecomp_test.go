package claimdecomp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/llm"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_claims tool call with the given input, so a test can fake the model's
// decomposition without the network.
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
func newTestClient(t *testing.T, cfg Config, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	if cfg.APIKey == "" {
		cfg.APIKey = "test-key"
	}
	c, err := New(cfg, llm.WithBaseURL(server.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// claimsResponder returns a handler that always answers with the given claims as
// a record_claims tool call.
func claimsResponder(t *testing.T, claims []string) http.HandlerFunc {
	t.Helper()
	items := make([]any, len(claims))
	for i, c := range claims {
		items[i] = c
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claims": items}))
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestNewDefaultsMaxClaims(t *testing.T) {
	t.Parallel()
	for _, in := range []int{0, -1} {
		c, err := New(Config{APIKey: "test-key", MaxClaimsPerUnit: in})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.maxClaims != defaultMaxClaims {
			t.Errorf("MaxClaimsPerUnit %d gave maxClaims %d, want %d", in, c.maxClaims, defaultMaxClaims)
		}
	}
}

func TestDecompose(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		maxic    int
		modelOut []string
		want     []string
	}{
		{
			name:     "multi-claim splitting",
			modelOut: []string{"Unemployment fell to four percent last quarter.", "The deficit doubled in 2023."},
			want:     []string{"Unemployment fell to four percent last quarter.", "The deficit doubled in 2023."},
		},
		{
			name:     "coreference resolution",
			modelOut: []string{"Senator Smith voted against the bill in 2022."},
			want:     []string{"Senator Smith voted against the bill in 2022."},
		},
		{
			name:     "opinion dropping leaves only the factual claim",
			modelOut: []string{"The factory employs two thousand workers."},
			want:     []string{"The factory employs two thousand workers."},
		},
		{
			name:     "empty list is valid",
			modelOut: []string{},
			want:     []string{},
		},
		{
			name:     "blanks and surrounding whitespace are dropped",
			modelOut: []string{"  GDP grew three percent.  ", "", "   "},
			want:     []string{"GDP grew three percent."},
		},
		{
			name:     "claims past the cap are dropped as a backstop",
			maxic:    2,
			modelOut: []string{"Claim one.", "Claim two.", "Claim three.", "Claim four."},
			want:     []string{"Claim one.", "Claim two."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, Config{MaxClaimsPerUnit: tt.maxic}, claimsResponder(t, tt.modelOut))
			got := c.Decompose(context.Background(), Input{Text: "the raw unit"})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Decompose() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDecomposeForcesStructuredToolCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claims": []any{"A claim."}}))
	})

	c.Decompose(context.Background(), Input{Text: "anything"})

	if captured["model"] != defaultModel {
		t.Errorf("model = %v, want %s", captured["model"], defaultModel)
	}
	if captured["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", captured["temperature"])
	}
	choice, ok := captured["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "tool" || choice["name"] != toolName {
		t.Errorf("tool_choice = %v, want forced %s", captured["tool_choice"], toolName)
	}
}

func TestDecomposeSendsSpeakerAndContext(t *testing.T) {
	t.Parallel()
	var captured struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claims": []any{"A claim."}}))
	})

	c.Decompose(context.Background(), Input{
		Text:    "He said it would pass.",
		Speaker: "Alice",
		Context: "Alice is discussing the infrastructure bill.",
	})

	if len(captured.Messages) == 0 || len(captured.Messages[0].Content) == 0 {
		t.Fatal("expected a user message with content")
	}
	user := captured.Messages[0].Content[0].Text
	for _, want := range []string{"Alice", "infrastructure bill", "He said it would pass."} {
		if !strings.Contains(user, want) {
			t.Errorf("user message missing %q; got %q", want, user)
		}
	}
}

func TestDecomposeErrorFallsBackToSingleClaim(t *testing.T) {
	t.Parallel()
	unit := "The bridge opened in March."
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	got := c.Decompose(context.Background(), Input{Text: unit})
	if !reflect.DeepEqual(got, []string{unit}) {
		t.Errorf("Decompose() on error = %#v, want single-claim fallback %#v", got, []string{unit})
	}
}

func TestDecomposeBlankInputReturnsEmptyListWithoutCallingModel(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "   ", "\t\n "} {
		t.Run("blank "+text, func(t *testing.T) {
			t.Parallel()
			called := false
			// The model would hallucinate claims for a blank unit; the guard must
			// return an empty list before the request is ever made.
			c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claims": []any{"A hallucinated claim."}}))
			})

			got := c.Decompose(context.Background(), Input{Text: text})
			if len(got) != 0 {
				t.Errorf("Decompose() on blank input = %#v, want empty list (no blank claim)", got)
			}
			if called {
				t.Error("Decompose() called the model on a blank input; want a short-circuit")
			}
		})
	}
}

func TestDecomposeMissingToolCallFallsBackToSingleClaim(t *testing.T) {
	t.Parallel()
	unit := "The bridge opened in March."
	c := newTestClient(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	got := c.Decompose(context.Background(), Input{Text: unit})
	if !reflect.DeepEqual(got, []string{unit}) {
		t.Errorf("Decompose() on missing tool call = %#v, want single-claim fallback %#v", got, []string{unit})
	}
}

// TestDecomposeRealisticUnit is the end-to-end check for this card: a realistic
// multi-claim utterance carrying coreference, an opinion, and a hedge is fed
// through the real Decompose path (request shaping, transport, parsing, cleaning,
// cap) against the faked LLM client, and the resolved atomic claims that the
// verify path will consume come back. This exercises the exact contract the live
// path will call without wiring it.
func TestDecomposeRealisticUnit(t *testing.T) {
	t.Parallel()
	// The model resolves "he"/"that" against the context, drops the opinion
	// ("I think it's a great deal") and the hedge ("maybe"), and emits the two
	// verifiable assertions as standalone sentences.
	modelClaims := []string{
		"Senator Smith voted for the 2023 infrastructure bill.",
		"The 2023 infrastructure bill allocated one trillion dollars.",
	}
	c := newTestClient(t, Config{MaxClaimsPerUnit: 4}, claimsResponder(t, modelClaims))

	got := c.Decompose(context.Background(), Input{
		Text:    "He voted for it, and I think it's a great deal - maybe a trillion dollars.",
		Speaker: "Host",
		Context: "The host is talking about Senator Smith and the 2023 infrastructure bill.",
	})

	if !reflect.DeepEqual(got, modelClaims) {
		t.Fatalf("Decompose() = %#v, want resolved atomic claims %#v", got, modelClaims)
	}
}

func TestDecomposePromptLanguage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		locale domain.Locale
		want   string
	}{
		{"default keeps english prompt", domain.LocaleEnglish, systemPrompt},
		{"french locale uses french prompt", domain.LocaleFrench, systemPromptFR},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var captured struct {
				System []struct {
					Text string `json:"text"`
				} `json:"system"`
			}
			c := newTestClient(t, Config{Locale: tc.locale}, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &captured)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claims": []any{}}))
			})

			c.Decompose(context.Background(), Input{Text: "une affirmation"})
			if len(captured.System) == 0 {
				t.Fatal("expected a system prompt")
			}
			if captured.System[0].Text != tc.want {
				t.Errorf("system prompt = %q, want %q", captured.System[0].Text, tc.want)
			}
		})
	}
}

func TestDecomposeFrenchUserMessageLabels(t *testing.T) {
	t.Parallel()
	var captured struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	c := newTestClient(t, Config{Locale: domain.LocaleFrench}, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"claims": []any{"Le Senateur Dupont a vote pour la loi."}}))
	})

	c.Decompose(context.Background(), Input{
		Text:    "Il a vote pour cette loi.",
		Speaker: "Animateur",
		Context: "L'animateur parle du Senateur Dupont et de la loi de finances.",
	})

	if len(captured.Messages) == 0 || len(captured.Messages[0].Content) == 0 {
		t.Fatal("expected a user message with content")
	}
	user := captured.Messages[0].Content[0].Text
	// French labels frame the prompt; the English labels must not leak through.
	for _, want := range []string{"Locuteur :", "Contexte recent :", "Enonce :", "Animateur"} {
		if !strings.Contains(user, want) {
			t.Errorf("french user message missing %q; got %q", want, user)
		}
	}
	for _, banned := range []string{"Speaker:", "Recent context:", "Statement:"} {
		if strings.Contains(user, banned) {
			t.Errorf("french user message leaked english label %q; got %q", banned, user)
		}
	}
}

func TestDecomposeFrenchMultiClaimSplit(t *testing.T) {
	t.Parallel()
	// The model resolves "il" against the context, drops the opinion, and emits
	// two French atomic claims; the adapter returns them verbatim.
	modelClaims := []string{
		"Le Senateur Dupont a vote pour la loi de finances de 2023.",
		"La loi de finances de 2023 a alloue mille milliards d'euros.",
	}
	c := newTestClient(t, Config{Locale: domain.LocaleFrench, MaxClaimsPerUnit: 4}, claimsResponder(t, modelClaims))

	got := c.Decompose(context.Background(), Input{
		Text:    "Il a vote pour, et je pense que c'est une bonne chose - mille milliards d'euros.",
		Speaker: "Animateur",
		Context: "L'animateur parle du Senateur Dupont et de la loi de finances de 2023.",
	})

	if !reflect.DeepEqual(got, modelClaims) {
		t.Fatalf("Decompose() = %#v, want resolved French atomic claims %#v", got, modelClaims)
	}
}

func TestDecomposeFrenchErrorFallsBackToSingleClaim(t *testing.T) {
	t.Parallel()
	// On a model error the French path degrades the same way: the unit comes back
	// verbatim as a single claim so the live path never stalls.
	unit := "Le pont a ouvert en mars 2023."
	c := newTestClient(t, Config{Locale: domain.LocaleFrench}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	got := c.Decompose(context.Background(), Input{Text: unit})
	if !reflect.DeepEqual(got, []string{unit}) {
		t.Errorf("Decompose() on error = %#v, want single-claim fallback %#v", got, []string{unit})
	}
}
