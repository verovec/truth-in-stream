package verify

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// toolUseResponse builds a minimal valid Messages response carrying one forced
// record_verdict tool call with the given input, so a test can fake the model's
// verdict without the network.
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

func TestNewRequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{APIKey: ""}); err == nil {
		t.Fatal("expected an error when the API key is empty")
	}
}

// TestVerifyEntailment exercises the model-judgment path end to end against a
// faked LLM, including the headline adversarial pairs the redesign exists to fix:
// evidence stating the opposite of the claim yields refutes, and a same-topic but
// non-bearing passage yields not_enough_info rather than a false support.
func TestVerifyEntailment(t *testing.T) {
	t.Parallel()

	const autismClaim = "the vaccine causes autism"
	refutingPassage := Passage{ID: "wiki:vaccine:0", Text: "Large studies show the vaccine does not cause autism."}
	sameTopicPassage := Passage{ID: "wiki:vaccine:1", Text: "The vaccine is administered in two doses several weeks apart."}

	tests := []struct {
		name      string
		claim     string
		passages  []Passage
		toolInput map[string]any
		want      Result
	}{
		{
			name:     "opposite-truth evidence refutes",
			claim:    autismClaim,
			passages: []Passage{refutingPassage},
			toolInput: map[string]any{
				"verdict":    VerdictRefutes,
				"confidence": 0.93,
				"citations": []map[string]any{
					{"evidence_id": "wiki:vaccine:0", "quoted_span": "the vaccine does not cause autism"},
				},
				"rationale": "The passage directly states the vaccine does not cause autism.",
			},
			want: Result{
				Verdict:    VerdictRefutes,
				Confidence: 0.93,
				Citations:  []Citation{{EvidenceID: "wiki:vaccine:0", QuotedSpan: "the vaccine does not cause autism"}},
				Rationale:  "The passage directly states the vaccine does not cause autism.",
			},
		},
		{
			name:     "same-topic non-bearing passage yields not_enough_info",
			claim:    autismClaim,
			passages: []Passage{sameTopicPassage},
			toolInput: map[string]any{
				"verdict":    VerdictNotEnoughInfo,
				"confidence": 0.4,
				"citations":  []map[string]any{},
				"rationale":  "The passage is about dosing and does not address causation of autism.",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0.4,
				Citations:  []Citation{},
				Rationale:  "The passage is about dosing and does not address causation of autism.",
			},
		},
		{
			name:     "affirming evidence supports",
			claim:    "the treaty was signed in 1648",
			passages: []Passage{{ID: "claim:42", Text: "The Peace of Westphalia treaty was signed in 1648."}},
			toolInput: map[string]any{
				"verdict":    VerdictSupports,
				"confidence": 0.88,
				"citations": []map[string]any{
					{"evidence_id": "claim:42", "quoted_span": "signed in 1648"},
				},
				"rationale": "The passage states the treaty was signed in 1648.",
			},
			want: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.88,
				Citations:  []Citation{{EvidenceID: "claim:42", QuotedSpan: "signed in 1648"}},
				Rationale:  "The passage states the treaty was signed in 1648.",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, toolUseResponse(t, tc.toolInput))
			})

			got, err := c.Verify(context.Background(), tc.claim, tc.passages)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			assertResultEqual(t, got, tc.want)
		})
	}
}

// TestVerifyForcesStructuredToolCall asserts the request is shaped as a single
// forced record_verdict call at temperature zero on the configured model.
func TestVerifyForcesStructuredToolCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"verdict":    VerdictNotEnoughInfo,
			"confidence": 0.1,
			"citations":  []map[string]any{},
			"rationale":  "",
		}))
	})

	if _, err := c.Verify(context.Background(), "a claim", []Passage{{ID: "e1", Text: "some text"}}); err != nil {
		t.Fatalf("Verify: %v", err)
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

func TestVerifyRequiresPassages(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("Verify must not call the model when no passages are supplied")
	})
	if _, err := c.Verify(context.Background(), "a claim", nil); err == nil {
		t.Fatal("expected an error when no evidence passages are supplied")
	}
}

func TestVerifyTransportErrorPropagates(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	if _, err := c.Verify(context.Background(), "a claim", []Passage{{ID: "e1", Text: "t"}}); err == nil {
		t.Fatal("expected an error when the API returns 500")
	}
}

func TestVerifyMissingToolCallErrors(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"no tool here"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	_, err := c.Verify(context.Background(), "a claim", []Passage{{ID: "e1", Text: "t"}})
	if err == nil || !strings.Contains(err.Error(), "no record_verdict") {
		t.Fatalf("expected a missing-tool-call error, got %v", err)
	}
}

// TestVerifyDropsFabricatedCitationOverWire confirms the citation guard runs on
// the model-judgment path, not just in isolation: a faked verdict that cites a
// passage never supplied comes back downgraded to not_enough_info.
func TestVerifyDropsFabricatedCitationOverWire(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"verdict":    VerdictSupports,
			"confidence": 0.9,
			"citations": []map[string]any{
				{"evidence_id": "ghost:99", "quoted_span": "not in any supplied passage"},
			},
			"rationale": "fabricated grounding",
		}))
	})

	got, err := c.Verify(context.Background(), "a claim", []Passage{{ID: "e1", Text: "real evidence text"}})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Verdict != VerdictNotEnoughInfo {
		t.Errorf("verdict = %q, want %q after dropping the fabricated citation", got.Verdict, VerdictNotEnoughInfo)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %v, want none", got.Citations)
	}
}

