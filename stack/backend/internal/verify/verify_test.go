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

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
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

// TestVerifyCredibility exercises the model-judgment path end to end against a
// faked LLM, including the headline cases the credibility reframe exists to get
// right: evidence stating the opposite of the claim yields disputed/evidence; an
// affirming passage yields credible/evidence; a same-topic but non-bearing passage
// no longer forces a verdict but falls back to a knowledge-basis credible verdict
// (the "Most people slip into addiction gradually" case); and a private/anecdotal
// claim is unverifiable.
func TestVerifyCredibility(t *testing.T) {
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
			name:     "opposite-truth evidence disputes",
			claim:    autismClaim,
			passages: []Passage{refutingPassage},
			toolInput: map[string]any{
				"verdict":    VerdictDisputed,
				"basis":      BasisEvidence,
				"confidence": 0.93,
				"citations": []map[string]any{
					{"evidence_id": "wiki:vaccine:0", "quoted_span": "the vaccine does not cause autism"},
				},
				"rationale": "The passage directly states the vaccine does not cause autism.",
			},
			want: Result{
				Verdict:    VerdictDisputed,
				Basis:      BasisEvidence,
				Confidence: 0.93,
				Citations:  []Citation{{EvidenceID: "wiki:vaccine:0", QuotedSpan: "the vaccine does not cause autism"}},
				Rationale:  "The passage directly states the vaccine does not cause autism.",
			},
		},
		{
			name:     "affirming evidence is credible",
			claim:    "the treaty was signed in 1648",
			passages: []Passage{{ID: "claim:42", Text: "The Peace of Westphalia treaty was signed in 1648."}},
			toolInput: map[string]any{
				"verdict":    VerdictCredible,
				"basis":      BasisEvidence,
				"confidence": 0.88,
				"citations": []map[string]any{
					{"evidence_id": "claim:42", "quoted_span": "signed in 1648"},
				},
				"rationale": "The passage states the treaty was signed in 1648.",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.88,
				Citations:  []Citation{{EvidenceID: "claim:42", QuotedSpan: "signed in 1648"}},
				Rationale:  "The passage states the treaty was signed in 1648.",
			},
		},
		{
			name:     "same-topic non-bearing passage falls back to knowledge credible",
			claim:    "most people slip into addiction gradually",
			passages: []Passage{sameTopicPassage},
			toolInput: map[string]any{
				"verdict":    VerdictCredible,
				"basis":      BasisKnowledge,
				"confidence": 0.55,
				"citations":  []map[string]any{},
				"rationale":  "No passage bears on the claim, but it is broadly consistent with general understanding of addiction.",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: 0.55,
				Citations:  []Citation{},
				Rationale:  "No passage bears on the claim, but it is broadly consistent with general understanding of addiction.",
			},
		},
		{
			name:     "private anecdotal claim is unverifiable",
			claim:    "my uncle quit smoking last tuesday",
			passages: []Passage{sameTopicPassage},
			toolInput: map[string]any{
				"verdict":    VerdictUnverifiable,
				"basis":      BasisKnowledge,
				"confidence": 0.2,
				"citations":  []map[string]any{},
				"rationale":  "A private anecdote no general knowledge can confirm or deny.",
			},
			want: Result{
				Verdict:    VerdictUnverifiable,
				Basis:      BasisKnowledge,
				Confidence: 0.2,
				Citations:  []Citation{},
				Rationale:  "A private anecdote no general knowledge can confirm or deny.",
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
			"verdict":    VerdictUnverifiable,
			"basis":      BasisKnowledge,
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

// TestVerifyRationaleAlwaysFrench pins the product rule that the viewer-facing
// rationale is French in every locale: both system prompts carry the explicit
// French-rationale instruction, whatever language they reason in.
func TestVerifyRationaleAlwaysFrench(t *testing.T) {
	t.Parallel()
	if !strings.Contains(systemPrompt, "Write the rationale in French") {
		t.Error("English system prompt lacks the French-rationale instruction")
	}
	if !strings.Contains(systemPromptFR, "Rédige le rationale en français") {
		t.Error("French system prompt lacks the French-rationale instruction")
	}
}

// TestVerifyPromptLocale asserts the credibility verifier reasons in the locale's
// language: the French locale sends the French system prompt, while the default
// locale keeps the English prompt (which itself instructs a French rationale).
// Only the system prompt language changes; the forced record_verdict tool call is
// identical across locales.
func TestVerifyPromptLocale(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		locale     domain.Locale
		wantSystem string
	}{
		{name: "default locale keeps English prompt", locale: domain.LocaleEnglish, wantSystem: systemPrompt},
		{name: "French locale sends French prompt", locale: domain.LocaleFrench, wantSystem: systemPromptFR},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var captured map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &captured)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
					"verdict":    VerdictUnverifiable,
					"basis":      BasisKnowledge,
					"confidence": 0.1,
					"citations":  []map[string]any{},
					"rationale":  "",
				}))
			}))
			t.Cleanup(server.Close)

			c, err := New(
				Config{Provider: llm.ProviderAnthropic, APIKey: "test-key", Locale: tc.locale},
				llm.WithBaseURL(server.URL), llm.WithMaxRetries(0),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if _, err := c.Verify(context.Background(), "a claim", []Passage{{ID: "e1", Text: "some text"}}); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got := systemText(t, captured["system"]); got != tc.wantSystem {
				t.Errorf("system = %q, want %q", got, tc.wantSystem)
			}
		})
	}
}

