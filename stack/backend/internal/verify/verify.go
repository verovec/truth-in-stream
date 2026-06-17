// Package verify is the credibility verifier at the core of the speaker-credibility
// fact-check redesign: one LLM call per claim that judges a claim against a set
// of supplied evidence passages and returns a grounded credibility verdict
// (credible, disputed, or unverifiable) with a grounding basis (evidence-backed
// or knowledge-only) and cited spans. It mirrors the codebase's existing
// "external API for the hard ML step" pattern (the stance and check-worthiness
// adapters): a single forced tool call at temperature zero over the shared
// internal/llm transport, so the verdict always arrives as validated structured
// data rather than prose.
//
// The package answers the product question "can I trust this speaker on what he
// just said?" rather than "does our corpus literally contain this sentence?". It
// is evidence-anchored with world knowledge as a tiebreaker: a passage that
// directly affirms the claim yields credible/evidence, a passage that contradicts
// it yields disputed/evidence, and when no supplied passage bears on the claim the
// model falls back to general knowledge (credible/disputed with basis knowledge,
// or unverifiable when nothing settles it). A deterministic citation guard runs
// after the model call (ValidateCitations): it drops citations the model
// fabricated and, when an evidence-basis verdict loses its last valid citation,
// demotes the basis to knowledge (and caps its confidence) rather than discarding
// the judgment, so hallucinated grounding can never prop up an evidence claim
// while a defensible knowledge-based credibility judgment still stands. This card
// adds the package and its tests; the live wiring lives in internal/service.
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
// grounded credibility judgment over a claim and a handful of short passages. The
// config layer mirrors this default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: the model emits one tool call carrying a
// verdict, a basis, a confidence, a few short cited spans, and a one-sentence
// rationale. The cap is looser than the binary classifiers because citations can
// quote several passages, but still tight enough to keep latency and cost down.
const maxTokens = 1024

// toolName is the single tool the model is forced to call, so the verdict always
// arrives as validated structured input rather than prose.
const toolName = "record_verdict"

// Verdict labels. A claim is credible unless we find a reason to doubt it: a
// passage or well-established consensus that contradicts it makes it disputed,
// and a genuinely indeterminate or private/subjective claim that no evidence and
// no general knowledge can speak to is unverifiable. The default posture is trust;
// we flag contradiction and implausibility, and stay quiet (credible) otherwise.
const (
	VerdictCredible     = "credible"
	VerdictDisputed     = "disputed"
	VerdictUnverifiable = "unverifiable"
)

// Basis records what a verdict rests on. An evidence-basis verdict is grounded in
// a supplied passage and must cite it; a knowledge-basis verdict is the model's
// world-knowledge tiebreaker, used only when no supplied passage bears on the
// claim, and is always lower-confidence and surfaced as "no direct sources".
const (
	BasisEvidence  = "evidence"
	BasisKnowledge = "knowledge"
)

// knowledgeConfidenceCap bounds the confidence of any verdict not grounded in a
// surviving citation. It is the deterministic half of the "world knowledge is a
// tiebreaker, never authoritative" rule: a knowledge-only credibility call can
// never render as high-confidence, no matter what the model returns. The prompt
// also instructs the model to keep knowledge verdicts modest; this constant makes
// it impossible to exceed. It is a tunable constant, not a config knob, because it
// is a property of the verifier's epistemics rather than a deployment choice.
const knowledgeConfidenceCap = 0.6

// systemPrompt frames the judgment as speaker credibility, evidence-anchored with
// world knowledge as a tiebreaker. It is deterministic and minimal - this is a
// verifier, not a chat. The model judges against the supplied evidence first and
// falls back to general knowledge only when no passage bears on the claim, marking
// those verdicts basis knowledge. The citation requirement for evidence verdicts
// is stated to the model and then enforced deterministically after the call.
const systemPrompt = "You judge whether a single factual claim is credible, disputed, or unverifiable, to help a viewer decide whether to trust the speaker. " +
	"Each evidence passage is labeled with an evidence_id. First judge against the supplied evidence: " +
	"if a passage directly affirms the claim, return \"credible\" with basis \"evidence\"; " +
	"if a passage directly contradicts it, return \"disputed\" with basis \"evidence\". " +
	"An evidence verdict MUST cite at least one passage by its evidence_id, quoting the exact span you relied on. " +
	"If no supplied passage bears on the claim, fall back to your general knowledge as a tiebreaker, with basis \"knowledge\": " +
	"return \"credible\" when the claim is broadly true or consistent with well-established consensus, " +
	"\"disputed\" when it is clearly false or contradicts well-established consensus, " +
	"and \"unverifiable\" when the claim is genuinely indeterminate or is a private, anecdotal, or subjective statement that no general knowledge can settle. " +
	"A basis \"knowledge\" verdict must keep its confidence modest, since it is not grounded in the supplied evidence. " +
	"Do not treat a passage as bearing on the claim merely because it shares a topic. " +
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

