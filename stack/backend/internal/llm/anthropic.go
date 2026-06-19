package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// defaultAnthropicModel is the cheapest, fastest current Claude model, the right
// default for a binary classification over a short statement. It is the value
// callers fall back to when they pass an empty model under the Anthropic
// provider.
const defaultAnthropicModel = "claude-haiku-4-5-20251001"

// anthropicProvider is the Anthropic-backed forced-tool transport: build a
// message, force a single tool call at temperature zero with a tight token cap,
// and return the tool-use block's structured input as raw JSON. This is the
// transport the codebase shipped with, moved behind the Provider seam unchanged.
type anthropicProvider struct {
	api   anthropic.Client
	model anthropic.Model
}

// newAnthropicProvider builds the Anthropic provider. The API key is required
// and must come from the environment only; an empty model falls back to
// defaultAnthropicModel. The provider-agnostic options are mapped onto the
// Anthropic SDK's request options so a test can point the client at a fake
// server.
func newAnthropicProvider(apiKey, model string, o options) (*anthropicProvider, error) {
	if apiKey == "" {
		return nil, errNoAPIKey
	}
	if model == "" {
		model = defaultAnthropicModel
	}
	reqOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if o.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(o.baseURL))
	}
	if o.httpClient != nil {
		reqOpts = append(reqOpts, option.WithHTTPClient(o.httpClient))
	}
	if o.maxRetries != nil {
		reqOpts = append(reqOpts, option.WithMaxRetries(*o.maxRetries))
	}
	return &anthropicProvider{
		api:   anthropic.NewClient(reqOpts...),
		model: anthropic.Model(model),
	}, nil
}

// classify forces a single tool call at temperature zero and returns the named
// tool's input as raw JSON. It errors when the transport fails or when the
// response carries no call to the named tool.
func (p *anthropicProvider) classify(ctx context.Context, req Request) (json.RawMessage, error) {
	tool := anthropic.ToolParam{
		Name:        req.Tool.Name,
		Description: anthropic.String(req.Tool.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: req.Tool.Properties,
			Required:   req.Tool.Required,
		},
	}

	msg, err := p.api.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       p.model,
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
		return nil, fmt.Errorf("forced tool call: %w", err)
	}

	return anthropicToolInput(msg, req.Tool.Name)
}

// reason runs the second-pass call with an auto tool choice (the tool is offered,
// not forced by name) so the model may deliberate before calling it. Anthropic has
// no DeepSeek-style restriction on combining tool use with reasoning, so this path
// mirrors classify but with ToolChoiceAuto; the shared Provider contract is
// "structured output without a forced name", which an auto choice satisfies. A
// response with no tool-use block (the model answered in prose) surfaces as an
// error the caller absorbs.
func (p *anthropicProvider) reason(ctx context.Context, req Request) (json.RawMessage, error) {
	tool := anthropic.ToolParam{
		Name:        req.Tool.Name,
		Description: anthropic.String(req.Tool.Description),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: req.Tool.Properties,
			Required:   req.Tool.Required,
		},
	}

	msg, err := p.api.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       p.model,
		MaxTokens:   req.MaxTokens,
		Temperature: anthropic.Float(0),
		System:      []anthropic.TextBlockParam{{Text: req.System}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.User)),
		},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}},
	})
	if err != nil {
		return nil, fmt.Errorf("reasoning tool call: %w", err)
	}

	return anthropicToolInput(msg, req.Tool.Name)
}

// anthropicToolInput returns the named tool-use block's input as raw JSON, shared
// by the forced classify and the auto reason paths. It errors when the response
// carries no call to the named tool.
func anthropicToolInput(msg *anthropic.Message, toolName string) (json.RawMessage, error) {
	for _, block := range msg.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != toolName {
			continue
		}
		return json.RawMessage(tu.Input), nil
	}
	return nil, fmt.Errorf("response carried no %s tool call", toolName)
}
