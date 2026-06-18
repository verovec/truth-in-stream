// Package digestsummary is the LLM-backed adapter that turns shipped cards into
// one-line "what was implemented" descriptions for the development digest. It
// implements report.CardSummarizer using the shared internal/llm forced-tool
// transport, mirroring the codebase's other single-purpose classifiers
// (checkworthy, stance): the report package depends on the interface, and this
// package supplies the prompt, tool schema, and decode. The digest degrades to
// card titles when this adapter is absent or errors, so it owns only the
// summary text, never the digest's success.
package digestsummary

import (
	"context"
	"fmt"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
	"github.com/verovec/truth-in-stream/backend/internal/report"
)

// defaultModel is the cheapest, fastest current Claude model. Summarizing a
// short card title plus a few commit subjects is a light generation, so the
// small model is the right default; an empty model otherwise falls back to the
// selected provider's own default.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: one short sentence per card across a
// dozen or so cards. The cap keeps cost and latency down without truncating a
// normal epic's worth of summaries.
const maxTokens = 1024

// toolName is the single tool the model is forced to call, so the summaries
// always arrive as validated structured input rather than prose.
const toolName = "record_card_summaries"

// systemPrompt frames the task: one plain-language sentence per card describing
// what was delivered, readable by a non-engineer, keyed by identifier. It is
// deterministic and minimal.
const systemPrompt = "You summarize completed engineering cards for a development digest. " +
	"For each card you receive its identifier, its title, and the subjects of the commits that implemented it. " +
	"Write exactly one plain-language sentence per card describing what was delivered, in terms a non-engineer can follow. " +
	"Be concrete and specific. Do not restate the identifier, do not add praise or filler, do not invent work the inputs do not support. " +
	"Record a summary for every card with the record_card_summaries tool, keyed by identifier."

// Config wires a Client. Provider selects the LLM backend (default Anthropic);
// APIKey/GeminiAPIKey are the per-provider secrets and come from the environment
// only. Model defaults to defaultModel when empty.
type Config struct {
	Provider     llm.ProviderName
	APIKey       string
	GeminiAPIKey string
	Model        string
}

// Client is the LLM-backed report.CardSummarizer adapter.
type Client struct {
	llm *llm.Client
}

// New builds a Client, failing when the selected provider has no key (the
// summarizer is opt-in upstream, so reaching here without a key is a wiring
// error). Extra options (e.g. a test base URL) are forwarded to the shared
// transport.
func New(cfg Config, opts ...llm.Option) (*Client, error) {
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	client, err := llm.NewClient(llm.Config{
		Provider:     cfg.Provider,
		APIKey:       cfg.APIKey,
		GeminiAPIKey: cfg.GeminiAPIKey,
		Model:        model,
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("digestsummary: %w", err)
	}
	return &Client{llm: client}, nil
}

var _ report.CardSummarizer = (*Client)(nil)

// summaries is the forced tool's structured input: one summary per card, keyed
// by identifier.
type summaries struct {
	Summaries []struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"summaries"`
}

// Summarize returns id -> one-line description for the given cards. It forces a
// single record_card_summaries tool call so the result is always validated
// structured data. Any failure (transport, missing tool block, malformed input)
// is returned as an error for the caller to degrade to card titles; this method
// never panics.
func (c *Client) Summarize(ctx context.Context, cards []report.CardInput) (map[string]string, error) {
	if len(cards) == 0 {
		return map[string]string{}, nil
	}
	v, err := llm.Classify[summaries](ctx, c.llm, llm.Request{
		System:    systemPrompt,
		User:      renderCards(cards),
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Record a one-sentence summary for every card, keyed by its identifier.",
			Properties: map[string]any{
				"summaries": map[string]any{
					"type":        "array",
					"description": "one entry per card provided",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":      map[string]any{"type": "string", "description": "the card identifier, e.g. VER-104"},
							"summary": map[string]any{"type": "string", "description": "one plain-language sentence on what the card delivered"},
						},
						"required": []string{"id", "summary"},
					},
				},
			},
			Required: []string{"summaries"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("digestsummary: %w", err)
	}
	out := make(map[string]string, len(v.Summaries))
	for _, s := range v.Summaries {
		out[s.ID] = s.Summary
	}
	return out, nil
}

// renderCards formats the cards as the user message: each identifier and title,
// with its commit subjects bulleted beneath.
func renderCards(cards []report.CardInput) string {
	var b strings.Builder
	b.WriteString("Summarize these cards:\n")
	for _, card := range cards {
		fmt.Fprintf(&b, "\n%s: %s\n", card.ID, card.Title)
		for _, subject := range card.Subjects {
			fmt.Fprintf(&b, "  - %s\n", subject)
		}
	}
	return b.String()
}
