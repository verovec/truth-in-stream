// Package claimdecomp is the LLM-backed adapter that splits one checkable speech
// unit into atomic, self-contained claims. It mirrors the codebase's existing
// "external API for the hard ML step" pattern (the stance and check-worthiness
// adapters): a downstream service depends on a small interface, and this package
// supplies the Anthropic-backed implementation.
//
// Decomposition runs on the smallest fast model (Haiku) at temperature zero and
// forces a single structured tool call rather than parsing free text - a list of
// claims is fragile under prose parsing, a forced tool call guarantees the
// schema. The shared transport lives in internal/llm; this package supplies only
// the prompt, tool schema, and result type.
//
// The step also doubles as a finer check-worthiness filter: non-factual
// fragments (opinions, hedges) are dropped, so a unit may decompose to an empty
// list (a valid result the caller turns into a single skip). On any model error
// the unit is returned verbatim as a single claim so the live path never stalls.
package claimdecomp

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// defaultModel is the cheapest, fastest current Claude model, suitable for
// splitting one short unit into atomic claims. The config layer mirrors this
// default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// defaultMaxClaims caps the fan-out of a single unit. Most checkable utterances
// carry one to a few assertions; past this cap the model is told to keep the
// most check-worthy, so a rambling unit cannot flood the verify path.
const defaultMaxClaims = 4

// maxTokens bounds the structured reply: the model emits one tool call carrying
// a short list of one-sentence claims, so a modest cap keeps latency and cost
// down without truncating a realistic decomposition.
const maxTokens = 512

// toolName is the single tool the model is forced to call, so the claims always
// arrive as validated structured input rather than prose.
const toolName = "record_claims"

// systemPrompt frames the task narrowly: resolve coreference against the
// supplied context, drop non-factual fragments, and emit each surviving claim as
// a standalone declarative sentence. It is deterministic and minimal - this is a
// decomposer, not a chat.
const systemPrompt = "You split one spoken statement into its atomic factual claims. " +
	"Each claim must be a single self-contained declarative sentence that survives on its own " +
	"outside the conversation: resolve every pronoun and contextual reference using the speaker " +
	"and recent context, naming the real subject explicitly. " +
	"Emit one claim per distinct verifiable assertion. " +
	"Drop anything that is not a verifiable public factual assertion - opinions, hedges, questions, " +
	"greetings, small talk, and sentence fragments are omitted entirely. " +
	"If the statement contains no verifiable factual claim, return an empty list. " +
	"Never invent claims the statement does not make. " +
	"When the statement carries more claims than requested, keep the most check-worthy and drop the rest. " +
	"Record the claims with the record_claims tool."

// systemPromptFR is the French counterpart of systemPrompt, used in the French/EU
// political fact-checking mode. The model reads French statements, resolves
// coreference, and emits French atomic claims; it is selected by
// domain.LocaleFrench. Every other locale keeps the English prompt, so English
// behavior is unchanged when French mode is off. It also instructs the model to
// emit claims in French, so the downstream verifier reasons in one language.
const systemPromptFR = "Tu decomposes un seul enonce parle en ses affirmations factuelles atomiques. " +
	"Chaque affirmation doit etre une seule phrase declarative autonome qui tient seule " +
	"hors de la conversation : resous chaque pronom et chaque reference contextuelle a l'aide du locuteur " +
	"et du contexte recent, en nommant explicitement le veritable sujet. " +
	"Emets une affirmation pour chaque assertion verifiable. " +
	"Ecris chaque affirmation en francais. " +
	"Ecarte tout ce qui n'est pas une assertion factuelle publique verifiable - les opinions, les formules prudentes, les questions, " +
	"les salutations, les conversations anodines et les fragments de phrase sont entierement omis. " +
	"Si l'enonce ne contient aucune affirmation factuelle verifiable, renvoie une liste vide. " +
	"N'invente jamais d'affirmations que l'enonce ne fait pas. " +
	"Lorsque l'enonce porte plus d'affirmations que demande, garde les plus verifiables et ecarte le reste. " +
	"Enregistre les affirmations avec l'outil record_claims."

// Config wires a Client. APIKey is required and comes from the environment only;
// Model defaults to defaultModel when empty; MaxClaimsPerUnit defaults to
// defaultMaxClaims when not positive. Locale selects the prompt language: the
// default (English) keeps the English prompt and labels; domain.LocaleFrench
// prompts and emits claims in French.
type Config struct {
	APIKey           string
	Model            string
	MaxClaimsPerUnit int
	Locale           domain.Locale
}

