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

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// politicalToolUseResponse builds a minimal valid Messages response carrying one
// forced record_assessment tool call with the given input, so a test can fake the
// model's two-axis assessment without the network.
func politicalToolUseResponse(t *testing.T, input map[string]any) string {
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
			{"type": "tool_use", "id": "toolu_test", "name": politicalToolName, "input": json.RawMessage(raw)},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return string(body)
}

// newPoliticalTestClient points a Client at a fake Anthropic server so request
// shaping and response parsing run without hitting the real API.
func newPoliticalTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := New(Config{Provider: llm.ProviderAnthropic, APIKey: "test-key"}, llm.WithBaseURL(server.URL), llm.WithMaxRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestVerifyPolitical exercises the two-axis judgment path end to end against a
// faked LLM, covering each adversarial French acceptance case: a statistic
// contradicted by the supplied figure -> inaccurate with a citation; a true figure
// quoted over a cherry-picked timeframe (full series supplied) -> accurate +
// cherry-picked; a real quote stripped of qualifying context -> missing-context;
// and a subjective/private claim -> unverifiable with no citations.
func TestVerifyPolitical(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claim     string
		passages  []Passage
		toolInput map[string]any
		want      PoliticalResult
	}{
		{
			name:  "contradicted statistic is inaccurate with a citation",
			claim: "Le chômage est à 12% aujourd'hui.",
			passages: []Passage{
				{ID: "insee:chomage:0", Text: "Selon l'INSEE, le taux de chômage est de 7,3% au dernier trimestre."},
			},
			toolInput: map[string]any{
				"literal":    LiteralInaccurate,
				"basis":      BasisEvidence,
				"flags":      []string{},
				"confidence": 0.92,
				"citations": []map[string]any{
					{"evidence_id": "insee:chomage:0", "quoted_span": "le taux de chômage est de 7,3%"},
				},
				"rationale": "Le chiffre cité contredit la donnée de l'INSEE.",
			},
			want: PoliticalResult{
				Literal:    LiteralInaccurate,
				Basis:      BasisEvidence,
				Flags:      nil,
				Confidence: 0.92,
				Citations:  []Citation{{EvidenceID: "insee:chomage:0", QuotedSpan: "le taux de chômage est de 7,3%"}},
				Rationale:  "Le chiffre cité contredit la donnée de l'INSEE.",
			},
		},
		{
			name:  "true figure on cherry-picked timeframe is accurate plus cherry-picked",
			claim: "La délinquance a baissé de 10% l'an dernier.",
			passages: []Passage{
				{ID: "insee:delinquance:0", Text: "La délinquance a baissé de 10% l'an dernier, après une hausse de 35% sur les cinq années précédentes."},
			},
			toolInput: map[string]any{
				"literal":    LiteralAccurate,
				"basis":      BasisEvidence,
				"flags":      []string{FlagCherryPicked},
				"confidence": 0.81,
				"citations": []map[string]any{
					{"evidence_id": "insee:delinquance:0", "quoted_span": "baissé de 10% l'an dernier"},
				},
				"rationale": "Le chiffre est exact mais la période ignore la hausse des cinq années précédentes.",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Flags:      []string{FlagCherryPicked},
				Confidence: 0.81,
				Citations:  []Citation{{EvidenceID: "insee:delinquance:0", QuotedSpan: "baissé de 10% l'an dernier"}},
				Rationale:  "Le chiffre est exact mais la période ignore la hausse des cinq années précédentes.",
			},
		},
		{
			name:  "stripped quote is accurate but missing-context",
			claim: "Le ministre a dit qu'il fallait supprimer les aides.",
			passages: []Passage{
				{ID: "press:ministre:0", Text: "Le ministre a dit qu'il fallait supprimer les aides « uniquement pour les ménages les plus aisés »."},
			},
			toolInput: map[string]any{
				"literal":    LiteralAccurate,
				"basis":      BasisEvidence,
				"flags":      []string{FlagMissingContext},
				"confidence": 0.7,
				"citations": []map[string]any{
					{"evidence_id": "press:ministre:0", "quoted_span": "supprimer les aides"},
				},
				"rationale": "La citation est exacte mais ampute la qualification « pour les ménages les plus aisés ».",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Flags:      []string{FlagMissingContext},
				Confidence: 0.7,
				Citations:  []Citation{{EvidenceID: "press:ministre:0", QuotedSpan: "supprimer les aides"}},
				Rationale:  "La citation est exacte mais ampute la qualification « pour les ménages les plus aisés ».",
			},
		},
		{
			name:  "subjective claim is unverifiable with no citations",
			claim: "C'est la plus belle réforme de la décennie.",
			passages: []Passage{
				{ID: "press:reforme:0", Text: "La réforme des retraites a été adoptée en 2023."},
			},
			toolInput: map[string]any{
				"literal":    LiteralUnverifiable,
				"basis":      BasisKnowledge,
				"flags":      []string{},
				"confidence": 0.2,
				"citations":  []map[string]any{},
				"rationale":  "Jugement de valeur subjectif qu'aucune source ne peut trancher.",
			},
			want: PoliticalResult{
				Literal:    LiteralUnverifiable,
				Basis:      BasisKnowledge,
				Flags:      nil,
				Confidence: 0.2,
				Citations:  []Citation{},
				Rationale:  "Jugement de valeur subjectif qu'aucune source ne peut trancher.",
			},
		},
		{
			name:  "outdated and misleading-causation flags are preserved and deduplicated",
			claim: "La hausse du SMIC a fait baisser le chômage.",
			passages: []Passage{
				{ID: "insee:smic:0", Text: "Le chômage a baissé en 2019; le SMIC a été revalorisé la même année, mais l'INSEE n'établit aucun lien de causalité."},
			},
			toolInput: map[string]any{
				"literal":    LiteralUnverifiable,
				"basis":      BasisKnowledge,
				"flags":      []string{FlagMisleadingCausation, FlagOutdated, FlagMisleadingCausation},
				"confidence": 0.6,
				"citations":  []map[string]any{},
				"rationale":  "Aucune source n'établit la causalité affirmée, et la donnée est ancienne.",
			},
			want: PoliticalResult{
				Literal:    LiteralUnverifiable,
				Basis:      BasisKnowledge,
				Flags:      []string{FlagMisleadingCausation, FlagOutdated},
				Confidence: 0.6,
				Citations:  nil,
				Rationale:  "Aucune source n'établit la causalité affirmée, et la donnée est ancienne.",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newPoliticalTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, politicalToolUseResponse(t, tc.toolInput))
			})

			got, err := c.VerifyPolitical(context.Background(), tc.claim, tc.passages)
			if err != nil {
				t.Fatalf("VerifyPolitical: %v", err)
			}
			assertPoliticalResultEqual(t, got, tc.want)
		})
	}
}

