package verify

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// The two-axis political verifier is the redesign's answer to political speech's
// signature failure: true-but-misleading. A single credibility verdict
// (credible/disputed/unverifiable) cannot say "the figure is exact, but the
// timeframe is cherry-picked". This file adds a second judgment surface alongside
// the credibility verifier (Verify/Result, which the retrieve-then-verify callers
// still use): VerifyPolitical returns two orthogonal axes -
//
//   - literal:  accurate | inaccurate | unverifiable  - is the claim, taken at
//     face value, true against the supplied evidence?
//   - flags:    a subset of {missing-context, cherry-picked, outdated,
//     misattributed, misleading-causation} - is the framing honest, independent
//     of whether the literal claim is true?
//
// plus a confidence, citations into the supplied evidence (evidence_id +
// quoted_span), and a French rationale. It runs one forced tool call at
// temperature zero over the shared internal/llm transport, exactly like the
// credibility verifier and the binary classifiers, so the assessment always
// arrives as validated structured data rather than prose.
//
// The same deterministic citation guard applies: ValidatePoliticalCitations runs
// after the model call, drops citations whose evidence_id was never supplied or
// whose span is not a substring of the cited passage, and - when an evidence-basis
// verdict loses its last valid citation - demotes the basis to knowledge (capping
// confidence) rather than discarding the judgment. The literal verdict and the
// flags are the model's call to make; the guard only governs grounding. It also
// drops any flag the model returns that is not in the closed vocabulary and
// deduplicates the rest, so a hallucinated or repeated flag can never reach the UI.
//
// This judgment surface is consumed by the orchestration capstone, the frontend,
// and the golden eval; it stays inert until those wire it behind FACTCHECK_POLITICAL.

// Literal verdict labels. The literal axis answers only "is the claim true as
// stated", independent of framing: accurate if the supplied evidence affirms it,
// inaccurate if the evidence contradicts it, and unverifiable when nothing settles
// it - a subjective, private, or otherwise indeterminate claim.
const (
	LiteralAccurate     = "accurate"
	LiteralInaccurate   = "inaccurate"
	LiteralUnverifiable = "unverifiable"
)

// Manipulation flags. The flags axis is orthogonal to the literal verdict: a claim
// can be literally accurate yet cherry-picked or stripped of context. The
// vocabulary is closed; ValidatePoliticalCitations drops anything outside it so a
// hallucinated flag never renders.
const (
	// FlagMissingContext marks a claim that omits qualifying context that
	// materially changes its meaning.
	FlagMissingContext = "missing-context"
	// FlagCherryPicked marks a true figure quoted over a selectively chosen
	// timeframe or subset that misrepresents the fuller series.
	FlagCherryPicked = "cherry-picked"
	// FlagOutdated marks a claim that was true at some point but no longer
	// reflects the current state.
	FlagOutdated = "outdated"
	// FlagMisattributed marks a quote or position attributed to the wrong source
	// or context.
	FlagMisattributed = "misattributed"
	// FlagMisleadingCausation marks a claim that asserts or implies a causal link
	// the evidence does not support.
	FlagMisleadingCausation = "misleading-causation"
)

// politicalToolName is the single tool the model is forced to call for the
// two-axis assessment, so the verdict always arrives as validated structured input
// rather than prose. It is distinct from the credibility verifier's tool so the two
// surfaces can coexist without sharing a schema.
const politicalToolName = "record_assessment"

// politicalSystemPrompt is the French two-axis verifier frame. It is deterministic
// and minimal. It instructs the model to (1) judge the literal verdict against the
// supplied evidence first, primary sources outranking corroborating fact-check
// archives; (2) flag dishonest framing on the orthogonal flags axis even when the
// literal claim is accurate; (3) never infer the speaker's intent - it judges the
// claim and its framing, not motive, so no "le locuteur ment". The citation
// requirement for evidence verdicts is stated and then enforced deterministically.
const politicalSystemPrompt = "Tu évalues une affirmation factuelle isolée sur DEUX axes distincts, pour un fact-checking de débats politiques français. " +
	"Chaque passage de preuve porte un evidence_id. " +
	"AXE 1 — verdict littéral : l'affirmation est-elle exacte telle qu'énoncée ? " +
	"Juge d'abord contre les preuves fournies ; une source primaire (institution, donnée officielle, texte) prime sur une archive de fact-check qui ne fait que corroborer. " +
	"Réponds \"accurate\" si une preuve l'affirme, \"inaccurate\" si une preuve la contredit, " +
	"\"unverifiable\" si rien ne tranche — affirmation subjective, privée, anecdotique, ou indéterminée. " +
	"Un verdict appuyé sur une preuve a la base \"evidence\" et DOIT citer au moins un passage par son evidence_id, en reproduisant la portion exacte utilisée. " +
	"Si aucun passage ne porte sur l'affirmation, replie-toi sur tes connaissances générales avec la base \"knowledge\" et une confiance modérée. " +
	"AXE 2 — drapeaux de manipulation : indépendamment du verdict littéral, le cadrage est-il honnête ? " +
	"Choisis le sous-ensemble pertinent parmi : missing-context (contexte qualifiant omis), cherry-picked (période ou sous-ensemble choisi pour tromper), outdated (donnée périmée), misattributed (citation ou position mal attribuée), misleading-causation (lien de cause à effet non étayé). " +
	"Une affirmation peut être \"accurate\" ET porter un drapeau (par ex. un chiffre exact sur une période cherry-pickée). N'invente aucun drapeau hors de cette liste. " +
	"Ne juge JAMAIS l'intention du locuteur (n'écris jamais qu'il ment ou cherche à tromper) : juge uniquement l'affirmation et son cadrage. " +
	"Ne traite pas un passage comme pertinent au seul motif qu'il partage le sujet. " +
	"Rédige le rationale en français, en une phrase. " +
	"Enregistre ton évaluation avec l'outil record_assessment."

