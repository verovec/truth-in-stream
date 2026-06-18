package llm

import (
	"context"
	"encoding/json"
	"fmt"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// deepSeekBaseURL is DeepSeek's OpenAI-compatible Chat Completions endpoint. The
// OpenAI SDK appends /chat/completions, so this bare host is the value DeepSeek's
// own docs document; it is the fallback used when no transport base URL is set
// (tests point the client at a fake server instead).
const deepSeekBaseURL = "https://api.deepseek.com"

// defaultDeepSeekModel is DeepSeek's cheap, fast chat model with function
// calling - the forward-safe id (the legacy deepseek-chat alias maps to it and is
// being retired). It is NOT the reasoner/thinking variant, which does not support
// tool calling or temperature. Callers fall back to it when they pass an empty
// model under the DeepSeek provider.
const defaultDeepSeekModel = "deepseek-v4-flash"

// deepSeekTemperature pins the forced call to deterministic decoding, matching the
// other transports' temperature zero so a provider switch does not change the
// classification regime.
const deepSeekTemperature = 0

// deepseekProvider is the DeepSeek-backed forced-tool transport over the official
// OpenAI Go SDK pointed at DeepSeek's OpenAI-compatible endpoint: declare the
// caller's tool as a single function whose parameters are the caller's JSON
// schema, force that exact function by name at temperature zero, and return the
// tool call's arguments as raw JSON. It is the structural twin of
// anthropicProvider and geminiProvider over a third SDK, so the downstream decode
// and deterministic guards stay provider-agnostic.
type deepseekProvider struct {
	api   openai.Client
	model string
}

// newDeepSeekProvider builds the DeepSeek provider. The API key is required and
// must come from the environment only (the SDK's implicit OPENAI_API_KEY lookup
// is never relied on - the key is passed explicitly); an empty model falls back
// to defaultDeepSeekModel. The provider-agnostic options map onto the OpenAI
// SDK's request options - the base URL defaults to DeepSeek's host and a test can
// override it to point at a fake server.
func newDeepSeekProvider(apiKey, model string, o options) (*deepseekProvider, error) {
	if apiKey == "" {
		return nil, errNoAPIKey
	}
	if model == "" {
		model = defaultDeepSeekModel
	}
	baseURL := deepSeekBaseURL
	if o.baseURL != "" {
		baseURL = o.baseURL
	}
	reqOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
	}
	if o.httpClient != nil {
		reqOpts = append(reqOpts, option.WithHTTPClient(o.httpClient))
	}
	if o.maxRetries != nil {
		reqOpts = append(reqOpts, option.WithMaxRetries(*o.maxRetries))
	}
	return &deepseekProvider{
		api:   openai.NewClient(reqOpts...),
		model: model,
	}, nil
}

// classify forces a single tool call at temperature zero and returns the named
// tool's arguments as raw JSON. It errors when the transport fails or when the
// response carries no call to the named tool. The caller's JSON-schema property
// map is passed straight onto the function parameters (the OpenAI API takes JSON
// schema directly, so no typed-schema translation is needed). frequency_penalty
// and presence_penalty are deliberately omitted: DeepSeek rejects them.
func (p *deepseekProvider) classify(ctx context.Context, req Request) (json.RawMessage, error) {
	parameters := openai.FunctionParameters{
		"type":       "object",
		"properties": req.Tool.Properties,
	}
	if len(req.Tool.Required) > 0 {
		parameters["required"] = req.Tool.Required
	}

	resp, err := p.api.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(p.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(req.System),
			openai.UserMessage(req.User),
		},
		Temperature: openai.Float(deepSeekTemperature),
		// DeepSeek honors max_tokens (not max_completion_tokens); req.MaxTokens is
		// validated positive in Classify before this point.
		MaxTokens: openai.Int(req.MaxTokens),
		Tools: []openai.ChatCompletionToolUnionParam{
			openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
				Name:        req.Tool.Name,
				Description: openai.String(req.Tool.Description),
				Parameters:  parameters,
			}),
		},
		ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
			OfFunctionToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
				Function: openai.ChatCompletionNamedToolChoiceFunctionParam{Name: req.Tool.Name},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("forced tool call: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("response carried no %s tool call", req.Tool.Name)
	}
	for _, call := range resp.Choices[0].Message.ToolCalls {
		if call.Function.Name != req.Tool.Name {
			continue
		}
		return json.RawMessage(call.Function.Arguments), nil
	}
	return nil, fmt.Errorf("response carried no %s tool call", req.Tool.Name)
}