// TestVerifyPoliticalForcesStructuredToolCall asserts the request is shaped as a
// single forced record_assessment call at temperature zero on the configured model.
func TestVerifyPoliticalForcesStructuredToolCall(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	c := newPoliticalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, politicalToolUseResponse(t, map[string]any{
			"literal":    LiteralUnverifiable,
			"basis":      BasisKnowledge,
			"flags":      []string{},
			"confidence": 0.1,
			"citations":  []map[string]any{},
			"rationale":  "",
		}))
	})

	if _, err := c.VerifyPolitical(context.Background(), "une affirmation", []Passage{{ID: "e1", Text: "du texte"}}); err != nil {
		t.Fatalf("VerifyPolitical: %v", err)
	}

	if captured["model"] != defaultModel {
		t.Errorf("model = %v, want %s", captured["model"], defaultModel)
	}
	if captured["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", captured["temperature"])
	}
	choice, ok := captured["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "tool" || choice["name"] != politicalToolName {
		t.Errorf("tool_choice = %v, want forced %s", captured["tool_choice"], politicalToolName)
	}
}

func TestVerifyPoliticalRequiresPassages(t *testing.T) {
	t.Parallel()
	c := newPoliticalTestClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("VerifyPolitical must not call the model when no passages are supplied")
	})
	if _, err := c.VerifyPolitical(context.Background(), "une affirmation", nil); err == nil {
		t.Fatal("expected an error when no evidence passages are supplied")
	}
}

func TestVerifyPoliticalTransportErrorPropagates(t *testing.T) {
	t.Parallel()
	c := newPoliticalTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	})

	if _, err := c.VerifyPolitical(context.Background(), "une affirmation", []Passage{{ID: "e1", Text: "t"}}); err == nil {
		t.Fatal("expected an error when the API returns 500")
	}
}

func TestVerifyPoliticalMissingToolCallErrors(t *testing.T) {
	t.Parallel()
	c := newPoliticalTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"pas d'outil"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	})

	_, err := c.VerifyPolitical(context.Background(), "une affirmation", []Passage{{ID: "e1", Text: "t"}})
	if err == nil || !strings.Contains(err.Error(), "no record_assessment") {
		t.Fatalf("expected a missing-tool-call error, got %v", err)
	}
}

