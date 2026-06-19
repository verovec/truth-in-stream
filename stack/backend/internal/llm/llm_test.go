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
// API. The provider is named explicitly because the empty-provider default is
// now DeepSeek; the empty model still exercises the default-model fallback.
func anthropicTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient(Config{Provider: ProviderAnthropic, APIKey: "test-key"}, WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// deepseekTestClient points a DeepSeek-provider Client at a fake OpenAI-compatible
// server. The empty provider exercises the default selection (DeepSeek) and the
// empty model the default-model fallback.
func deepseekTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewClient(Config{DeepSeekAPIKey: "test-key"}, WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// deepseekToolResponse builds a minimal valid OpenAI Chat Completions response
// carrying one forced testTool call with the given arguments, so a test can fake
// DeepSeek's reply without the network.
func deepseekToolResponse(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	return deepseekRawArgsResponse(t, name, string(raw), "tool_calls")
}

// deepseekRawArgsResponse builds an OpenAI Chat Completions response carrying one
// forced tool call whose arguments are the given raw string under the given
// finish_reason, so a test can fake the empty-arguments and truncated replies
// DeepSeek occasionally returns for a forced tool call.
func deepseekRawArgsResponse(t *testing.T, name, args, finishReason string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":      "chatcmpl_test",
		"object":  "chat.completion",
		"created": 1,
		"model":   defaultDeepSeekModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []map[string]any{
						{
							"id":       "call_test",
							"type":     "function",
							"function": map[string]any{"name": name, "arguments": args},
						},
					},
				},
				"finish_reason": finishReason,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// deepseekReasonResponse builds a minimal valid OpenAI Chat Completions response
// shaped like DeepSeek's thinking-mode reply: a reasoning_content chain-of-thought
// alongside one tool call carrying the verdict, so a test can fake the reasoning
// reply without the network.
func deepseekReasonResponse(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":      "chatcmpl_test",
		"object":  "chat.completion",
		"created": 1,
		"model":   "deepseek-v4-pro",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":              "assistant",
					"reasoning_content": "Let me weigh the passages against the claim...",
					"tool_calls": []map[string]any{
						{
							"id":       "call_test",
							"type":     "function",
							"function": map[string]any{"name": name, "arguments": string(raw)},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// deepseekProseResponse builds a thinking-mode reply where the model deliberated
// but answered in prose without calling the offered tool - the case the auto
// tool_choice allows and the reason path must surface as an error.
func deepseekProseResponse(t *testing.T) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":      "chatcmpl_test",
		"object":  "chat.completion",
		"created": 1,
		"model":   "deepseek-v4-pro",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":              "assistant",
					"reasoning_content": "thinking out loud",
					"content":           "I think the claim is credible.",
				},
				"finish_reason": "stop",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
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
			name: "empty provider defaults to deepseek",
			cfg:  Config{DeepSeekAPIKey: "k"},
		},
		{
			name: "explicit deepseek",
			cfg:  Config{Provider: ProviderDeepSeek, DeepSeekAPIKey: "k"},
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
			name:    "deepseek missing key degrades like the others",
			cfg:     Config{Provider: ProviderDeepSeek},
			wantErr: "api key is required",
		},
		{
			name:    "default (deepseek) missing key degrades like the others",
			cfg:     Config{APIKey: "k"},
			wantErr: "api key is required",
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
	c, err := NewClient(Config{DeepSeekAPIKey: "k"})
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

	c, err := NewClient(Config{Provider: ProviderAnthropic, APIKey: "test-key", Model: "claude-test-model"}, WithBaseURL(server.URL), WithMaxRetries(0))
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

func TestClassifyDeepSeekResponseHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		want    testVerdict
		wantErr string
	}{
		{
			name: "decodes tool arguments into caller type",
			body: deepseekToolResponse(t, testTool, map[string]any{"flag": true, "reason": "because"}),
			want: testVerdict{Flag: true, Reason: "because"},
		},
		{
			name:    "missing tool call errors",
			body:    `{"id":"c","object":"chat.completion","created":1,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"no tool here"},"finish_reason":"stop"}]}`,
			wantErr: "no " + testTool,
		},
		{
			name:    "wrong tool name errors",
			body:    deepseekToolResponse(t, "some_other_tool", map[string]any{"flag": true, "reason": "x"}),
			wantErr: "no " + testTool,
		},
		{
			name:    "malformed tool arguments errors",
			body:    deepseekToolResponse(t, testTool, map[string]any{"flag": "not-a-bool", "reason": ""}),
			wantErr: "decode " + testTool,
		},
		{
			name:    "empty tool arguments errors clearly",
			body:    deepseekRawArgsResponse(t, testTool, "", "tool_calls"),
			wantErr: "empty " + testTool,
		},
		{
			name:    "truncated tool call reports max_tokens",
			body:    deepseekRawArgsResponse(t, testTool, "", "length"),
			wantErr: "truncated",
		},
		{
			name:    "transport error propagates",
			status:  http.StatusInternalServerError,
			body:    `{"error":{"message":"boom","type":"server_error"}}`,
			wantErr: "forced tool call",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := deepseekTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
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

func TestClassifyDeepSeekForcesToolCallAtTempZero(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekToolResponse(t, testTool, map[string]any{"flag": false, "reason": ""}))
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(Config{Provider: ProviderDeepSeek, DeepSeekAPIKey: "test-key", Model: "deepseek-test-model"}, WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := Classify[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if captured["model"] != "deepseek-test-model" {
		t.Errorf("model = %v, want the supplied deepseek-test-model", captured["model"])
	}
	if captured["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", captured["temperature"])
	}
	if captured["max_tokens"] != float64(64) {
		t.Errorf("max_tokens = %v, want 64", captured["max_tokens"])
	}
	// DeepSeek rejects penalty knobs; they must never be sent.
	if _, ok := captured["frequency_penalty"]; ok {
		t.Errorf("frequency_penalty must not be sent, got %v", captured["frequency_penalty"])
	}
	if _, ok := captured["presence_penalty"]; ok {
		t.Errorf("presence_penalty must not be sent, got %v", captured["presence_penalty"])
	}
	choice, ok := captured["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool_choice = %v, want a forced function choice", captured["tool_choice"])
	}
	fn, ok := choice["function"].(map[string]any)
	if !ok || fn["name"] != testTool {
		t.Errorf("tool_choice.function = %v, want forced %s", choice["function"], testTool)
	}
	// Thinking must be disabled: DeepSeek's hybrid models default to thinking on
	// and reject a forced tool_choice while it is, returning 400.
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want {type: disabled} so the forced tool_choice is accepted", captured["thinking"])
	}
}

func TestNewClientDefaultsDeepSeekModelWhenEmpty(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := deepseekTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekToolResponse(t, testTool, map[string]any{"flag": false, "reason": ""}))
	})

	if _, err := Classify[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured["model"] != defaultDeepSeekModel {
		t.Errorf("model = %v, want default %s", captured["model"], defaultDeepSeekModel)
	}
}

func TestReasonRejectsNonPositiveMaxTokens(t *testing.T) {
	t.Parallel()
	c, err := NewClient(Config{DeepSeekAPIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	req := testRequest()
	req.MaxTokens = 0
	if _, err := Reason[testVerdict](context.Background(), c, req); err == nil ||
		!strings.Contains(err.Error(), "MaxTokens must be positive") {
		t.Fatalf("err = %v, want a MaxTokens guard error", err)
	}
}

func TestReasonDeepSeekResponseHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		want    testVerdict
		wantErr string
	}{
		{
			name: "decodes tool arguments from a thinking-mode reply",
			body: deepseekReasonResponse(t, testTool, map[string]any{"flag": true, "reason": "because"}),
			want: testVerdict{Flag: true, Reason: "because"},
		},
		{
			name:    "prose answer without a tool call errors",
			body:    deepseekProseResponse(t),
			wantErr: "no " + testTool,
		},
		{
			name:    "malformed tool arguments errors",
			body:    deepseekReasonResponse(t, testTool, map[string]any{"flag": "not-a-bool", "reason": ""}),
			wantErr: "decode " + testTool,
		},
		{
			name:    "transport error propagates",
			status:  http.StatusInternalServerError,
			body:    `{"error":{"message":"boom","type":"server_error"}}`,
			wantErr: "reasoning tool call",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := deepseekTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.body)
			})

			got, err := Reason[testVerdict](context.Background(), c, testRequest())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Reason: %v", err)
			}
			if got != tc.want {
				t.Errorf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReasonDeepSeekEnablesThinkingWithAutoToolChoice(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekReasonResponse(t, testTool, map[string]any{"flag": false, "reason": ""}))
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(Config{Provider: ProviderDeepSeek, DeepSeekAPIKey: "test-key", Model: "deepseek-v4-pro"}, WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := Reason[testVerdict](context.Background(), c, testRequest()); err != nil {
		t.Fatalf("Reason: %v", err)
	}

	if captured["model"] != "deepseek-v4-pro" {
		t.Errorf("model = %v, want the supplied deepseek-v4-pro", captured["model"])
	}
	// Thinking must be enabled on the reason path.
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Errorf("thinking = %v, want {type: enabled}", captured["thinking"])
	}
	// The tool_choice MUST be the auto string, never a forced (named) object:
	// DeepSeek rejects a forced named tool_choice while thinking is enabled.
	if captured["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v (%T), want the auto string", captured["tool_choice"], captured["tool_choice"])
	}
	// The penalty knobs DeepSeek rejects must still never be sent.
	if _, ok := captured["frequency_penalty"]; ok {
		t.Errorf("frequency_penalty must not be sent, got %v", captured["frequency_penalty"])
	}
	if _, ok := captured["presence_penalty"]; ok {
		t.Errorf("presence_penalty must not be sent, got %v", captured["presence_penalty"])
	}
}