// TestValidateCitations exercises the deterministic guard directly, without the
// model: fabricated ids and non-substring spans are dropped, supports/refutes
// left ungrounded are downgraded to not_enough_info, and an already-NEI verdict
// keeps its surviving citations untouched.
func TestValidateCitations(t *testing.T) {
	t.Parallel()
	passages := []Passage{
		{ID: "e1", Text: "The Earth orbits the Sun."},
		{ID: "e2", Text: "Water boils at 100 degrees Celsius at sea level."},
		{ID: "dup", Text: "first body"},
		{ID: "dup", Text: "second body"},
	}

	tests := []struct {
		name string
		in   Result
		want Result
	}{
		{
			name: "valid citation kept",
			in: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.9,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "ok",
			},
			want: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.9,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "ok",
			},
		},
		{
			name: "fabricated evidence_id dropped and verdict downgraded",
			in: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.95,
				Citations:  []Citation{{EvidenceID: "nope", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "hallucinated id",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0,
				Citations:  nil,
				Rationale:  "hallucinated id",
			},
		},
		{
			name: "non-substring span dropped and verdict downgraded",
			in: Result{
				Verdict:    VerdictRefutes,
				Confidence: 0.8,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "water freezes at 100"}},
				Rationale:  "span not present",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0,
				Citations:  nil,
				Rationale:  "span not present",
			},
		},
		{
			name: "supports kept when at least one citation survives",
			in: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.7,
				Citations: []Citation{
					{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"},
					{EvidenceID: "ghost", QuotedSpan: "Earth orbits the Sun"},
				},
				Rationale: "one real, one fake",
			},
			want: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.7,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "one real, one fake",
			},
		},
		{
			name: "not_enough_info not upgraded and keeps surviving citations",
			in: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0.3,
				Citations: []Citation{
					{EvidenceID: "e2", QuotedSpan: "boils at 100 degrees"},
					{EvidenceID: "ghost", QuotedSpan: "anything"},
				},
				Rationale: "inconclusive",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0.3,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "boils at 100 degrees"}},
				Rationale:  "inconclusive",
			},
		},
		{
			name: "not_enough_info with no valid citations is left as-is",
			in: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0.2,
				Citations:  []Citation{{EvidenceID: "ghost", QuotedSpan: "anything"}},
				Rationale:  "nothing relevant",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0.2,
				Citations:  []Citation{},
				Rationale:  "nothing relevant",
			},
		},
		{
			name: "empty quoted_span dropped and supports downgraded",
			in: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.91,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: ""}},
				Rationale:  "blank span must not ground a verdict",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0,
				Citations:  nil,
				Rationale:  "blank span must not ground a verdict",
			},
		},
		{
			name: "whitespace-only quoted_span dropped and refutes downgraded",
			in: Result{
				Verdict:    VerdictRefutes,
				Confidence: 0.77,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "   \t\n"}},
				Rationale:  "whitespace span grounds nothing",
			},
			want: Result{
				Verdict:    VerdictNotEnoughInfo,
				Confidence: 0,
				Citations:  nil,
				Rationale:  "whitespace span grounds nothing",
			},
		},
		{
			name: "duplicate evidence_id keeps a span valid against an earlier passage",
			in: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.85,
				Citations:  []Citation{{EvidenceID: "dup", QuotedSpan: "first body"}},
				Rationale:  "span matches the first of two same-id passages",
			},
			want: Result{
				Verdict:    VerdictSupports,
				Confidence: 0.85,
				Citations:  []Citation{{EvidenceID: "dup", QuotedSpan: "first body"}},
				Rationale:  "span matches the first of two same-id passages",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ValidateCitations(tc.in, passages)
			assertResultEqual(t, got, tc.want)
		})
	}
}

// TestValidateCitationsClampsConfidence asserts an out-of-range or NaN
// confidence from the model is pulled into the documented [0,1] range on the
// guard's return path, regardless of verdict, so no sentinel value reaches the
// caller.
func TestValidateCitationsClampsConfidence(t *testing.T) {
	t.Parallel()
	passages := []Passage{{ID: "e1", Text: "The Earth orbits the Sun."}}

	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{name: "above one clamped to one", in: 1.5, want: 1},
		{name: "below zero clamped to zero", in: -0.2, want: 0},
		{name: "nan becomes zero", in: math.NaN(), want: 0},
		{name: "in range untouched", in: 0.42, want: 0.42},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ValidateCitations(Result{
				Verdict:    VerdictSupports,
				Confidence: tc.in,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "grounded",
			}, passages)
			if got.Confidence != tc.want {
				t.Errorf("confidence = %v, want %v", got.Confidence, tc.want)
			}
		})
	}
}

func assertResultEqual(t *testing.T, got, want Result) {
	t.Helper()
	if got.Verdict != want.Verdict {
		t.Errorf("verdict = %q, want %q", got.Verdict, want.Verdict)
	}
	if got.Confidence != want.Confidence {
		t.Errorf("confidence = %v, want %v", got.Confidence, want.Confidence)
	}
	if got.Rationale != want.Rationale {
		t.Errorf("rationale = %q, want %q", got.Rationale, want.Rationale)
	}
	if len(got.Citations) != len(want.Citations) {
		t.Fatalf("citations = %v, want %v", got.Citations, want.Citations)
	}
	for i := range want.Citations {
		if got.Citations[i] != want.Citations[i] {
			t.Errorf("citation[%d] = %v, want %v", i, got.Citations[i], want.Citations[i])
		}
	}
}