// TestVerifyPoliticalDropsFabricatedCitationOverWire confirms the citation guard
// runs on the model-judgment path: a faked evidence-basis verdict that cites a
// passage never supplied comes back demoted to basis knowledge with a capped
// confidence - the literal verdict and flags stand, but it loses its claimed
// evidence grounding rather than being forced to unverifiable.
func TestVerifyPoliticalDropsFabricatedCitationOverWire(t *testing.T) {
	t.Parallel()
	c := newPoliticalTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, politicalToolUseResponse(t, map[string]any{
			"literal":    LiteralInaccurate,
			"basis":      BasisEvidence,
			"flags":      []string{FlagCherryPicked},
			"confidence": 0.9,
			"citations": []map[string]any{
				{"evidence_id": "ghost:99", "quoted_span": "n'apparaît dans aucun passage"},
			},
			"rationale": "ancrage fabriqué",
		}))
	})

	got, err := c.VerifyPolitical(context.Background(), "une affirmation", []Passage{{ID: "e1", Text: "vrai texte de preuve"}})
	if err != nil {
		t.Fatalf("VerifyPolitical: %v", err)
	}
	if got.Literal != LiteralInaccurate {
		t.Errorf("literal = %q, want %q (state is not forced when evidence is lost)", got.Literal, LiteralInaccurate)
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
	if len(got.Flags) != 1 || got.Flags[0] != FlagCherryPicked {
		t.Errorf("flags = %v, want the cherry-picked flag preserved", got.Flags)
	}
}

// TestValidatePoliticalCitations exercises the deterministic two-axis guard
// directly, without the model.
func TestValidatePoliticalCitations(t *testing.T) {
	t.Parallel()
	passages := []Passage{
		{ID: "e1", Text: "La Terre tourne autour du Soleil."},
		{ID: "e2", Text: "L'eau bout à 100 degrés Celsius au niveau de la mer."},
		{ID: "dup", Text: "premier corps"},
		{ID: "dup", Text: "second corps"},
	}

	tests := []struct {
		name string
		in   PoliticalResult
		want PoliticalResult
	}{
		{
			name: "valid evidence citation kept with model confidence and flags",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Flags:      []string{FlagMissingContext},
				Confidence: 0.9,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}},
				Rationale:  "ok",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Flags:      []string{FlagMissingContext},
				Confidence: 0.9,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}},
				Rationale:  "ok",
			},
		},
		{
			name: "fabricated evidence_id dropped and basis demoted to knowledge",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Flags:      []string{FlagCherryPicked},
				Confidence: 0.95,
				Citations:  []Citation{{EvidenceID: "nope", QuotedSpan: "tourne autour du Soleil"}},
				Rationale:  "id halluciné",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisKnowledge,
				Flags:      []string{FlagCherryPicked},
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "id halluciné",
			},
		},
		{
			name: "non-substring span dropped and inaccurate demoted to knowledge",
			in: PoliticalResult{
				Literal:    LiteralInaccurate,
				Basis:      BasisEvidence,
				Confidence: 0.8,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "l'eau gèle à 100"}},
				Rationale:  "span absent",
			},
			want: PoliticalResult{
				Literal:    LiteralInaccurate,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "span absent",
			},
		},
		{
			name: "evidence kept when at least one citation survives",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Confidence: 0.7,
				Citations: []Citation{
					{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"},
					{EvidenceID: "ghost", QuotedSpan: "tourne autour du Soleil"},
				},
				Rationale: "une vraie, une fausse",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Confidence: 0.7,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}},
				Rationale:  "une vraie, une fausse",
			},
		},
		{
			name: "knowledge verdict needs no citation and is confidence-capped",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisKnowledge,
				Confidence: 0.95,
				Citations:  nil,
				Rationale:  "globalement vrai",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "globalement vrai",
			},
		},
		{
			name: "unverifiable cleared of citations, set knowledge, capped, never upgraded",
			in: PoliticalResult{
				Literal:    LiteralUnverifiable,
				Basis:      BasisEvidence,
				Confidence: 0.95,
				Citations:  []Citation{{EvidenceID: "e2", QuotedSpan: "bout à 100 degrés"}},
				Rationale:  "rien ne tranche",
			},
			want: PoliticalResult{
				Literal:    LiteralUnverifiable,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  nil,
				Rationale:  "rien ne tranche",
			},
		},
		{
			name: "empty quoted_span dropped and accurate demoted",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Confidence: 0.91,
				Citations:  []Citation{{EvidenceID: "e1", QuotedSpan: ""}},
				Rationale:  "span vide ne fonde rien",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisKnowledge,
				Confidence: knowledgeConfidenceCap,
				Citations:  []Citation{},
				Rationale:  "span vide ne fonde rien",
			},
		},
		{
			name: "duplicate evidence_id keeps a span valid against an earlier passage",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Confidence: 0.85,
				Citations:  []Citation{{EvidenceID: "dup", QuotedSpan: "premier corps"}},
				Rationale:  "span correspond au premier des deux passages homonymes",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisEvidence,
				Confidence: 0.85,
				Citations:  []Citation{{EvidenceID: "dup", QuotedSpan: "premier corps"}},
				Rationale:  "span correspond au premier des deux passages homonymes",
			},
		},
		{
			name: "unknown flags dropped, known flags deduplicated and order-stable",
			in: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisKnowledge,
				Flags:      []string{FlagOutdated, "le-locuteur-ment", FlagCherryPicked, FlagOutdated, ""},
				Confidence: 0.3,
				Citations:  nil,
				Rationale:  "drapeaux à nettoyer",
			},
			want: PoliticalResult{
				Literal:    LiteralAccurate,
				Basis:      BasisKnowledge,
				Flags:      []string{FlagOutdated, FlagCherryPicked},
				Confidence: 0.3,
				Citations:  []Citation{},
				Rationale:  "drapeaux à nettoyer",
			},
		},
		{
			name: "unverifiable keeps its flags",
			in: PoliticalResult{
				Literal:    LiteralUnverifiable,
				Basis:      BasisKnowledge,
				Flags:      []string{FlagMisleadingCausation},
				Confidence: 0.5,
				Citations:  nil,
				Rationale:  "non vérifiable mais causalité trompeuse",
			},
			want: PoliticalResult{
				Literal:    LiteralUnverifiable,
				Basis:      BasisKnowledge,
				Flags:      []string{FlagMisleadingCausation},
				Confidence: 0.5,
				Citations:  nil,
				Rationale:  "non vérifiable mais causalité trompeuse",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ValidatePoliticalCitations(tc.in, passages)
			assertPoliticalResultEqual(t, got, tc.want)
		})
	}
}