func TestReasonAnthropicOffersToolWithAutoChoice(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, anthropicToolResponse(t, map[string]any{"flag": true, "reason": "because"}))
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(Config{Provider: ProviderAnthropic, APIKey: "test-key"}, WithBaseURL(server.URL), WithMaxRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := Reason[testVerdict](context.Background(), c, testRequest())
	if err != nil {
		t.Fatalf("Reason: %v", err)
	}
	if want := (testVerdict{Flag: true, Reason: "because"}); got != want {
		t.Errorf("verdict = %+v, want %+v", got, want)
	}
	choice, ok := captured["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "auto" {
		t.Errorf("tool_choice = %v, want an auto choice (never forced by name)", captured["tool_choice"])
	}
}

func TestReasonGeminiOffersFunctionWithAutoMode(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := geminiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, geminiFunctionResponse(t, testTool, map[string]any{"flag": true, "reason": "because"}))
	})

	got, err := Reason[testVerdict](context.Background(), c, testRequest())
	if err != nil {
		t.Fatalf("Reason: %v", err)
	}
	if want := (testVerdict{Flag: true, Reason: "because"}); got != want {
		t.Errorf("verdict = %+v, want %+v", got, want)
	}
	toolConfig, ok := captured["toolConfig"].(map[string]any)
	if !ok {
		t.Fatalf("toolConfig missing in %v", captured)
	}
	fcc, ok := toolConfig["functionCallingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("functionCallingConfig missing in %v", toolConfig)
	}
	if fcc["mode"] != "AUTO" {
		t.Errorf("mode = %v, want AUTO (offered, not forced)", fcc["mode"])
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
