package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testTool = "record_test_verdict"

// testVerdict is a caller-supplied result type, standing in for the real
// classifiers' verdicts: the transport must decode into it without knowing what
// its fields mean.
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

// anthropicToolResponse builds a minimal valid Anthropic Messages response
// carrying one forced testTool call with the given input, so a test can fake the
// model's reply without the network.
func anthropicToolResponse(t *testing.T, input map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"role":  "assistant",
		"model": defaultAnthropicModel,
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

// geminiFunctionResponse builds a minimal valid Gemini GenerateContent response
// carrying one forced testTool function call with the given args, matching the
// wire shape the Gen AI SDK decodes.
func geminiFunctionResponse(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"functionCall": map[string]any{"name": name, "args": args}},
					},
				},
				"finishReason": "STOP",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal gemini response: %v", err)
	}
	return string(body)
}

// anthropicTestClient points an Anthropic-provider Client at a fake server, so
// request shaping and response parsing are exercised without hitting the real
// API. The empty model exercises the default-model fallback.
func anthropicTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient(Config{APIKey: "test-key"}, WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// geminiTestClient points a Gemini-provider Client at a fake server.
func geminiTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient(Config{Provider: ProviderGemini, GeminiAPIKey: "test-key"}, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClientProviderSelectionAndKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     Config
		wantErr string // substring; empty means construction must succeed
	}{
		{
			name: "empty provider defaults to anthropic",
			cfg:  Config{APIKey: "k"},
		},
		{
			name: "explicit anthropic",
			cfg:  Config{Provider: ProviderAnthropic, APIKey: "k"},
		},
		{
			name: "gemini with key",
			cfg:  Config{Provider: ProviderGemini, GeminiAPIKey: "k"},
		},
		{
			name:    "anthropic missing key degrades like before",
			cfg:     Config{Provider: ProviderAnthropic},
			wantErr: "api key is required",
		},
		{
			name:    "gemini missing key degrades like before",
			cfg:     Config{Provider: ProviderGemini},
			wantErr: "api key is required",
		},
		{
			name:    "unknown provider fails fast",
			cfg:     Config{Provider: "mistral", APIKey: "k"},
			wantErr: `unknown provider "mistral"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
		})
	}
}

func TestClassifyRejectsNonPositiveMaxTokens(t *testing.T) {
	t.Parallel()
	// No server is contacted: the guard short-circuits before any provider call,
	// so a caller bug surfaces uniformly across providers rather than as an
	// uncapped Gemini request or an Anthropic 400.
	c, err := NewClient(Config{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	req := testRequest()
	req.MaxTokens = 0
	if _, err := Classify[testVerdict](context.Background(), c, req); err == nil ||
		!strings.Contains(err.Error(), "MaxTokens must be positive") {
		t.Fatalf("err = %v, want a MaxTokens guard error", err)
	}
}

func TestClassifyAnthropicResponseHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		want    testVerdict
		wantErr string
	}{
		{
			name: "decodes tool input into caller type",
			body: anthropicToolResponse(t, map[string]any{"flag": true, "reason": "because"}),
			want: testVerdict{Flag: true, Reason: "because"},
		},
		{
			name:    "missing tool call errors",
			body:    `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr: "no " + testTool,
		},
		{
			name:    "malformed tool input errors",
			body:    anthropicToolResponse(t, map[string]any{"flag": "not-a-bool", "reason": ""}),
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
			c := anthropicTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
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

func TestClassifyAnthropicForcesToolCallAtTempZero(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicToolResponse(t, map[string]any{"flag": false, "reason": ""}))
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(Config{APIKey: "test-key", Model: "claude-test-model"}, WithBaseURL(server.URL), WithMaxRetries(0))
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

func TestNewClientDefaultsAnthropicModelWhenEmpty(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := anthropicTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicToolResponse(t, map[string]any{"flag": false, "reason": ""}))
	})

	if _, err := Classify[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured["model"] != DefaultModel {
		t.Errorf("model = %v, want default %s", captured["model"], DefaultModel)
	}
}

func TestClassifyGeminiDecodesForcedFunctionCall(t *testing.T) {
	t.Parallel()
	c := geminiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, geminiFunctionResponse(t, testTool, map[string]any{"flag": true, "reason": "because"}))
	})

	got, err := Classify[testVerdict](context.Background(), c, testRequest())
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if want := (testVerdict{Flag: true, Reason: "because"}); got != want {
		t.Errorf("verdict = %+v, want %+v", got, want)
	}
}

func TestClassifyGeminiForcesFunctionCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	var path string
	c := geminiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, geminiFunctionResponse(t, testTool, map[string]any{"flag": false, "reason": ""}))
	})

	if _, err := Classify[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if !strings.Contains(path, defaultGeminiModel) {
		t.Errorf("request path = %q, want it to name the default gemini model %s", path, defaultGeminiModel)
	}
	gc, ok := captured["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing in %v", captured)
	}
	if gc["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", gc["temperature"])
	}
	toolConfig, ok := captured["toolConfig"].(map[string]any)
	if !ok {
		t.Fatalf("toolConfig missing in %v", captured)
	}
	fcc, ok := toolConfig["functionCallingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("functionCallingConfig missing in %v", toolConfig)
	}
	if fcc["mode"] != "ANY" {
		t.Errorf("mode = %v, want ANY (forced)", fcc["mode"])
	}
	allowed, ok := fcc["allowedFunctionNames"].([]any)
	if !ok || len(allowed) != 1 || allowed[0] != testTool {
		t.Errorf("allowedFunctionNames = %v, want [%s]", fcc["allowedFunctionNames"], testTool)
	}
}

func TestClassifyGeminiErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "transport error propagates",
			status:  http.StatusInternalServerError,
			body:    `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`,
			wantErr: "forced tool call",
		},
		{
			name:    "missing function call errors",
			body:    `{"candidates":[{"content":{"role":"model","parts":[{"text":"no call here"}]},"finishReason":"STOP"}]}`,
			wantErr: "no " + testTool,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := geminiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := Classify[testVerdict](context.Background(), c, testRequest())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
