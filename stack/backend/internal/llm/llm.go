// Package llm is the shared Anthropic forced-tool-call transport behind the
// codebase's single-purpose LLM classifiers (the live stance check, the
// check-worthiness gate, and the ingestion fact-checkability gate). Each
// classifier owns its model choice, system prompt, tool schema, and result
// type; this package owns only the transport: client construction, one forced
// tool call at temperature zero with a tight token cap, tool-use-block
// extraction, and decoding the structured input into a caller-supplied type. It
// has no knowledge of what any individual verdict means, so adding a classifier
// is a new caller here rather than a third copy of the Anthropic client.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is the cheapest, fastest current Claude model, the right default
// for a binary classification over a short statement. Callers supply their own
// model; this is the fallback used only when a caller passes an empty model.
const DefaultModel = "claude-haiku-4-5-20251001"

// Client wraps the Anthropic SDK client and the model every Classify call uses.
// It is safe for concurrent use: the underlying SDK client is, and the model is
// immutable after construction.
type Client struct {
	api   anthropic.Client
	model anthropic.Model
}

// NewClient builds a Client. The API key is required and must come from the
// environment only; an empty model falls back to DefaultModel. Extra request
// options (e.g. a test base URL) are appended after the key so a caller can
// point the client at a fake server.
func NewClient(apiKey, model string, opts ...option.RequestOption) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("llm: api key is required")
	}
	if model == "" {
		model = DefaultModel
	}
	clientOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &Client{
		api:   anthropic.NewClient(clientOpts...),
		model: anthropic.Model(model),
	}, nil
}

// Tool describes the single tool the model is forced to call. Properties and
// Required are the JSON-schema fields of the structured verdict the caller
// decodes back.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

// Request is one forced-tool classification: a system frame, the user statement
// to classify, the tool whose input carries the verdict, and a tight token cap.
type Request struct {
	System    string
	User      string
	Tool      Tool
	MaxTokens int64
}

// Classify runs req as a single forced tool call at temperature zero and decodes
// the named tool's structured input into T. It errors when the transport fails,
// when the response carries no call to the named tool, or when the tool input
// does not decode into T. Callers wrap the returned error with their own package
// context.
func Classify[T any](ctx context.Context, c *Client, req Request) (T, error) {
	var zero T
	tool := anthropic.ToolParam{
		Name:        req.Tool.Name,
		Description: anthropic.String(req.Tool.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: req.Tool.Properties,
			Required:   req.Tool.Required,
		},
	}

	msg, err := c.api.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       c.model,
		MaxTokens:   req.MaxTokens,
		Temperature: anthropic.Float(0),
		System:      []anthropic.TextBlockParam{{Text: req.System}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.User)),
		},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: req.Tool.Name}},
	})
	if err != nil {
		return zero, fmt.Errorf("forced tool call: %w", err)
	}

	for _, block := range msg.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != req.Tool.Name {
			continue
		}
		var out T
		if err := json.Unmarshal(tu.Input, &out); err != nil {
			return zero, fmt.Errorf("decode %s input: %w", req.Tool.Name, err)
		}
		return out, nil
	}
	return zero, fmt.Errorf("response carried no %s tool call", req.Tool.Name)
}
