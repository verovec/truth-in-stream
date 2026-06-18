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

// TestProviderParityAcrossStageSchemas is the end-to-end exercise of the factory
// under both LLM_PROVIDER values across the real stage schema shapes the six
// callers use: a boolean verdict (stance / check-worthiness / evidence gate), an
// enum value (claim typing), a string array (claim decomposition), and a nested
// object array (the verifier's citations). It drives every shape through both
// providers via NewClient and asserts each decodes to the same typed result, and
// that the anthropic-default path keys off the same DefaultModel the codebase
// shipped with. Fakes stand in for both wire formats; no real API call is made.
func TestProviderParityAcrossStageSchemas(t *testing.T) {
	t.Parallel()

	type boolVerdict struct {
		Flag   bool   `json:"flag"`
		Reason string `json:"reason"`
	}
	type enumVerdict struct {
		ClaimType string `json:"claim_type"`
	}
	type listResult struct {
		Claims []string `json:"claims"`
	}
	type citation struct {
		EvidenceID string `json:"evidence_id"`
		QuotedSpan string `json:"quoted_span"`
	}
	type verifyResult struct {
		Verdict   string     `json:"verdict"`
		Citations []citation `json:"citations"`
	}

	boolReq := Request{
		System: "judge", User: "x", MaxTokens: 64,
		Tool: Tool{
			Name: "record_bool", Description: "boolean verdict",
			Properties: map[string]any{
				"flag":   map[string]any{"type": "boolean"},
				"reason": map[string]any{"type": "string"},
			},
			Required: []string{"flag", "reason"},
		},
	}
	enumReq := Request{
		System: "type", User: "x", MaxTokens: 64,
		Tool: Tool{
			Name: "record_type", Description: "claim type",
			Properties: map[string]any{
				"claim_type": map[string]any{"type": "string", "enum": []string{"statistic", "opinion"}},
			},
			Required: []string{"claim_type"},
		},
	}
	listReq := Request{
		System: "split", User: "x", MaxTokens: 256,
		Tool: Tool{
			Name: "record_claims", Description: "atomic claims",
			Properties: map[string]any{
				"claims": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			Required: []string{"claims"},
		},
	}
	verifyReq := Request{
		System: "verify", User: "x", MaxTokens: 512,
		Tool: Tool{
			Name: "record_verdict", Description: "credibility verdict",
			Properties: map[string]any{
				"verdict": map[string]any{"type": "string", "enum": []string{"credible", "disputed"}},
				"citations": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"evidence_id": map[string]any{"type": "string"},
							"quoted_span": map[string]any{"type": "string"},
						},
						"required": []string{"evidence_id", "quoted_span"},
					},
				},
			},
			Required: []string{"verdict", "citations"},
		},
	}

	wantBool := boolVerdict{Flag: true, Reason: "because"}
	wantEnum := enumVerdict{ClaimType: "statistic"}
	wantList := listResult{Claims: []string{"one", "two"}}
	wantVerify := verifyResult{Verdict: "disputed", Citations: []citation{{EvidenceID: "e1", QuotedSpan: "span"}}}

	runStages := func(t *testing.T, c *Client) {
		t.Helper()
		gotBool, err := Classify[boolVerdict](context.Background(), c, boolReq)
		if err != nil || gotBool != wantBool {
			t.Fatalf("bool stage: got %+v err %v, want %+v", gotBool, err, wantBool)
		}
		gotEnum, err := Classify[enumVerdict](context.Background(), c, enumReq)
		if err != nil || gotEnum != wantEnum {
			t.Fatalf("enum stage: got %+v err %v, want %+v", gotEnum, err, wantEnum)
		}
		gotList, err := Classify[listResult](context.Background(), c, listReq)
		if err != nil || strings.Join(gotList.Claims, ",") != "one,two" {
			t.Fatalf("list stage: got %+v err %v, want %+v", gotList, err, wantList)
		}
		gotVerify, err := Classify[verifyResult](context.Background(), c, verifyReq)
		if err != nil || gotVerify.Verdict != wantVerify.Verdict || len(gotVerify.Citations) != 1 ||
			gotVerify.Citations[0] != wantVerify.Citations[0] {
			t.Fatalf("verify stage: got %+v err %v, want %+v", gotVerify, err, wantVerify)
		}
	}

	// args returns the structured result for the named tool, so one fake server
	// can answer every stage by tool name.
	args := func(name string) map[string]any {
		switch name {
		case "record_bool":
			return map[string]any{"flag": true, "reason": "because"}
		case "record_type":
			return map[string]any{"claim_type": "statistic"}
		case "record_claims":
			return map[string]any{"claims": []any{"one", "two"}}
		case "record_verdict":
			return map[string]any{"verdict": "disputed", "citations": []any{map[string]any{"evidence_id": "e1", "quoted_span": "span"}}}
		default:
			return map[string]any{}
		}
	}

	t.Run("deepseek default routes and decodes every stage", func(t *testing.T) {
		t.Parallel()
		var sawModel string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			sawModel = body.Model
			name := body.Tools[0].Function.Name
			argsJSON, _ := json.Marshal(args(name))
			resp, _ := json.Marshal(map[string]any{
				"id": "c", "object": "chat.completion", "created": 1, "model": body.Model,
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":       "assistant",
						"tool_calls": []map[string]any{{"id": "t", "type": "function", "function": map[string]any{"name": name, "arguments": string(argsJSON)}}},
					},
					"finish_reason": "tool_calls",
				}},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(resp)
		}))
		t.Cleanup(server.Close)

		// Empty provider must default to deepseek across every stage schema.
		c, err := NewClient(Config{DeepSeekAPIKey: "k"}, WithBaseURL(server.URL), WithMaxRetries(0))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		runStages(t, c)
		if sawModel != defaultDeepSeekModel {
			t.Errorf("deepseek default model = %q, want %q", sawModel, defaultDeepSeekModel)
		}
	})

	t.Run("anthropic routes and decodes every stage", func(t *testing.T) {
		t.Parallel()
		var sawModel string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			sawModel = body.Model
			name := body.Tools[0].Name
			in, _ := json.Marshal(args(name))
			resp, _ := json.Marshal(map[string]any{
				"id": "m", "type": "message", "role": "assistant", "model": body.Model,
				"content":     []map[string]any{{"type": "tool_use", "id": "t", "name": name, "input": json.RawMessage(in)}},
				"stop_reason": "tool_use",
				"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(resp)
		}))
		t.Cleanup(server.Close)

		// Explicit anthropic must route to the Claude path, byte-for-byte the shipped
		// behavior, and key off the same DefaultModel.
		c, err := NewClient(Config{Provider: ProviderAnthropic, APIKey: "k"}, WithBaseURL(server.URL), WithMaxRetries(0))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		runStages(t, c)
		if sawModel != DefaultModel {
			t.Errorf("anthropic model = %q, want %q (unchanged from shipped behavior)", sawModel, DefaultModel)
		}
	})

	t.Run("gemini routes and decodes every stage", func(t *testing.T) {
		t.Parallel()
		var sawForced bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				ToolConfig struct {
					FunctionCallingConfig struct {
						Mode                 string   `json:"mode"`
						AllowedFunctionNames []string `json:"allowedFunctionNames"`
					} `json:"functionCallingConfig"`
				} `json:"toolConfig"`
				Tools []struct {
					FunctionDeclarations []struct {
						Name string `json:"name"`
					} `json:"functionDeclarations"`
				} `json:"tools"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			name := body.Tools[0].FunctionDeclarations[0].Name
			if body.ToolConfig.FunctionCallingConfig.Mode == "ANY" &&
				len(body.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) == 1 &&
				body.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] == name {
				sawForced = true
			}
			resp, _ := json.Marshal(map[string]any{
				"candidates": []map[string]any{{
					"content":      map[string]any{"role": "model", "parts": []map[string]any{{"functionCall": map[string]any{"name": name, "args": args(name)}}}},
					"finishReason": "STOP",
				}},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(resp)
		}))
		t.Cleanup(server.Close)

		c, err := NewClient(Config{Provider: ProviderGemini, GeminiAPIKey: "k"}, WithBaseURL(server.URL))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		runStages(t, c)
		if !sawForced {
			t.Error("gemini path did not force the single named function call (mode ANY + allowed name)")
		}
	})
}
