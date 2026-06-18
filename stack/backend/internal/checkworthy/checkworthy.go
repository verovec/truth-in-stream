// Package checkworthy is the LLM-backed adapter for the check-worthiness gate's
// model stage: a binary "is this a check-worthy public factual claim?" judgment
// over one short statement. It mirrors the codebase's existing "external API for
// the hard ML step" pattern (Voyage embeddings, AssemblyAI transcription, the
// stance adapter): the service layer depends on the
// service.CheckWorthinessClassifier interface, and this package supplies the
// Anthropic-backed implementation.
//
// The judgment is a classification, not generation, so it runs on the smallest
// fast model (Haiku) at temperature zero and forces a single structured tool
// call rather than parsing free text - a one-word reply is fragile under load, a
// forced tool call guarantees the schema. The shared transport lives in
// internal/llm; this package supplies only the prompt, tool schema, and verdict
// type. Any adapter error is the caller's to absorb: the cascade that wraps this
// adapter degrades to its heuristic decision on error, so this package never
// decides the gate's outcome on its own.
package checkworthy

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// defaultModel is the cheapest, fastest current Claude model, suitable for a
// binary check-worthiness judgment over one short statement. The config layer
// mirrors this default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: the model emits one small tool call
// (a boolean plus a one-sentence reason), so a tight cap keeps latency and cost
// down without truncating the result.
const maxTokens = 128

// toolName is the single tool the model is forced to call, so the verdict always
// arrives as validated structured input rather than prose.
const toolName = "record_check_worthiness"

// systemPrompt frames the judgment narrowly: a check-worthy claim is a public,
// verifiable assertion about the world, not casual small talk. It is
// deterministic and minimal - this is a classifier, not a chat - and biases to
// precision: an unsure judgment is recorded as not check-worthy, matching the
// gate's precision-over-recall stance.
const systemPrompt = "You judge whether a single spoken statement is a check-worthy public factual claim. " +
	"A statement is check-worthy only when it makes a verifiable, public assertion about the world - " +
	"a fact, statistic, event, or attributable claim that could be confirmed or refuted against evidence. " +
	"It is NOT check-worthy when it is small talk, a personal or casual declarative (\"I had coffee this morning\", " +
	"\"my flight was late\"), an opinion, a question, a greeting, a hedge, or a sentence fragment. " +
	"When you are unsure, record it as not check-worthy. Record your verdict with the record_check_worthiness tool."

// systemPromptFR is the French counterpart of systemPrompt, used in the French/EU
// political fact-checking mode. It frames the judgment in French and reasons in
// French so the gate keeps French factual statements and drops French opinions,
// questions, hedges, and fragments, mirroring systemPrompt's precision-over-recall
// stance. It is selected by domain.LocaleFrench; every other locale keeps the
// English prompt, so English behavior is unchanged when French mode is off.
const systemPromptFR = "Tu evalues si un seul enonce parle est une affirmation factuelle publique verifiable. " +
	"Un enonce est verifiable uniquement lorsqu'il avance une affirmation publique et verifiable sur le monde - " +
	"un fait, une statistique, un evenement, ou une declaration attribuable qui pourrait etre confirmee ou refutee a partir de preuves. " +
	"Il n'est PAS verifiable lorsqu'il s'agit d'une conversation anodine, d'une declaration personnelle ou banale (\"j'ai pris un cafe ce matin\", " +
	"\"mon vol etait en retard\"), d'une opinion, d'une question, d'une salutation, d'une formule prudente, ou d'un fragment de phrase. " +
	"En cas de doute, enregistre-le comme non verifiable. Enregistre ton verdict avec l'outil record_check_worthiness."

// Config wires a Client. Provider selects the LLM backend (default Anthropic);
// APIKey/GeminiAPIKey/DeepSeekAPIKey are the per-provider secrets and come from the environment
// only; an empty Model lets the selected provider apply its own default model. Locale selects the prompt
// language: the default (English) keeps the English prompt; domain.LocaleFrench
// reasons in French. The judgment is unchanged across locales - only the prompt
// language differs.
type Config struct {
	Provider       llm.ProviderName
	APIKey         string
	GeminiAPIKey   string
	DeepSeekAPIKey string
	Model          string
	Locale         domain.Locale
}

// Client is the Anthropic-backed CheckWorthinessClassifier adapter.
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
		return nil, fmt.Errorf("checkworthy: %w", err)
	}
	return &Client{llm: client, system: promptFor(cfg.Locale)}, nil
}

// promptFor selects the system prompt for the locale: the French prompt for
// domain.LocaleFrench, the English prompt for every other locale (including the
// default), so English behavior is unchanged when French mode is off.
func promptFor(locale domain.Locale) string {
	if locale.IsFrench() {
		return systemPromptFR
	}
	return systemPrompt
}

// verdict is the forced tool's structured input.
type verdict struct {
	CheckWorthy bool   `json:"check_worthy"`
	Reason      string `json:"reason"`
}

// CheckWorthy reports whether text is a check-worthy public factual claim. It
// forces a single record_check_worthiness tool call so the verdict is always
// validated structured data. Any failure (transport, missing tool block,
// malformed input) is returned as an error for the caller to degrade to its
// heuristic decision - this method never panics or blocks the live path.
func (c *Client) CheckWorthy(ctx context.Context, text string) (bool, error) {
	v, err := llm.Classify[verdict](ctx, c.llm, llm.Request{
		System:    c.system,
		User:      "Statement: " + text,
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Record whether the statement is a check-worthy public factual claim.",
			Properties: map[string]any{
				"check_worthy": map[string]any{
					"type":        "boolean",
					"description": "true if the statement is a check-worthy public factual claim, false otherwise",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "one short sentence naming why; empty when check_worthy is true",
				},
			},
			Required: []string{"check_worthy", "reason"},
		},
	})
	if err != nil {
		return false, fmt.Errorf("checkworthy: %w", err)
	}
	return v.CheckWorthy, nil
}