// TestValidatePoliticalCitationsConfidence asserts the confidence rules: an
// evidence-grounded verdict keeps the model's value clamped to [0,1]; any
// non-evidence verdict (knowledge or unverifiable) is additionally bounded at the
// knowledge cap.
func TestValidatePoliticalCitationsConfidence(t *testing.T) {
	t.Parallel()
	passages := []Passage{{ID: "e1", Text: "La Terre tourne autour du Soleil."}}

	tests := []struct {
		name string
		in   PoliticalResult
		want float64
	}{
		{
			name: "evidence above one clamped to one",
			in:   PoliticalResult{Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: 1.5, Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}}},
			want: 1,
		},
		{
			name: "evidence below zero clamped to zero",
			in:   PoliticalResult{Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: -0.2, Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}}},
			want: 0,
		},
		{
			name: "evidence nan becomes zero",
			in:   PoliticalResult{Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: math.NaN(), Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}}},
			want: 0,
		},
		{
			name: "evidence in range untouched",
			in:   PoliticalResult{Literal: LiteralAccurate, Basis: BasisEvidence, Confidence: 0.42, Citations: []Citation{{EvidenceID: "e1", QuotedSpan: "tourne autour du Soleil"}}},
			want: 0.42,
		},
		{
			name: "knowledge above cap bounded to cap",
			in:   PoliticalResult{Literal: LiteralAccurate, Basis: BasisKnowledge, Confidence: 0.95},
			want: knowledgeConfidenceCap,
		},
		{
			name: "knowledge below cap untouched",
			in:   PoliticalResult{Literal: LiteralInaccurate, Basis: BasisKnowledge, Confidence: 0.3},
			want: 0.3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ValidatePoliticalCitations(tc.in, passages)
			if got.Confidence != tc.want {
				t.Errorf("confidence = %v, want %v", got.Confidence, tc.want)
			}
		})
	}
}

func assertPoliticalResultEqual(t *testing.T, got, want PoliticalResult) {
	t.Helper()
	if got.Literal != want.Literal {
		t.Errorf("literal = %q, want %q", got.Literal, want.Literal)
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
	assertStringSliceEqual(t, "flags", got.Flags, want.Flags)
	if len(got.Citations) != len(want.Citations) {
		t.Fatalf("citations = %v, want %v", got.Citations, want.Citations)
	}
	for i := range want.Citations {
		if got.Citations[i] != want.Citations[i] {
			t.Errorf("citation[%d] = %v, want %v", i, got.Citations[i], want.Citations[i])
		}
	}
}

func assertStringSliceEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %q, want %q", label, i, got[i], want[i])
		}
	}
}
