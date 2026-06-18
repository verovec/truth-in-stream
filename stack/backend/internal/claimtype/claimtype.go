// Package claimtype is the LLM-backed adapter that tags one atomic French
// political claim with its type, so the verify path can route it to the right
// source (statistics to stats sources, voting claims to voting records,
// attributions to press) and hold non-verifiable claims away from live
// verification. It mirrors the codebase's existing "external API for the hard ML
// step" pattern (the stance, check-worthiness, and claim-decomposition
// adapters): a downstream service depends on a small interface, and this package
// supplies the Anthropic-backed implementation.
//
// Classification runs on the smallest fast model (Haiku) at temperature zero and
// forces a single structured tool call with an enum-constrained value rather than
// parsing free text - a one-word reply is fragile under load, a forced tool call
// guarantees the schema. The shared transport lives in internal/llm; this package
// supplies only the prompt, tool schema, and result type.
//
// The step doubles as a second check-worthiness filter: opinion and promise are
// their own types, distinct from the verifiable ones (see Type.Verifiable). On
// any model error the claim degrades to a safe generic type so the live path
// never stalls on a classification failure.
package claimtype

import (
	"context"
	"fmt"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// Type is the claim-type label that drives source routing.
type Type string

// The claim-type enum. The first five are verifiable against an external
// source; promise and opinion are not, and routing holds them away from live
// verification (see Verifiable).
const (
	// Statistic is a quantitative assertion (a figure, rate, count, or share).
	Statistic Type = "statistic"
	// VotingRecord is a claim about how a named person or group voted.
	VotingRecord Type = "voting-record"
	// Attribution is a claim that a named person said or wrote something.
	Attribution Type = "attribution"
	// Causal is a claim that one thing caused or led to another.
	Causal Type = "causal"
	// Comparative is a claim that ranks or compares two or more things.
	Comparative Type = "comparative"
	// Promise is a pledge of future action; it cannot be verified against
	// present evidence and is routed away from live verification.
	Promise Type = "promise"
	// Opinion is a value judgment or subjective assessment with no factual
	// truth value; it is routed away from live verification.
	Opinion Type = "opinion"
)

// DefaultType is the safe generic type used when the model errors or returns a
// value outside the enum. Causal is a verifiable type, so a misclassified claim
// still reaches the verify path rather than being silently dropped as
// non-verifiable.
const DefaultType = Causal

// known is the set of valid enum values, used to reject anything the model
// returns outside the schema.
var known = map[Type]struct{}{
	Statistic:    {},
	VotingRecord: {},
	Attribution:  {},
	Causal:       {},
	Comparative:  {},
	Promise:      {},
	Opinion:      {},
}

// Verifiable reports whether a claim of this type can be checked against an
// external source. Promise (a future pledge) and Opinion (a value judgment) are
// not verifiable; every other type is. Routing uses this to hold non-verifiable
// claims away from live verification.
func (t Type) Verifiable() bool {
	return t != Promise && t != Opinion
}

// defaultModel is the cheapest, fastest current Claude model, suitable for a
// single-label classification over one short claim. The config layer mirrors
// this default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: the model emits one tool call carrying
// a single enum value, so a tight cap keeps latency and cost down.
const maxTokens = 64

// toolName is the single tool the model is forced to call, so the verdict always
// arrives as validated structured input rather than prose.
const toolName = "record_claim_type"

// systemPrompt frames the task in French: classify one claim into exactly one
// type from the fixed set. It is deterministic and minimal - this is a
// classifier, not a chat - and it spells out each type so the model separates
// promises and opinions from the verifiable types.
const systemPrompt = "Tu classes une affirmation politique française dans exactement un type parmi cette liste fermée :\n" +
	"- statistic : une affirmation chiffrée (un chiffre, un taux, un nombre, une part, un montant).\n" +
	"- voting-record : une affirmation sur la façon dont une personne ou un groupe nommé a voté.\n" +
	"- attribution : une affirmation selon laquelle une personne nommée a dit, écrit ou promis quelque chose.\n" +
	"- causal : une affirmation selon laquelle une chose en a causé ou entraîné une autre.\n" +
	"- comparative : une affirmation qui classe ou compare deux choses ou plus.\n" +
	"- promise : un engagement d'action future, qui ne peut pas être vérifié contre des faits présents.\n" +
	"- opinion : un jugement de valeur ou une appréciation subjective, sans valeur de vérité factuelle.\n" +
	"Choisis le type le plus précis qui correspond à l'affirmation. " +
	"Une promesse ou une opinion ne doit jamais être classée comme un type vérifiable. " +
	"Enregistre ta décision avec l'outil record_claim_type."

// Config wires a Client. Provider selects the LLM backend (default Anthropic);
// APIKey/GeminiAPIKey/DeepSeekAPIKey are the per-provider secrets and come from the environment
// only; an empty Model lets the selected provider apply its own default model.
type Config struct {
	Provider       llm.ProviderName
	APIKey         string
	GeminiAPIKey   string
	DeepSeekAPIKey string
	Model          string
}

// Client is the Anthropic-backed claim-type classifier adapter.
type Client struct {
	llm *llm.Client
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
		return nil, fmt.Errorf("claimtype: %w", err)
	}
	return &Client{llm: client}, nil
}

// verdict is the forced tool's structured input.
type verdict struct {
	ClaimType Type `json:"claim_type"`
}

// Classify tags claim with its type. It forces a single record_claim_type tool
// call whose value is enum-constrained, so the verdict is always validated
// structured data.
//
// A blank claim short-circuits to DefaultType without an LLM call: there is
// nothing to classify and the model could only guess. On any failure (transport,
// missing tool block, malformed input, or a value outside the enum) it degrades
// to DefaultType and returns no error, so the live path never stalls on a
// classification failure.
func (c *Client) Classify(ctx context.Context, claim string) Type {
	if strings.TrimSpace(claim) == "" {
		return DefaultType
	}
	v, err := llm.Classify[verdict](ctx, c.llm, llm.Request{
		System:    systemPrompt,
		User:      "Affirmation : " + claim,
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Enregistre le type de l'affirmation politique.",
			Properties: map[string]any{
				"claim_type": map[string]any{
					"type":        "string",
					"description": "le type de l'affirmation",
					"enum": []string{
						string(Statistic),
						string(VotingRecord),
						string(Attribution),
						string(Causal),
						string(Comparative),
						string(Promise),
						string(Opinion),
					},
				},
			},
			Required: []string{"claim_type"},
		},
	})
	if err != nil {
		return DefaultType
	}
	if _, ok := known[v.ClaimType]; !ok {
		return DefaultType
	}
	return v.ClaimType
}
