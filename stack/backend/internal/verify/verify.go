// Package verify is the evidence verifier at the core of the retrieve-then-verify
// fact-check redesign: one LLM call per claim that judges a claim against a set
// of supplied evidence passages and returns a grounded verdict (supports,
// refutes, or not_enough_info) with cited spans. It mirrors the codebase's
// existing "external API for the hard ML step" pattern (the stance and
// check-worthiness adapters): a single forced tool call at temperature zero over
// the shared internal/llm transport, so the verdict always arrives as validated
// structured data rather than prose.
//
// The package exists to kill the "similarity is not entailment" bug: a passage
// that is topically related but does not bear on the claim must yield
// not_enough_info, never a false support, because the model is instructed to
// judge only from what the supplied evidence actually says. A deterministic
// citation guard runs after the model call (ValidateCitations) and is the second
// half of that defense: it drops citations the model fabricated and downgrades
// any supports/refutes verdict left without grounding to not_enough_info, so
// hallucinated grounding never reaches the UI. This card adds the package and its
// tests only; nothing here is wired into the live path yet.
package verify

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// defaultModel is the cheapest, fastest current Claude model, suitable for a
// grounded entailment judgment over a claim and a handful of short passages. The
// config layer mirrors this default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: the model emits one tool call carrying a
// verdict, a confidence, a few short cited spans, and a one-sentence rationale.
// The cap is looser than the binary classifiers because citations can quote
// several passages, but still tight enough to keep latency and cost down.
const maxTokens = 1024

// toolName is the single tool the model is forced to call, so the verdict always
// arrives as validated structured input rather than prose.
const toolName = "record_verdict"

// Verdict labels. A claim is supported or refuted only when the supplied evidence
// settles it; otherwise the model returns not_enough_info, which is a first-class
// outcome here rather than a pre-emptive skip.
const (
	VerdictSupports      = "supports"
	VerdictRefutes       = "refutes"
	VerdictNotEnoughInfo = "not_enough_info"
)

// systemPrompt frames the judgment as grounded entailment over the supplied
// evidence only. It is deterministic and minimal - this is a verifier, not a
// chat - and it bars outside knowledge so that a same-topic but non-bearing
// passage yields not_enough_info instead of a false support. The citation
// requirement is stated to the model and then enforced deterministically after
// the call.
const systemPrompt = "You judge whether a single factual claim is supported or refuted by a set of supplied evidence passages. " +
	"Each passage is labeled with an evidence_id. Judge ONLY from what these passages actually say - " +
	"do not use any outside knowledge, and do not treat a passage as supporting or refuting merely because it shares a topic with the claim. " +
	"Return \"supports\" only when a passage directly affirms the claim, \"refutes\" only when a passage directly denies it, " +
	"and \"not_enough_info\" whenever the supplied evidence does not settle the claim - including when a passage is on the same topic but does not bear on the claim. " +
	"Every \"supports\" or \"refutes\" verdict MUST cite at least one passage by its evidence_id, quoting the exact span of that passage you relied on. " +
	"Record your verdict with the record_verdict tool."

// Passage is one retrieved evidence passage handed to the verifier. ID is the
// stable evidence_id the citation round-trips against; Text is the passage body
// the model reads and that a cited span must be a substring of.
type Passage struct {
	ID   string
	Text string
}

// Citation is one grounding the model returned: the evidence_id it relied on and
// the exact span it quoted from that passage. After ValidateCitations runs, every
// surviving citation's EvidenceID was actually supplied and QuotedSpan is a
// substring of that passage's text.
type Citation struct {
	EvidenceID string `json:"evidence_id"`
	QuotedSpan string `json:"quoted_span"`
}

// Result is the verifier's grounded judgment for one claim. After the citation
// guard runs, Citations holds only validated groundings and a Supports/Refutes
// Verdict always has at least one of them.
type Result struct {
	Verdict    string     `json:"verdict"`
	Confidence float64    `json:"confidence"`
	Citations  []Citation `json:"citations"`
	Rationale  string     `json:"rationale"`
}

// Config wires a Client. APIKey is required and comes from the environment only;
// Model defaults to defaultModel when empty.
type Config struct {
	APIKey string
	Model  string
}

// Client is the Anthropic-backed evidence verifier.
type Client struct {
	llm *llm.Client
}

// New builds a Client, failing when no API key is supplied (the feature is gated
// off upstream when unconfigured, so reaching here without a key is a wiring
// error). Extra request options (e.g. a test base URL) are forwarded to the
// shared transport, so a caller can point the client at a fake server.
func New(cfg Config, opts ...option.RequestOption) (*Client, error) {
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	client, err := llm.NewClient(cfg.APIKey, model, opts...)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	return &Client{llm: client}, nil
}

