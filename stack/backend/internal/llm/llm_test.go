package llm

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

const testTool = "record_test_verdict"

// testVerdict is a caller-supplied result type, standing in for stance's and
// check-worthiness's verdicts: the transport must decode into it without knowing
// what its fields mean.
type testVerdict struct {
	Flag   bool   `json:"flag"`
	Reason string `json:"reason"`
}

func testRequest() Request {
	return Request{
		System: "You classify a statement.",
		User:   "Statement: anything",
		Tool: Tool{
			Name:        testTool,
			Description: "Record the test verdict.",
			Properties: map[string]any{
				"flag":   map[string]any{"type": "boolean"},
				"reason": map[string]any{"type": "string"},
			},
			Required: []string{"flag", "reason"},
		},
		MaxTokens: 64,
	}
}

// toolUseResponse builds a minimal valid Messages response carrying one forced
// testTool call with the given input, so a test can fake the model's reply
// without the network.
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
		"model": DefaultModel,
		"content": []map[string]any{
			{"type": "tool_use", "id": "toolu_test", "name": testTool, "input": json.RawMessage(raw)},
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
// and response parsing are exercised without hitting the real API. The empty
// model exercises the DefaultModel fallback.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient("test-key", "", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClientRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := NewClient("", ""); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

func TestClassifyResponseHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		want    testVerdict
		wantErr string // substring; empty means the call must succeed
	}{
		{
			name: "decodes tool input into caller type",
			body: toolUseResponse(t, map[string]any{"flag": true, "reason": "because"}),
			want: testVerdict{Flag: true, Reason: "because"},
		},
		{
			name:    "missing tool call errors",
			body:    `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr: "no " + testTool,
		},
		{
			name:    "malformed tool input errors",
			body:    toolUseResponse(t, map[string]any{"flag": "not-a-bool", "reason": ""}),
			wantErr: "decode " + testTool,
		},
		{
			name:    "transport error propagates",
			status:  http.StatusInternalServerError,
			body:    `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
			wantErr: "forced tool call",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.body)
			})

			got, err := Classify[testVerdict](context.Background(), c, testRequest())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got != tc.want {
				t.Errorf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClassifyForcesToolCallAtTempZero(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"flag": false, "reason": ""}))
	}))
	t.Cleanup(server.Close)

	c, err := NewClient("test-key", "claude-test-model", option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := Classify[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if captured["model"] != "claude-test-model" {
		t.Errorf("model = %v, want the supplied claude-test-model", captured["model"])
	}
	if captured["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", captured["temperature"])
	}
	if captured["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v, want 64", captured["max_tokens"])
	}
	choice, ok := captured["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "tool" || choice["name"] != testTool {
		t.Errorf("tool_choice = %v, want forced %s", captured["tool_choice"], testTool)
	}
}

func TestNewClientDefaultsModelWhenEmpty(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{"flag": false, "reason": ""}))
	})

	if _, err := Classify[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured["model"] != DefaultModel {
		t.Errorf("model = %v, want default %s", captured["model"], DefaultModel)
	}
}
