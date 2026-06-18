package digestsummary

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/verovec/truth-in-stream/backend/internal/report"
)

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_card_summaries tool call with the given input, so a test can fake the
// model's reply without the network.
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

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestSummarizeMapsByIdentifier(t *testing.T) {
	t.Parallel()
	var gotUser string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}
		if len(body.Messages) > 0 && len(body.Messages[0].Content) > 0 {
			gotUser = body.Messages[0].Content[0].Text
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"summaries": []map[string]any{
				{"id": "VER-104", "summary": "Reworked the verdict display."},
				{"id": "VER-105", "summary": "Added a golden evaluation."},
			},
		}))
	})

	out, err := c.Summarize(context.Background(), []report.CardInput{
		{ID: "VER-104", Title: "Two-axis verdict UI", Subjects: []string{"feat: verdict UI (VER-104)"}},
		{ID: "VER-105", Title: "Golden eval"},
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if out["VER-104"] != "Reworked the verdict display." || out["VER-105"] != "Added a golden evaluation." {
		t.Fatalf("summaries = %+v", out)
	}
	// The card titles and commit subjects must reach the model.
	for _, want := range []string{"VER-104", "Two-axis verdict UI", "feat: verdict UI (VER-104)"} {
		if !strings.Contains(gotUser, want) {
			t.Errorf("user message missing %q: %s", want, gotUser)
		}
	}
}

func TestSummarizeEmptyCardsSkipsCall(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("Summarize must not call the API for an empty card list")
	})
	out, err := c.Summarize(context.Background(), nil)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty map, got %+v", out)
	}
}

func TestSummarizeTransportErrorPropagates(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.Summarize(context.Background(), []report.CardInput{{ID: "VER-1", Title: "x"}}); err == nil {
		t.Fatal("want error when the API fails, got nil")
	}
}
