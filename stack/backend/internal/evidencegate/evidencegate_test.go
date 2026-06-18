package evidencegate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_fact_checkability tool call with the given input, so a test can fake
// the model's verdict without the network.
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
	c, err := New(Config{Provider: llm.ProviderAnthropic, APIKey: "test-key"}, llm.WithBaseURL(server.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestFactCheckableTrue(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"fact_checkable": true,
			"reason":         "",
		}))
	})

	got, err := c.FactCheckable(context.Background(), "The Eiffel Tower is 330 meters tall and was completed in 1889.")
	if err != nil {
		t.Fatalf("FactCheckable: %v", err)
	}
	if !got {
		t.Fatal("expected a citable factual passage")
	}
}

func TestFactCheckableFalse(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"fact_checkable": false,
			"reason":         "navigational disambiguation note, no checkable assertion",
		}))
	})

	got, err := c.FactCheckable(context.Background(), "This article is about the physical structure. For other uses, see Tower (disambiguation).")
	if err != nil {
		t.Fatalf("FactCheckable: %v", err)
	}
	if got {
		t.Fatal("expected a navigational passage to be not fact-checkable")
	}
}

func TestFactCheckableForcesStructuredToolCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"fact_checkable": true, "reason": ""}))
	})

	if _, err := c.FactCheckable(context.Background(), "anything"); err != nil {
		t.Fatalf("FactCheckable: %v", err)
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

func TestFactCheckableTransportErrorPropagates(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	if _, err := c.FactCheckable(context.Background(), "a passage"); err == nil {
		t.Fatal("expected an error when the API returns 500")
	}
}

func TestFactCheckableMissingToolCallErrors(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	_, err := c.FactCheckable(context.Background(), "a passage")
	if err == nil || !strings.Contains(err.Error(), "no record_fact_checkability") {
		t.Fatalf("expected a missing-tool-call error, got %v", err)
	}
}