// systemText pulls the prompt out of the Anthropic request's system field, which
// serializes as an array of {type:text, text:...} content blocks rather than a
// bare string.
func systemText(t *testing.T, system any) string {
	t.Helper()
	blocks, ok := system.([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("system = %v, want a non-empty content-block array", system)
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("system[0] = %v, want a content block", blocks[0])
	}
	text, ok := block["text"].(string)
	if !ok {
		t.Fatalf("system[0].text = %v, want a string", block["text"])
	}
	return text
}

func TestVerifyNoPassagesJudgesFromKnowledge(t *testing.T) {
	t.Parallel()
	// The knowledge fallback for a sparse evidence corpus: with no passages the
	// model is still asked, the prompt states none were retrieved, and the
	// citation guard pins the result to a capped knowledge basis - a fabricated
	// citation cannot survive an empty passage set, so a no-passage verdict can
	// never masquerade as evidence-grounded or high-confidence.
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
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"verdict":    VerdictCredible,
			"basis":      BasisEvidence,
			"confidence": 0.95,
			"citations":  []any{map[string]any{"evidence_id": "made-up", "quoted_span": "fabricated"}},
			"rationale":  "broadly true",
		}))
	})

	got, err := c.Verify(context.Background(), "a claim", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(captured.Messages) == 0 || len(captured.Messages[0].Content) == 0 {
		t.Fatal("expected a user message with content")
	}
	user := captured.Messages[0].Content[0].Text
	if !strings.Contains(user, "No evidence passages were retrieved") {
		t.Errorf("user message must state no passages were retrieved; got %q", user)
	}
	if got.Verdict != VerdictCredible {
		t.Errorf("verdict = %q, want %q", got.Verdict, VerdictCredible)
	}
	if got.Basis != BasisKnowledge {
		t.Errorf("basis = %q, want demoted %q", got.Basis, BasisKnowledge)
	}
	if got.Confidence != knowledgeConfidenceCap {
		t.Errorf("confidence = %v, want capped %v", got.Confidence, knowledgeConfidenceCap)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %v, want the fabricated citation dropped", got.Citations)
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

// TestVerifyDemotesFabricatedCitationOverWire confirms the citation guard runs on
// the model-judgment path, not just in isolation: a faked evidence-basis verdict
// that cites a passage never supplied comes back demoted to basis knowledge with a
// capped confidence - the credibility judgment stands, but it loses its claimed
// evidence grounding rather than being forced to unverifiable.
func TestVerifyDemotesFabricatedCitationOverWire(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolUseResponse(t, map[string]any{
			"verdict":    VerdictCredible,
			"basis":      BasisEvidence,
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
	if got.Verdict != VerdictCredible {
		t.Errorf("verdict = %q, want %q (state is not forced when evidence is lost)", got.Verdict, VerdictCredible)
	}
	if got.Basis != BasisKnowledge {
		t.Errorf("basis = %q, want %q after dropping the fabricated citation", got.Basis, BasisKnowledge)
	}
	if got.Confidence != knowledgeConfidenceCap {
		t.Errorf("confidence = %v, want it capped at %v", got.Confidence, knowledgeConfidenceCap)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %v, want none", got.Citations)
	}
}

// TestValidateCitations exercises the deterministic guard directly, without the
// model: fabricated ids and non-substring spans are dropped; an evidence-basis
// verdict left ungrounded is demoted to knowledge and confidence-capped (not
// forced to unverifiable); a knowledge-basis verdict needs no citation; and an
// unverifiable verdict is stripped of citations and never upgraded.
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
			name: "valid evidence citation kept with model confidence",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.9,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "ok",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.9,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "ok",
			},
		},
		{
			name: "fabricated evidence_id dropped and basis demoted to knowledge",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.95,
				Citations:  []Citation{{EvidenceID: "nope", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "hallucinated id",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "hallucinated id",
			},
		},
		{
			name: "non-substring span dropped and disputed demoted to knowledge",
			in: Result{
				Verdict:    VerdictDisputed,
				Basis:      BasisEvidence,
				Confidence: 0.8,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "water freezes at 100"}},
				Rationale:  "span not present",
			},
			want: Result{
				Verdict:    VerdictDisputed,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "span not present",
			},
		},
		{
			name: "evidence kept when at least one citation survives",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.7,
				Citations: []Citation{
					{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"},
					{EvidenceID: "ghost", QuotedSpan: "Earth orbits the Sun"},
				},
				Rationale: "one real, one fake",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.7,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}},
				Rationale:  "one real, one fake",
			},
		},
		{
			name: "knowledge verdict needs no citation and is confidence-capped",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: 0.95,
				Citations:  nil,
				Rationale:  "broadly true",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "broadly true",
			},
		},
		{
			name: "knowledge verdict keeps a surviving stray citation and drops a fabricated one",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: 0.4,
				Citations: []Citation{
					{EvidenceID: "e2", QuotedSpan: "boils at 100 degrees"},
					{EvidenceID: "ghost", QuotedSpan: "anything"},
				},
				Rationale: "knowledge with a stray real citation",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: 0.4,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "boils at 100 degrees"}},
				Rationale:  "knowledge with a stray real citation",
			},
		},
		{
			name: "unverifiable cleared of citations, set knowledge, capped, never upgraded",
			in: Result{
				Verdict:    VerdictUnverifiable,
				Basis:      BasisEvidence,
				Confidence: 0.95,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "boils at 100 degrees"}},
				Rationale:  "nothing settles it",
			},
			want: Result{
				Verdict:    VerdictUnverifiable,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  nil,
				Rationale:  "nothing settles it",
			},
		},
		{
			name: "empty quoted_span dropped and credible demoted",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.91,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: ""}},
				Rationale:  "blank span must not ground a verdict",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "blank span must not ground a verdict",
			},
		},
		{
			name: "whitespace-only quoted_span dropped and disputed demoted",
			in: Result{
				Verdict:    VerdictDisputed,
				Basis:      BasisEvidence,
				Confidence: 0.77,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "   \t\n"}},
				Rationale:  "whitespace span grounds nothing",
			},
			want: Result{
				Verdict:    VerdictDisputed,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "whitespace span grounds nothing",
			},
		},
		{
			name: "duplicate evidence_id keeps a span valid against an earlier passage",
			in: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
				Confidence: 0.85,
				Citations:  []Citation{{EvidenceID: "dup", QuotedSpan: "first body"}},
				Rationale:  "span matches the first of two same-id passages",
			},
			want: Result{
				Verdict:    VerdictCredible,
				Basis:      BasisEvidence,
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

// TestValidateCitationsConfidence asserts the documented confidence rules on each
// basis: an evidence-grounded verdict keeps the model's value clamped to [0,1],
// while any non-evidence verdict (knowledge or unverifiable) is additionally
// bounded at the knowledge cap so a tiebreaker can never read high-confidence.
func TestValidateCitationsConfidence(t *testing.T) {
	t.Parallel()
	passages := []Passage{{ID: "e1", Text: "The Earth orbits the Sun."}}

	tests := []struct {
		name string
		in   Result
		want float64
	}{
		{
			name: "evidence above one clamped to one",
			in:   Result{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 1.5, Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}}},
			want: 1,
		},
		{
			name: "evidence below zero clamped to zero",
			in:   Result{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: -0.2, Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}}},
			want: 0,
		},
		{
			name: "evidence nan becomes zero",
			in:   Result{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: math.NaN(), Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}}},
			want: 0,
		},
		{
			name: "evidence in range untouched",
			in:   Result{Verdict: VerdictCredible, Basis: BasisEvidence, Confidence: 0.42, Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "Earth orbits the Sun"}}},
			want: 0.42,
		},
		{
			name: "knowledge above cap bounded to cap",
			in:   Result{Verdict: VerdictCredible, Basis: BasisKnowledge, Confidence: 0.95},
			want: knowledgeConfidenceCap,
		},
		{
			name: "knowledge below cap untouched",
			in:   Result{Verdict: VerdictDisputed, Basis: BasisKnowledge, Confidence: 0.3},
			want: 0.3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ValidateCitations(tc.in, passages)
			if got.Confidence != tc.want {
				t.Errorf("confidence = %v, want %v", got.Confidence, tc.want)
			}
		})
	}
}