// Verify judges claim against passages and returns a grounded verdict. It forces
// a single record_verdict tool call so the result is always validated structured
// data, then runs the deterministic citation guard (ValidateCitations) so a
// fabricated or unsupported grounding can never reach the caller. It errors when
// no passages are supplied (a verdict without evidence is meaningless) or when
// the transport, the forced tool call, or decoding fails - the caller absorbs the
// error rather than emitting an ungrounded verdict.
func (c *Client) Verify(ctx context.Context, claim string, passages []Passage) (Result, error) {
	if len(passages) == 0 {
		return Result{}, fmt.Errorf("verify: no evidence passages supplied")
	}

	res, err := llm.Classify[Result](ctx, c.llm, llm.Request{
		System:    systemPrompt,
		User:      buildPrompt(claim, passages),
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Record the grounded verdict for the claim against the supplied evidence.",
			Properties: map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{VerdictSupports, VerdictRefutes, VerdictNotEnoughInfo},
					"description": "supports if a passage directly affirms the claim, refutes if a passage directly denies it, not_enough_info if the evidence does not settle it",
				},
				"confidence": map[string]any{
					"type":        "number",
					"description": "calibrated confidence in the verdict, from 0.0 to 1.0",
				},
				"citations": map[string]any{
					"type":        "array",
					"description": "evidence relied on; required for supports/refutes, each quoting the exact span of the cited passage",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"evidence_id": map[string]any{
								"type":        "string",
								"description": "the evidence_id of a supplied passage",
							},
							"quoted_span": map[string]any{
								"type":        "string",
								"description": "the exact span quoted verbatim from that passage",
							},
						},
						"required": []string{"evidence_id", "quoted_span"},
					},
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "one short sentence explaining the verdict",
				},
			},
			Required: []string{"verdict", "confidence", "citations", "rationale"},
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("verify: %w", err)
	}

	return ValidateCitations(res, passages), nil
}

// buildPrompt renders the claim and the labeled evidence passages into the user
// message. Each passage is fenced by its evidence_id so the model can cite it and
// the citation guard can round-trip the id.
func buildPrompt(claim string, passages []Passage) string {
	var b strings.Builder
	b.WriteString("Claim: ")
	b.WriteString(claim)
	b.WriteString("\n\nEvidence passages:\n")
	for _, p := range passages {
		b.WriteString("\n[evidence_id: ")
		b.WriteString(p.ID)
		b.WriteString("]\n")
		b.WriteString(p.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// ValidateCitations is the deterministic citation guard, the second half of the
// defense against hallucinated grounding. It runs after the model call and is
// directly unit-testable without the model: it keeps only citations whose
// evidence_id was actually supplied and whose quoted_span is a non-empty
// substring of a passage carrying that evidence_id, dropping the rest. An
// empty or whitespace-only span is treated as invalid, because strings.Contains
// trivially matches the empty string and a blank span grounds nothing. When the
// same evidence_id is supplied on more than one passage, a span that matches any
// of them is kept (last-writer-wins would wrongly drop a span valid against an
// earlier passage). If that leaves a supports or refutes verdict with no valid
// citation, the verdict is downgraded to not_enough_info (with its confidence
// floored and citations cleared), because a supports/refutes claim must be
// grounded in evidence the verifier was actually given. A not_enough_info
// verdict is returned with its surviving citations intact and is never upgraded.
// Confidence is clamped into [0,1] on every return path (a NaN becomes 0), so a
// model value outside that range never reaches the caller.
func ValidateCitations(res Result, passages []Passage) Result {
	byID := make(map[string][]string, len(passages))
	for _, p := range passages {
		byID[p.ID] = append(byID[p.ID], p.Text)
	}

	valid := make([]Citation, 0, len(res.Citations))
	for _, c := range res.Citations {
		if strings.TrimSpace(c.QuotedSpan) == "" {
			continue
		}
		texts, ok := byID[c.EvidenceID]
		if !ok {
			continue
		}
		for _, text := range texts {
			if strings.Contains(text, c.QuotedSpan) {
				valid = append(valid, c)
				break
			}
		}
	}
	res.Citations = valid

	if (res.Verdict == VerdictSupports || res.Verdict == VerdictRefutes) && len(valid) == 0 {
		res.Verdict = VerdictNotEnoughInfo
		res.Confidence = 0
		res.Citations = nil
	}
	res.Confidence = clampConfidence(res.Confidence)
	return res
}

// clampConfidence bounds a model-supplied confidence to the documented [0,1]
// range. A NaN (which compares false against every bound) is treated as 0 so it
// never escapes as a sentinel; values above 1 or below 0 are pulled to the
// nearest bound.
func clampConfidence(c float64) float64 {
	switch {
	case math.IsNaN(c) || c < 0:
		return 0
	case c > 1:
		return 1
	default:
		return c
	}
}