// Input is one decomposition request: the unit to split, the speaker who said
// it, and recent prior context from the same conversation used to resolve
// coreference. Speaker and Context may be empty.
type Input struct {
	Text    string
	Speaker string
	Context string
}

// Client is the Anthropic-backed claim-decomposition adapter. locale is the
// single source for both the system prompt and the user-message labels, so the
// two cannot drift onto different languages.
type Client struct {
	llm       *llm.Client
	maxClaims int
	locale    domain.Locale
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
	maxClaims := cfg.MaxClaimsPerUnit
	if maxClaims <= 0 {
		maxClaims = defaultMaxClaims
	}
	client, err := llm.NewClient(cfg.APIKey, model, opts...)
	if err != nil {
		return nil, fmt.Errorf("claimdecomp: %w", err)
	}
	return &Client{llm: client, maxClaims: maxClaims, locale: cfg.Locale}, nil
}

// promptFor selects the system prompt for the locale: the French prompt for
// domain.LocaleFrench, the English prompt for every other locale (including the
// default), so English behavior is unchanged when French mode is off. It mirrors
// the sibling checkworthy adapter's selection shape.
func promptFor(locale domain.Locale) string {
	if locale.IsFrench() {
		return systemPromptFR
	}
	return systemPrompt
}

// result is the forced tool's structured input.
type result struct {
	Claims []string `json:"claims"`
}

// Decompose splits in.Text into atomic, self-contained claims, resolving
// coreference against the speaker and recent context. It forces a single
// record_claims tool call so the result is always validated structured data.
//
// A blank unit short-circuits to an empty list without an LLM call: there is
// nothing to verify and the model could only hallucinate. An empty slice is also
// a valid result for a non-blank unit that carried no verifiable claim. On any
// failure (transport, missing tool block, malformed input) it degrades to a
// single claim holding the unit verbatim and returns no error, so the live path
// never stalls on a decomposition failure. The returned slice is trimmed of
// blanks and capped at the configured MaxClaimsPerUnit.
func (c *Client) Decompose(ctx context.Context, in Input) []string {
	if strings.TrimSpace(in.Text) == "" {
		return []string{}
	}
	res, err := llm.Classify[result](ctx, c.llm, llm.Request{
		System:    promptFor(c.locale),
		User:      c.userMessage(in),
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Record the atomic, self-contained factual claims the statement makes.",
			Properties: map[string]any{
				"claims": map[string]any{
					"type":        "array",
					"description": "the atomic claims, each a standalone declarative sentence with coreference resolved; empty when the statement makes no verifiable factual claim",
					"items": map[string]any{
						"type": "string",
					},
					"maxItems": c.maxClaims,
				},
			},
			Required: []string{"claims"},
		},
	})
	if err != nil {
		return c.cleanClaims([]string{in.Text})
	}
	return c.cleanClaims(res.Claims)
}

// userMessage assembles the prompt body from the unit, its speaker, and recent
// context, including the cap so the model self-limits the fan-out. The labels
// match the prompt language so a French run reads as one coherent French prompt.
func (c *Client) userMessage(in Input) string {
	if c.locale.IsFrench() {
		return c.userMessageFR(in)
	}
	var b strings.Builder
	if in.Speaker != "" {
		fmt.Fprintf(&b, "Speaker: %s\n", in.Speaker)
	}
	if in.Context != "" {
		fmt.Fprintf(&b, "Recent context: %s\n", in.Context)
	}
	fmt.Fprintf(&b, "Return at most %d claims.\n\n", c.maxClaims)
	fmt.Fprintf(&b, "Statement: %s", in.Text)
	return b.String()
}

// userMessageFR is the French-labeled prompt body, used when the locale is
// French so the speaker, context, cap, and statement are all framed in French.
func (c *Client) userMessageFR(in Input) string {
	var b strings.Builder
	if in.Speaker != "" {
		fmt.Fprintf(&b, "Locuteur : %s\n", in.Speaker)
	}
	if in.Context != "" {
		fmt.Fprintf(&b, "Contexte recent : %s\n", in.Context)
	}
	fmt.Fprintf(&b, "Renvoie au plus %d affirmations.\n\n", c.maxClaims)
	fmt.Fprintf(&b, "Enonce : %s", in.Text)
	return b.String()
}

// cleanClaims trims surrounding whitespace, drops blanks, and enforces the cap
// as a deterministic backstop in case the model ignores the maxItems hint.
func (c *Client) cleanClaims(claims []string) []string {
	cleaned := make([]string, 0, len(claims))
	for _, claim := range claims {
		trimmed := strings.TrimSpace(claim)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, trimmed)
		if len(cleaned) == c.maxClaims {
			break
		}
	}
	return cleaned
}