// newReasonTestClient points a DeepSeek-provider Client at a fake OpenAI-compatible
// server, so the thinking-enabled Reverify path is exercised against the real
// provider wire format without hitting the network.
func newReasonTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{Provider: llm.ProviderDeepSeek, DeepSeekAPIKey: "test-key", Model: "deepseek-v4-pro"}, llm.WithBaseURL(server.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// deepseekReasonVerdict builds a DeepSeek thinking-mode chat completion carrying a
// reasoning_content chain-of-thought alongside the record_verdict tool call - the
// real wire shape the reasoning second pass parses.
func deepseekReasonVerdict(t *testing.T, input map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(input)
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
					"reasoning_content": "Re-reading the passages, the contradiction is subtle but the evidence does settle it.",
					"tool_calls": []map[string]any{
						{
							"id":       "call_test",
							"type":     "function",
							"function": map[string]any{"name": toolName, "arguments": string(raw)},
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

// TestReverifyParsesReasoningVerdict asserts the thinking-enabled second pass
// parses a verdict from the real DeepSeek reasoning wire format (reasoning_content
// plus a tool call) and runs the same citation guard, surviving a valid citation
// into an evidence verdict.
func TestReverifyParsesReasoningVerdict(t *testing.T) {
	t.Parallel()
	passage := Passage{ID: "wiki:v:0", Text: "Large studies show the vaccine does not cause autism."}
	c := newReasonTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekReasonVerdict(t, map[string]any{
			"verdict":    VerdictDisputed,
			"basis":      BasisEvidence,
			"confidence": 0.95,
			"citations": []map[string]any{
				{"evidence_id": "wiki:v:0", "quoted_span": "the vaccine does not cause autism"},
			},
			"rationale": "The passage directly contradicts the claim.",
		}))
	})

	got, err := c.Reverify(context.Background(), "the vaccine causes autism", []Passage{passage})
	if err != nil {
		t.Fatalf("Reverify: %v", err)
	}
	assertResultEqual(t, got, Result{
		Verdict:    VerdictDisputed,
		Basis:      BasisEvidence,
		Confidence: 0.95,
		Citations:  []Citation{{EvidenceID: "wiki:v:0", QuotedSpan: "the vaccine does not cause autism"}},
		Rationale:  "The passage directly contradicts the claim.",
	})
}

// TestReverifyEnforcesKnowledgeCap asserts the cap invariant holds on the reasoning
// path: a reasoning model that claims an evidence basis at high confidence but cites
// nothing surviving the guard is demoted to a capped knowledge basis, exactly as on
// the fast Verify path - a deeper model can never prop up an ungrounded verdict.
func TestReverifyEnforcesKnowledgeCap(t *testing.T) {
	t.Parallel()
	passage := Passage{ID: "wiki:v:0", Text: "Large studies show the vaccine does not cause autism."}
	c := newReasonTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekReasonVerdict(t, map[string]any{
			"verdict":    VerdictCredible,
			"basis":      BasisEvidence,
			"confidence": 0.99,
			"citations": []map[string]any{
				{"evidence_id": "wiki:v:0", "quoted_span": "a span that is not in the passage"},
			},
			"rationale": "Confidently asserted but ungrounded.",
		}))
	})

	got, err := c.Reverify(context.Background(), "the vaccine is safe", []Passage{passage})
	if err != nil {
		t.Fatalf("Reverify: %v", err)
	}
	if got.Basis != BasisKnowledge {
		t.Errorf("basis = %q, want %q (demoted: no surviving citation)", got.Basis, BasisKnowledge)
	}
	if got.Confidence > knowledgeConfidenceCap {
		t.Errorf("confidence = %v, want <= cap %v", got.Confidence, knowledgeConfidenceCap)
	}
	if len(got.Citations) != 0 {
		t.Errorf("citations = %v, want none survived", got.Citations)
	}
}

