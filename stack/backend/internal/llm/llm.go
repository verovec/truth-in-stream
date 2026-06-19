// Package llm is the shared forced-tool-call transport behind the codebase's
// single-purpose LLM classifiers (the live stance check, the check-worthiness
// gate, claim decomposition, claim typing, the credibility verifier, and the
// ingestion fact-checkability gate). Each classifier owns its model choice,
// system prompt, tool schema, and result type; this package owns only the
// transport: one forced tool call at temperature zero with a tight token cap,
// tool-call extraction, and decoding the structured arguments into a
// caller-supplied type. It has no knowledge of what any individual verdict
// means, so adding a classifier is a new caller here rather than another copy
// of a provider client.
//
// The transport is provider-agnostic: a Provider captures the forced-tool /
// structured-output contract, and NewClient selects an implementation from
// configuration. DeepSeek (a cheap OpenAI-compatible chat model) is the default;
// Anthropic (Claude Haiku) and Gemini are alternatives selected by LLM_PROVIDER.
// Callers route through NewClient and never name a provider.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
)

// DefaultModel is the cheapest, fastest current Claude model, the right default
// for a binary classification over a short statement. Callers supply their own
// model; this is the fallback used only when a caller passes an empty model and
// the selected provider is Anthropic. Each provider applies its own default
// when the model is empty.
const DefaultModel = defaultAnthropicModel

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
// to classify, the tool whose arguments carry the verdict, and a tight token
// cap.
type Request struct {
	System    string
	User      string
	Tool      Tool
	MaxTokens int64
}

// Provider is the structured-output transport contract every LLM backend
// satisfies. It has two paths over the same Request/Tool shape, both returning
// the tool's structured arguments as raw JSON for the caller to decode:
//
//   - classify is the fast path: a single forced (named) tool call at temperature
//     zero with thinking off, used by every binary/structured classifier.
//   - reason is the thinking-enabled path: the model is offered a single tool with
//     an auto tool choice (never forced by name) so it may deliberate before
//     emitting the tool call, used by the deeper reasoner second pass.
//
// The split exists because DeepSeek - the default backend - rejects a forced
// named tool_choice while thinking is enabled, so a reasoning call cannot reuse
// the forced-tool path. Implementations map provider errors uniformly and wrap
// them with %w. The interface is unexported-by-method so the generic Classify
// and Reason are the only ways callers decode a verdict; a provider never learns
// the caller's type.
type Provider interface {
	// classify runs the forced tool call and returns the tool arguments as raw
	// JSON. It errors when the transport fails or when the response carries no
	// call to the named tool.
	classify(ctx context.Context, req Request) (json.RawMessage, error)
	// reason runs the thinking-enabled call (auto tool choice, single tool
	// offered) and returns the tool arguments as raw JSON. It errors when the
	// transport fails or when the model deliberated but emitted no call to the
	// offered tool (a thinking model may answer in prose instead of calling the
	// tool, since the choice is not forced).
	reason(ctx context.Context, req Request) (json.RawMessage, error)
}

// Client is the transport every Classify call uses. It wraps the configured
// Provider and is safe for concurrent use when the underlying provider client
// is, which both adapters are.
type Client struct {
	provider Provider
}

// Classify runs req as a single forced tool call at temperature zero and decodes
// the named tool's structured arguments into T. It errors when MaxTokens is not
// positive, when the transport fails, when the response carries no call to the
// named tool, or when the tool arguments do not decode into T. Callers wrap the
// returned error with their own package context.
func Classify[T any](ctx context.Context, c *Client, req Request) (T, error) {
	var zero T
	// A non-positive cap is a caller bug. Reject it uniformly so the two providers
	// behave alike: Anthropic rejects max_tokens<=0 with a 400, while Gemini would
	// silently treat MaxOutputTokens 0 as its large default and issue an uncapped
	// call - a divergence a zero-value Request would otherwise expose.
	if req.MaxTokens <= 0 {
		return zero, fmt.Errorf("llm: MaxTokens must be positive, got %d", req.MaxTokens)
	}
	raw, err := c.provider.classify(ctx, req)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("decode %s arguments: %w", req.Tool.Name, err)
	}
	return out, nil
}

// Reason runs req on the thinking-enabled structured-output path - the model is
// offered a single tool with an auto tool choice (never forced by name) so it may
// deliberate before emitting the tool call - and decodes the tool's structured
// arguments into T. It is the deeper-reasoner counterpart of Classify: same
// Request/Tool shape and decode, but a reasoning model behind it. It errors when
// MaxTokens is not positive, when the transport fails, when the model answered in
// prose without calling the offered tool, or when the tool arguments do not decode
// into T. Callers wrap the returned error with their own package context.
func Reason[T any](ctx context.Context, c *Client, req Request) (T, error) {
	var zero T
	if req.MaxTokens <= 0 {
		return zero, fmt.Errorf("llm: MaxTokens must be positive, got %d", req.MaxTokens)
	}
	raw, err := c.provider.reason(ctx, req)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("decode %s arguments: %w", req.Tool.Name, err)
	}
	return out, nil
}
