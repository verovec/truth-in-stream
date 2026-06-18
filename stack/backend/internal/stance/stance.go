// Package stance is the LLM-backed adapter for the live analyzer's
// StanceClassifier port: a binary "does statement two contradict statement
// one?" judgment over two short statements. It mirrors the codebase's existing
// "external API for the hard ML step" pattern (Voyage embeddings, AssemblyAI
// transcription): the live service depends on the service.StanceClassifier
// interface, and this package supplies the Anthropic-backed implementation.
//
// The judgment is a classification, not generation, so it runs on the smallest
// fast model (Haiku) and forces a single structured tool call rather than
// parsing free text - a one-word reply is fragile under load, a forced tool
// call guarantees the schema. The shared transport lives in internal/llm; this
// package supplies only the prompt, tool schema, and verdict type. Any adapter
// error is the caller's to absorb as "no flag"; this package never decides the
// live session's fate.
package stance

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/llm"
)

// defaultModel is the cheapest, fastest current Claude model, suitable for a
// binary contradiction judgment over two short statements. The config layer
// mirrors this default; the two must stay in sync.
const defaultModel = "claude-haiku-4-5-20251001"

// maxTokens bounds the structured reply: the model emits one small tool call
// (a boolean plus a one-sentence rationale), so a tight cap keeps latency and
// cost down without truncating the result.
const maxTokens = 128

// toolName is the single tool the model is forced to call, so the verdict
// always arrives as validated structured input rather than prose.
const toolName = "record_contradiction"

// systemPrompt frames the judgment narrowly: same speaker, factual stance,
// genuine contradiction only. It is deterministic and minimal - this is a
// classifier, not a chat.
const systemPrompt = "You judge whether two statements made by the same speaker contradict each other. " +
	"They contradict only when both make a factual assertion and one asserts something the other denies - " +
	"a genuine reversal of position, not merely a different topic, an elaboration, or a restatement. " +
	"If either statement is a question, opinion, or non-assertion, they do not contradict. " +
	"Record your verdict with the record_contradiction tool."

// Config wires a Client. Provider selects the LLM backend (default Anthropic);
// APIKey/GeminiAPIKey are the per-provider secrets and come from the environment
// only; Model defaults to defaultModel when empty.
type Config struct {
	Provider     llm.ProviderName
	APIKey       string
	GeminiAPIKey string
	Model        string
}

// Client is the Anthropic-backed StanceClassifier adapter.
type Client struct {
	llm *llm.Client
}

// New builds a Client, failing when no API key is supplied (the feature is
// gated off upstream when unconfigured, so reaching here without a key is a
// wiring error). Extra request options (e.g. a test base URL) are forwarded to
// the shared transport, so a caller can point the client at a fake server.
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
		return nil, fmt.Errorf("stance: %w", err)
	}
	return &Client{llm: client}, nil
}

// verdict is the forced tool's structured input.
type verdict struct {
	Contradicts bool   `json:"contradicts"`
	Rationale   string `json:"rationale"`
}

// Contradicts reports whether later contradicts earlier, with a short rationale
// set only when it does. It forces a single record_contradiction tool call so
// the verdict is always validated structured data. Any failure (transport,
// missing tool block, malformed input) is returned as an error for the caller
// to degrade to "no flag" - this method never panics or blocks the live path.
func (c *Client) Contradicts(ctx context.Context, earlier, later string) (bool, string, error) {
	v, err := llm.Classify[verdict](ctx, c.llm, llm.Request{
		System:    systemPrompt,
		User:      fmt.Sprintf("Statement one (earlier): %s\n\nStatement two (later): %s", earlier, later),
		MaxTokens: maxTokens,
		Tool: llm.Tool{
			Name:        toolName,
			Description: "Record whether statement two contradicts statement one.",
			Properties: map[string]any{
				"contradicts": map[string]any{
					"type":        "boolean",
					"description": "true if statement two contradicts statement one, false otherwise",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "one short sentence naming the contradiction; empty when contradicts is false",
				},
			},
			Required: []string{"contradicts", "rationale"},
		},
	})
	if err != nil {
		return false, "", fmt.Errorf("stance: %w", err)
	}
	if !v.Contradicts {
		return false, "", nil
	}
	return true, v.Rationale, nil
}