// TestReverifyUsesReasoningTokenCap asserts the thinking-enabled path sends the
// larger reasoningMaxTokens (not the fast path's tight maxTokens), so the model's
// deliberation does not exhaust the budget and truncate the tool call.
func TestReverifyUsesReasoningTokenCap(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newReasonTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekReasonVerdict(t, map[string]any{
			"verdict": VerdictUnverifiable, "basis": BasisKnowledge, "confidence": 0.1, "citations": []map[string]any{}, "rationale": "",
		}))
	})

	if _, err := c.Reverify(context.Background(), "a claim", []Passage{{ID: "e1", Text: "some text"}}); err != nil {
		t.Fatalf("Reverify: %v", err)
	}
	if captured["max_tokens"] != float64(reasoningMaxTokens) {
		t.Errorf("max_tokens = %v, want the larger reasoning cap %d", captured["max_tokens"], reasoningMaxTokens)
	}
	if reasoningMaxTokens <= maxTokens {
		t.Errorf("reasoningMaxTokens %d must exceed the fast cap %d", reasoningMaxTokens, maxTokens)
	}
}

// TestReverifyNoPassagesJudgesFromKnowledge asserts the reasoning path, like
// Verify, accepts an empty evidence set under the knowledge fallback: the model
// is called and the guard demotes the result to a capped knowledge basis, so an
// ungrounded reasoning verdict can never render as evidence-grounded.
func TestReverifyNoPassagesJudgesFromKnowledge(t *testing.T) {
	t.Parallel()
	called := false
	c := newReasonTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, deepseekReasonVerdict(t, map[string]any{
			"verdict":    VerdictDisputed,
			"basis":      BasisKnowledge,
			"confidence": 0.9,
			"citations":  []any{},
			"rationale":  "contradicts well-established consensus",
		}))
	})

	got, err := c.Reverify(context.Background(), "a claim", nil)
	if err != nil {
		t.Fatalf("Reverify: %v", err)
	}
	if !called {
		t.Fatal("Reverify must call the model under the knowledge fallback")
	}
	if got.Verdict != VerdictDisputed || got.Basis != BasisKnowledge {
		t.Errorf("got %q/%q, want disputed/knowledge", got.Verdict, got.Basis)
	}
	if got.Confidence != knowledgeConfidenceCap {
		t.Errorf("confidence = %v, want capped %v", got.Confidence, knowledgeConfidenceCap)
	}
}

func assertResultEqual(t *testing.T, got, want Result) {
	t.Helper()
	if got.Verdict != want.Verdict {
		t.Errorf("verdict = %q, want %q", got.Verdict, want.Verdict)
	}
	if got.Basis != want.Basis {
		t.Errorf("basis = %q, want %q", got.Basis, want.Basis)
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