// Result is the verifier's grounded credibility judgment for one claim. Verdict is
// credible/disputed/unverifiable; Basis is evidence (grounded in a surviving
// citation) or knowledge (world-knowledge tiebreaker, capped confidence). After
// the citation guard runs, Citations holds only validated groundings, an evidence
// verdict that lost its last citation has been demoted to knowledge, and an
// unverifiable verdict carries no citations.
type Result struct {
	Verdict    string     `json:"verdict"`
	Basis      string     `json:"basis"`
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

// Client is the Anthropic-backed credibility verifier.
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

// Verify judges claim against passages and returns a grounded credibility verdict.
// It forces a single record_verdict tool call so the result is always validated
// structured data, then runs the deterministic citation guard (ValidateCitations)
// so a fabricated or unsupported grounding can never prop up an evidence verdict.
// It errors when no passages are supplied (this is the evidence verifier; the
// knowledge-only no-evidence case is handled by the caller) or when the transport,
// the forced tool call, or decoding fails - the caller absorbs the error rather
// than emitting an ungrounded verdict.
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
			Description: "Record the grounded credibility verdict for the claim against the supplied evidence.",
			Properties: map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{VerdictCredible, VerdictDisputed, VerdictUnverifiable},
					"description": "credible if the claim is affirmed or broadly true, disputed if it is contradicted or clearly false, unverifiable if nothing can settle it",
				},
				"basis": map[string]any{
					"type":        "string",
					"enum":        []string{BasisEvidence, BasisKnowledge},
					"description": "evidence if a supplied passage settles it (cite it), knowledge if the verdict rests on general world knowledge",
				},
				"confidence": map[string]any{
					"type":        "number",
					"description": "calibrated confidence in the verdict, from 0.0 to 1.0; keep it modest for a knowledge-basis verdict",
				},
				"citations": map[string]any{
					"type":        "array",
					"description": "evidence relied on; required for a basis-evidence verdict, each quoting the exact span of the cited passage",
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
			Required: []string{"verdict", "basis", "confidence", "citations", "rationale"},
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
// directly unit-testable without the model. It keeps only citations whose
// evidence_id was actually supplied and whose quoted_span is a non-empty substring
// of a passage carrying that evidence_id, dropping the rest. An empty or
// whitespace-only span is treated as invalid, because strings.Contains trivially
// matches the empty string and a blank span grounds nothing. When the same
// evidence_id is supplied on more than one passage, a span that matches any of
// them is kept.
//
// The guard then normalizes the verdict's basis and confidence rather than its
// state, which is the credibility reframe's core rule:
//   - An unverifiable verdict carries no citations and is treated as knowledge-
//     basis (nothing grounds it); its confidence is capped like any non-evidence
//     verdict and it is never upgraded.
//   - A basis-evidence verdict that lost its last valid citation is demoted to
//     basis knowledge and confidence-capped, NOT forced to unverifiable: the model
//     may still hold a defensible knowledge-based judgment, it just loses its
//     evidence grounding. A basis-evidence verdict that kept a citation stands with
//     the model's clamped confidence.
//   - A basis-knowledge verdict needs no citation; any stray citations are still
//     validated and fabricated ones dropped, and its confidence is capped.
//
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

	switch res.Verdict {
	case VerdictUnverifiable:
		// Nothing settles the claim: it carries no citations and rests on no
		// evidence. Cap and clamp its (cosmetic) confidence like any non-evidence
		// verdict; never upgrade it.
		res.Basis = BasisKnowledge
		res.Citations = nil
		res.Confidence = capKnowledgeConfidence(res.Confidence)
	default:
		// credible or disputed.
		if res.Basis == BasisEvidence && len(valid) > 0 {
			// Genuinely grounded: keep the evidence basis and the model's confidence.
			res.Confidence = clampConfidence(res.Confidence)
			break
		}
		// Either the model already called it knowledge, or it claimed evidence but
		// no citation survived. Demote (or keep) the basis to knowledge and cap the
		// confidence; the judgment stands, it is just no longer evidence-grounded.
		res.Basis = BasisKnowledge
		res.Confidence = capKnowledgeConfidence(res.Confidence)
	}
	return res
}

// capKnowledgeConfidence clamps a confidence into [0,1] and then bounds it at the
// knowledge cap, so a verdict that does not rest on a surviving citation can never
// render as high-confidence.
func capKnowledgeConfidence(c float64) float64 {
	return min(clampConfidence(c), knowledgeConfidenceCap)
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
