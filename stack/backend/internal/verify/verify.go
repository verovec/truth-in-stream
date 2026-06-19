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

	"github.com/verovec/truth-in-stream/backend/internal/domain"
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

// systemPromptFR is the French counterpart of systemPrompt, used in the French/EU
// political fact-checking mode so the viewer-facing rationale comes back in French
// instead of English. It is a faithful translation that keeps the literal tool
// tokens (the credible/disputed/unverifiable verdicts and the evidence/knowledge
// bases) in English, since those round-trip as the tool's enum values, and it adds
// an explicit instruction to write the rationale in French - mirroring the political
// verifier (political.go), the other viewer-facing rationale producer. It is
// selected by domain.LocaleFrench; every other locale keeps the English prompt, so
// English behavior is unchanged when French mode is off.
const systemPromptFR = "Tu juges si une seule affirmation factuelle est crédible, contestée ou invérifiable, afin d'aider un spectateur à décider s'il peut faire confiance au locuteur. " +
	"Chaque passage de preuve porte un evidence_id. Juge d'abord contre les preuves fournies : " +
	"si un passage affirme directement l'affirmation, renvoie \"credible\" avec la base \"evidence\" ; " +
	"si un passage la contredit directement, renvoie \"disputed\" avec la base \"evidence\". " +
	"Un verdict appuyé sur une preuve DOIT citer au moins un passage par son evidence_id, en reproduisant la portion exacte sur laquelle tu t'appuies. " +
	"Si aucun passage fourni ne porte sur l'affirmation, replie-toi sur tes connaissances générales pour trancher, avec la base \"knowledge\" : " +
	"renvoie \"credible\" lorsque l'affirmation est globalement vraie ou conforme à un consensus bien établi, " +
	"\"disputed\" lorsqu'elle est manifestement fausse ou contredit un consensus bien établi, " +
	"et \"unverifiable\" lorsqu'elle est véritablement indéterminée ou qu'il s'agit d'un énoncé privé, anecdotique ou subjectif qu'aucune connaissance générale ne peut trancher. " +
	"Un verdict de base \"knowledge\" doit garder une confiance modérée, car il ne s'appuie pas sur les preuves fournies. " +
	"Ne considère pas qu'un passage porte sur l'affirmation au seul motif qu'il partage le même sujet. " +
	"Rédige le rationale en français, en une phrase. " +
	"Enregistre ton verdict avec l'outil record_verdict."

// promptFor selects the system prompt for the locale: the French prompt for
// domain.LocaleFrench, the English prompt for every other locale (including the
// default), so English behavior is unchanged when French mode is off.
func promptFor(locale domain.Locale) string {
	if locale.IsFrench() {
		return systemPromptFR
	}
	return systemPrompt
}

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

// Config wires a Client. Provider selects the LLM backend (default Anthropic);
// APIKey/GeminiAPIKey/DeepSeekAPIKey are the per-provider secrets and come from the environment
// only; an empty Model lets the selected provider apply its own default model. Locale
// selects the prompt language: the default (English) keeps the English prompt;
// domain.LocaleFrench reasons and writes the rationale in French. The verdict is
// unchanged across locales - only the prompt language differs.
type Config struct {
	Provider       llm.ProviderName
	APIKey         string
	GeminiAPIKey   string
	DeepSeekAPIKey string
	Model          string
	Locale         domain.Locale
}

// Client is the Anthropic-backed credibility verifier.
type Client struct {
	llm    *llm.Client
	system string
}

// New builds a Client, failing when no API key is supplied (the feature is gated
// off upstream when unconfigured, so reaching here without a key is a wiring
// error). Extra request options (e.g. a test base URL) are forwarded to the
// shared transport, so a caller can point the client at a fake server.
func New(cfg Config, opts ...llm.Option) (*Client, error) {
	client, err := llm.NewClient(llm.Config{
		Provider:       cfg.Provider,
		APIKey:         cfg.APIKey,
		GeminiAPIKey:   cfg.GeminiAPIKey,
		DeepSeekAPIKey: cfg.DeepSeekAPIKey,
		Model:          cfg.Model,
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	return &Client{llm: client, system: promptFor(cfg.Locale)}, nil
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
		System:    c.system,
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
// directly unit-testable without the model: it validates the cited spans against
// the supplied passages (validCitations) and then normalizes the verdict's basis
// and confidence rather than its state (groundVerdict), which is the credibility
// reframe's core rule - a verdict that loses its evidence grounding is demoted to a
// capped knowledge basis, never forced to unverifiable.
func ValidateCitations(res Result, passages []Passage) Result {
	res.Citations = validCitations(res.Citations, passages)
	res.Basis, res.Citations, res.Confidence = groundVerdict(
		res.Verdict == VerdictUnverifiable, res.Basis, res.Citations, res.Confidence,
	)
	return res
}

// validCitations is the deterministic substring check both verifiers share. It
// keeps only citations whose evidence_id was actually supplied and whose
// quoted_span is a non-empty substring of a passage carrying that evidence_id,
// dropping the rest. An empty or whitespace-only span is invalid, because
// strings.Contains trivially matches the empty string and a blank span grounds
// nothing. When the same evidence_id is supplied on more than one passage, a span
// that matches any of them is kept. Sharing one implementation keeps the
// anti-hallucination grounding check from diverging between the two judgment
// surfaces.
func validCitations(cits []Citation, passages []Passage) []Citation {
	byID := make(map[string][]string, len(passages))
	for _, p := range passages {
		byID[p.ID] = append(byID[p.ID], p.Text)
	}

	valid := make([]Citation, 0, len(cits))
	for _, c := range cits {
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
	return valid
}

// groundVerdict normalizes a verdict's basis, citations, and confidence after its
// citations have been validated - the grounding epistemics both verifiers share,
// governing the basis and confidence rather than the verdict's state. unverifiable
// is true for a verdict nothing can settle (VerdictUnverifiable / LiteralUnverifiable);
// validCits are the citations that survived validCitations.
//
//   - An unverifiable verdict carries no citations and rests on no evidence: it is
//     forced to knowledge-basis, stripped of citations, and confidence-capped, and
//     never upgraded.
//   - A basis-evidence verdict that kept at least one citation stays evidence with
//     the model's clamped confidence.
//   - A verdict that claimed evidence but lost its last citation, or that already
//     called itself knowledge, is demoted (or kept) at knowledge-basis and
//     confidence-capped; the judgment stands, it just loses its evidence grounding.
//     Stray (already validated) citations on a knowledge verdict are kept.
//
// Confidence is clamped into [0,1] on every path (a NaN becomes 0).
func groundVerdict(unverifiable bool, basis string, validCits []Citation, confidence float64) (string, []Citation, float64) {
	if unverifiable {
		return BasisKnowledge, nil, capKnowledgeConfidence(confidence)
	}
	if basis == BasisEvidence && len(validCits) > 0 {
		return BasisEvidence, validCits, clampConfidence(confidence)
	}
	return BasisKnowledge, validCits, capKnowledgeConfidence(confidence)
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