// PoliticalResult is the verifier's two-axis assessment of one claim. Literal is
// the face-value verdict (accurate/inaccurate/unverifiable); Flags is the subset of
// the closed manipulation vocabulary that applies to the claim's framing, and is
// orthogonal to Literal. Basis is evidence (grounded in a surviving citation) or
// knowledge (world-knowledge tiebreaker, capped confidence). After the citation
// guard runs, Citations holds only validated groundings, Flags holds only known and
// deduplicated flags, an evidence verdict that lost its last citation has been
// demoted to knowledge, and an unverifiable verdict carries no citations.
type PoliticalResult struct {
	Literal    string     `json:"literal"`
	Basis      string     `json:"basis"`
	Flags      []string   `json:"flags"`
	Confidence float64    `json:"confidence"`
	Citations  []Citation `json:"citations"`
	Rationale  string     `json:"rationale"`
}

// VerifyPolitical judges claim against passages on both axes and returns the
// two-axis assessment. It forces a single record_assessment tool call so the result
// is always validated structured data, then runs the deterministic guard
// (ValidatePoliticalCitations) so a fabricated grounding or an unknown flag can
// never reach the caller. It errors when no passages are supplied (this is the
// evidence verifier; the no-evidence case is the caller's concern) or when the
// transport, the forced tool call, or decoding fails - the caller absorbs the error
// rather than emitting an ungrounded verdict.
func (c *Client) VerifyPolitical(ctx context.Context, claim string, passages []Passage) (PoliticalResult, error) {
	if len(passages) == 0 {
		return PoliticalResult{}, fmt.Errorf("verify: no evidence passages supplied")
	}

	res, err := llm.Classify[PoliticalResult](ctx, c.llm, llm.Request{
		System:    politicalSystemPrompt,
		User:      buildPrompt(claim, passages),
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        politicalToolName,
			Description: "Record the two-axis assessment (literal verdict plus manipulation flags) for the claim against the supplied evidence.",
			Properties: map[string]any{
				"literal": map[string]any{
					"type":        "string",
					"enum":        []string{LiteralAccurate, LiteralInaccurate, LiteralUnverifiable},
					"description": "accurate if the claim is true as stated, inaccurate if a passage contradicts it, unverifiable if nothing can settle it",
				},
				"basis": map[string]any{
					"type":        "string",
					"enum":        []string{BasisEvidence, BasisKnowledge},
					"description": "evidence if a supplied passage settles it (cite it), knowledge if the verdict rests on general world knowledge",
				},
				"flags": map[string]any{
					"type":        "array",
					"description": "the subset of manipulation flags that apply to the framing, orthogonal to the literal verdict; empty if the framing is honest",
					"items": map[string]any{
						"type": "string",
						"enum": []string{FlagMissingContext, FlagCherryPicked, FlagOutdated, FlagMisattributed, FlagMisleadingCausation},
					},
				},
				"confidence": map[string]any{
					"type":        "number",
					"description": "calibrated confidence in the literal verdict, from 0.0 to 1.0; keep it modest for a knowledge-basis verdict",
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
					"description": "one short sentence in French explaining the verdict and any flag, without inferring the speaker's intent",
				},
			},
			Required: []string{"literal", "basis", "flags", "confidence", "citations", "rationale"},
		},
	})
	if err != nil {
		return PoliticalResult{}, fmt.Errorf("verify: %w", err)
	}

	return ValidatePoliticalCitations(res, passages), nil
}

// knownPoliticalFlags is the closed manipulation-flag vocabulary the guard accepts.
var knownPoliticalFlags = map[string]struct{}{
	FlagMissingContext:      {},
	FlagCherryPicked:        {},
	FlagOutdated:            {},
	FlagMisattributed:       {},
	FlagMisleadingCausation: {},
}

// ValidatePoliticalCitations is the deterministic guard for the two-axis result,
// the second half of the defense against hallucinated output. It runs after the
// model call and is directly unit-testable without the model. It shares the
// credibility guard's citation validation (validCitations) and grounding epistemics
// (groundVerdict) so the anti-hallucination rules cannot diverge between the two
// verifiers, and adds one rule of its own: it drops any flag outside the closed
// vocabulary and deduplicates the rest (sanitizeFlags), preserving the model's
// order, so a hallucinated or repeated flag never reaches the UI. Flags survive on
// every verdict, including unverifiable - the framing can be dishonest regardless of
// whether the literal claim can be settled.
func ValidatePoliticalCitations(res PoliticalResult, passages []Passage) PoliticalResult {
	res.Flags = sanitizeFlags(res.Flags)
	res.Citations = validCitations(res.Citations, passages)
	res.Basis, res.Citations, res.Confidence = groundVerdict(
		res.Literal == LiteralUnverifiable, res.Basis, res.Citations, res.Confidence,
	)
	return res
}

// sanitizeFlags drops flags outside the closed vocabulary and deduplicates the
// rest, preserving the order the model returned them in. A nil or all-invalid input
// yields nil, so an honest-framing verdict carries no flag slice.
func sanitizeFlags(flags []string) []string {
	if len(flags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(flags))
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		if _, ok := knownPoliticalFlags[f]; !ok {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
