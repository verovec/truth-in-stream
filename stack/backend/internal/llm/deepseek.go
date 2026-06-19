package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// deepSeekBaseURL is DeepSeek's OpenAI-compatible Chat Completions endpoint. The
// OpenAI SDK appends /chat/completions, so this bare host is the value DeepSeek's
// own docs document; it is the fallback used when no transport base URL is set
// (tests point the client at a fake server instead).
const deepSeekBaseURL = "https://api.deepseek.com"

// defaultDeepSeekModel is DeepSeek's cheap, fast hybrid model. It defaults to
// thinking mode ON, and DeepSeek rejects a forced (named) tool_choice while
// thinking is enabled, so classify must disable thinking per request (see
// thinkingDisabled). The legacy deepseek-chat alias was this model with thinking
// off and is being retired. Callers fall back to it when they pass an empty model
// under the DeepSeek provider.
const defaultDeepSeekModel = "deepseek-v4-flash"

// deepSeekTemperature pins the forced call to deterministic decoding, matching the
// other transports' temperature zero so a provider switch does not change the
// classification regime.
const deepSeekTemperature = 0

// thinkingDisabled is the request-body field DeepSeek's hybrid models read to turn
// thinking mode off. Thinking is on by default, and DeepSeek returns 400
// "Thinking mode does not support this tool_choice" for a forced (named)
// tool_choice while it is on; disabling it restores deterministic forced-tool
// classification and lets temperature zero take effect.
var thinkingDisabled = map[string]any{"type": "disabled"}

// thinkingEnabled is the request-body field that turns DeepSeek's reasoning
// (thinking) mode on for the deeper-reasoner second pass. Thinking is the model's
// default, so this is explicit rather than strictly required, and it pairs with
// an auto (never named) tool_choice - DeepSeek rejects a forced named tool_choice
// while thinking is on, so the reason path offers a single tool and lets the model
// choose to call it after deliberating.
var thinkingEnabled = map[string]any{"type": "enabled"}

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
	}, option.WithJSONSet("thinking", thinkingDisabled))
	if err != nil {
		return nil, fmt.Errorf("forced tool call: %w", err)
	}

	return toolCallArguments(resp, req.Tool.Name)
}

// reason runs the thinking-enabled second-pass call: the same single tool is
// offered, but with an auto tool_choice and thinking turned on, so the model may
// deliberate (its reasoning_content) before emitting the tool call. The named and
// "required" tool_choice values are deliberately not used here - DeepSeek rejects
// both while thinking is enabled - so the tool is offered and the model chooses to
// call it. Temperature and the penalty knobs are omitted because thinking mode
// ignores them; max_tokens still bounds the (reasoning + answer) output. A
// thinking model can answer in prose without calling the tool, so a missing tool
// call surfaces as an error the caller absorbs rather than a forced retry.
func (p *deepseekProvider) reason(ctx context.Context, req Request) (json.RawMessage, error) {
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
		MaxTokens: openai.Int(req.MaxTokens),
		Tools: []openai.ChatCompletionToolUnionParam{
			openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
				Name:        req.Tool.Name,
				Description: openai.String(req.Tool.Description),
				Parameters:  parameters,
			}),
		},
		ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		},
	}, option.WithJSONSet("thinking", thinkingEnabled))
	if err != nil {
		return nil, fmt.Errorf("reasoning tool call: %w", err)
	}

	return toolCallArguments(resp, req.Tool.Name)
}

// toolCallArguments extracts the named tool call's arguments as raw JSON from a
// chat completion, shared by the forced-tool classify and the auto-tool reason
// paths. It errors when the response carries no choice or no call to the named
// tool (on the reason path the latter also covers a thinking model that answered
// in prose without calling the offered tool).
func toolCallArguments(resp *openai.ChatCompletion, toolName string) (json.RawMessage, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("response carried no %s tool call", toolName)
	}
	for _, call := range resp.Choices[0].Message.ToolCalls {
		if call.Function.Name != toolName {
			continue
		}
		// DeepSeek occasionally returns the forced tool call with empty arguments
		// (a degenerate passage, or the reply truncated at max_tokens). Passing the
		// empty string through would surface as a cryptic "unexpected end of JSON
		// input" on decode, so report the cause directly; the caller still degrades
		// fail-open on the error.
		if strings.TrimSpace(call.Function.Arguments) == "" {
			if resp.Choices[0].FinishReason == "length" {
				return nil, fmt.Errorf("%s tool call truncated at max_tokens", toolName)
			}
			return nil, fmt.Errorf("empty %s tool-call arguments", toolName)
		}
		return json.RawMessage(call.Function.Arguments), nil
	}
	return nil, fmt.Errorf("response carried no %s tool call", toolName)
}
