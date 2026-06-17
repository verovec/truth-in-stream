package checkworthy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_check_worthiness tool call with the given input, so a test can fake the
// model's verdict without the network.
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
	return newTestClientLocale(t, domain.LocaleEnglish, handler)
}

// newTestClientLocale points a locale-configured Client at a fake Anthropic
// server, so prompt-language selection is exercised without the network.
func newTestClientLocale(t *testing.T, locale domain.Locale, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{APIKey: "test-key", Locale: locale}, option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// captureSystem returns a handler that records the request's system prompt and
// replies with a fixed not-check-worthy verdict, so a test can assert which
// prompt language the client sent.
func captureSystem(t *testing.T, into *string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}
		if len(body.System) > 0 {
			*into = body.System[0].Text
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"check_worthy": false, "reason": ""}))
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestCheckWorthyTrue(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"check_worthy": true,
			"reason":       "",
		}))
	})

	got, err := c.CheckWorthy(context.Background(), "Unemployment fell to four percent last quarter.")
	if err != nil {
		t.Fatalf("CheckWorthy: %v", err)
	}
	if !got {
		t.Fatal("expected a check-worthy claim")
	}
}

func TestCheckWorthyFalse(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"check_worthy": false,
			"reason":       "personal casual declarative, not a public claim",
		}))
	})

	got, err := c.CheckWorthy(context.Background(), "I had coffee this morning.")
	if err != nil {
		t.Fatalf("CheckWorthy: %v", err)
	}
	if got {
		t.Fatal("expected a casual declarative to be not check-worthy")
	}
}

func TestCheckWorthyForcesStructuredToolCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"check_worthy": false, "reason": ""}))
	})

	if _, err := c.CheckWorthy(context.Background(), "anything"); err != nil {
		t.Fatalf("CheckWorthy: %v", err)
	}

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

func TestCheckWorthyTransportErrorPropagates(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	if _, err := c.CheckWorthy(context.Background(), "a claim"); err == nil {
		t.Fatal("expected an error when the API returns 500")
	}
}

func TestCheckWorthyMissingToolCallErrors(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	_, err := c.CheckWorthy(context.Background(), "a claim")
	if err == nil || !strings.Contains(err.Error(), "no record_check_worthiness") {
		t.Fatalf("expected a missing-tool-call error, got %v", err)
	}
}

func TestCheckWorthyPromptLanguage(t *testing.T) {
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
			var sent string
			c := newTestClientLocale(t, tc.locale, captureSystem(t, &sent))
			if _, err := c.CheckWorthy(context.Background(), "une affirmation"); err != nil {
				t.Fatalf("CheckWorthy: %v", err)
			}
			if sent != tc.want {
				t.Errorf("system prompt = %q, want %q", sent, tc.want)
			}
		})
	}
}

func TestCheckWorthyFrenchVerdicts(t *testing.T) {
	t.Parallel()
	// The model's judgment is the same across locales; the French prompt only
	// changes the prompt language. These exercise the gate's intent for French
	// inputs: a factual statement is kept, an opinion and a question are dropped.
	tests := []struct {
		name        string
		text        string
		checkWorthy bool
	}{
		{"french fact kept", "Le chomage est tombe a sept pour cent au dernier trimestre.", true},
		{"french opinion dropped", "Je pense que ce gouvernement est le pire de notre histoire.", false},
		{"french question dropped", "Pensez-vous que les impots vont baisser cette annee ?", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClientLocale(t, domain.LocaleFrench, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"check_worthy": tc.checkWorthy, "reason": ""}))
			})
			got, err := c.CheckWorthy(context.Background(), tc.text)
			if err != nil {
				t.Fatalf("CheckWorthy: %v", err)
			}
			if got != tc.checkWorthy {
				t.Errorf("CheckWorthy(%q) = %v, want %v", tc.text, got, tc.checkWorthy)
			}
		})
	}
}
