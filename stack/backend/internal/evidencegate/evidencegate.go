// Package evidencegate is the LLM-backed fact-checkability gate for the crawl
// ingestion producer: a binary "does this passage contain verifiable, citable
// factual content suitable as fact-check evidence?" judgment over one wiki
// chunk. It mirrors the codebase's "external API for the hard ML step" pattern
// (Voyage embeddings, AssemblyAI transcription, the stance and check-worthiness
// adapters): the crawl producer depends on a narrow consumer interface and this
// package supplies the Anthropic-backed implementation.
//
// It is deliberately distinct from internal/checkworthy: that judges a single
// short SPOKEN statement for the live path, biased to precision; this judges a
// 256-512-token encyclopedia PASSAGE for whether it is citable evidence, biased
// to recall so real evidence is not discarded. The shared forced-tool transport
// lives in internal/llm; this package supplies only the prompt, tool schema, and
// verdict type. Any adapter error is the caller's to absorb: the crawl producer
// publishes the chunk anyway (fail-open) so a flaky model can never silently
// empty the corpus.
package evidencegate

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// defaultModel is the cheapest, fastest current Claude model, suitable for a
// binary fact-checkability judgment over one passage. The config layer mirrors
// this default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: the model emits one small tool call (a
// boolean plus a one-sentence reason), so a tight cap keeps latency and cost
// down without truncating the result.
const maxTokens = 128

// toolName is the single tool the model is forced to call, so the verdict always
// arrives as validated structured input rather than prose.
const toolName = "record_fact_checkability"

// systemPrompt frames the judgment: a fact-checkable passage states concrete,
// verifiable assertions that could support or refute a public claim, not
// navigational or meta prose. It biases to recall - when genuinely unsure it
// keeps the passage - because dropping real citable evidence is worse for the
// corpus than retaining the occasional marginal chunk.
const systemPrompt = "You judge whether a passage from an encyclopedia article contains verifiable, " +
	"citable factual content suitable as evidence when fact-checking a public claim. " +
	"A passage IS fact-checkable when it states concrete, checkable facts - dates, figures, named events, " +
	"measurable properties, causal or attributable claims - that could confirm or refute an assertion about the world. " +
	"It is NOT fact-checkable when it is purely navigational, a disambiguation note, meta-commentary about the article " +
	"itself, a bare list of links or titles, or vague prose carrying no checkable assertion. " +
	"When you are genuinely unsure, judge it fact-checkable: missing real evidence is worse than keeping a marginal " +
	"passage. Record your verdict with the record_fact_checkability tool."

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

// Client is the Anthropic-backed fact-checkability gate adapter.
type Client struct {
	llm *llm.Client
}

// New builds a Client, failing when no API key is supplied (the gate is wired
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
		return nil, fmt.Errorf("evidencegate: %w", err)
	}
	return &Client{llm: client}, nil
}

// verdict is the forced tool's structured input.
type verdict struct {
	FactCheckable bool   `json:"fact_checkable"`
	Reason        string `json:"reason"`
}

// FactCheckable reports whether passage contains verifiable, citable factual
// content suitable as fact-check evidence. It forces a single
// record_fact_checkability tool call so the verdict is always validated
// structured data. Any failure (transport, missing tool block, malformed input)
// is returned as an error for the producer to degrade fail-open - this method
// never panics or decides the gate's outcome on its own.
func (c *Client) FactCheckable(ctx context.Context, passage string) (bool, error) {
	v, err := llm.Classify[verdict](ctx, c.llm, llm.Request{
		System:    systemPrompt,
		User:      "Passage:\n" + passage,
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Record whether the passage contains verifiable, citable factual evidence.",
			Properties: map[string]any{
				"fact_checkable": map[string]any{
					"type":        "boolean",
					"description": "true if the passage contains verifiable, citable factual evidence, false otherwise",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "one short sentence naming why; empty when fact_checkable is true",
				},
			},
			Required: []string{"fact_checkable", "reason"},
		},
	})
	if err != nil {
		return false, fmt.Errorf("evidencegate: %w", err)
	}
	return v.FactCheckable, nil
}
