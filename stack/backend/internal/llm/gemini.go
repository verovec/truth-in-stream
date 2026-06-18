package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"google.golang.org/genai"
)

// defaultGeminiModel is the cheapest, fastest current Gemini model with function
// calling, the right default for a structured classification. It is the value
// callers fall back to when they pass an empty model under the Gemini provider.
const defaultGeminiModel = "gemini-2.5-flash"

// geminiTemperature pins the forced call to deterministic decoding, matching the
// Anthropic transport's temperature zero so a provider switch does not change
// the classification regime.
var geminiTemperature float32

// geminiProvider is the Gemini-backed forced-function-call transport: declare
// the caller's tool as a single function with its JSON schema, force the model
// to call it (function-calling mode ANY with the one allowed name) at
// temperature zero, and return the function-call arguments as raw JSON. It is
// the structural twin of anthropicProvider over a different SDK, so the
// downstream decode and deterministic guards are provider-agnostic.
type geminiProvider struct {
	models *genai.Models
	model  string
}

// newGeminiProvider builds the Gemini provider against the Gemini Developer API.
// The API key is required and must come from the environment only; an empty
// model falls back to defaultGeminiModel. The provider-agnostic options map onto
// the Gen AI SDK's client config so a test can point the client at a fake
// server.
func newGeminiProvider(apiKey, model string, o options) (*geminiProvider, error) {
	if apiKey == "" {
		return nil, errNoAPIKey
	}
	if model == "" {
		model = defaultGeminiModel
	}
	cc := &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	}
	if o.baseURL != "" {
		cc.HTTPOptions.BaseURL = o.baseURL
	}
	if o.httpClient != nil {
		cc.HTTPClient = o.httpClient
	} else {
		cc.HTTPClient = &http.Client{}
	}
	client, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		return nil, fmt.Errorf("llm: gemini client: %w", err)
	}
	return &geminiProvider{models: client.Models, model: model}, nil
}

// classify forces a single function call at temperature zero and returns the
// named function's arguments as raw JSON. It errors when the transport fails or
// when the response carries no call to the named function.
func (p *geminiProvider) classify(ctx context.Context, req Request) (json.RawMessage, error) {
	params, err := schemaFromProperties(req.Tool.Properties, req.Tool.Required)
	if err != nil {
		return nil, fmt.Errorf("gemini tool schema: %w", err)
	}

	config := &genai.GenerateContentConfig{
		Temperature: &geminiTemperature,
		// MaxOutputTokens is the per-call cap; req.MaxTokens is validated positive
		// in classify before this point, so the int32 conversion is in range and
		// never zero (a zero would tell Gemini to use its large default).
		MaxOutputTokens: int32(req.MaxTokens),
		// The system frame is a Content with no role, matching the Gen AI SDK's own
		// examples; setting RoleUser would put it on a conversation turn rather than
		// the system-instruction channel.
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: req.System}}},
		Tools: []*genai.Tool{{
			FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name:        req.Tool.Name,
				Description: req.Tool.Description,
				Parameters:  params,
			}},
		}},
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode:                 genai.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{req.Tool.Name},
			},
		},
	}

	resp, err := p.models.GenerateContent(
		ctx,
		p.model,
		genai.Text(req.User),
		config,
	)
	if err != nil {
		return nil, fmt.Errorf("forced tool call: %w", err)
	}

	for _, call := range resp.FunctionCalls() {
		if call == nil || call.Name != req.Tool.Name {
			continue
		}
		raw, err := json.Marshal(call.Args)
		if err != nil {
			return nil, fmt.Errorf("encode %s arguments: %w", req.Tool.Name, err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("response carried no %s tool call", req.Tool.Name)
}

// schemaFromProperties translates the caller's JSON-schema property map into a
// genai object Schema. The caller-supplied tool schema is a JSON-schema fragment
// (the same map both providers receive); the Gen AI SDK takes a typed *Schema,
// so the fragment is round-tripped through JSON into one. This keeps the
// caller's tool definition provider-agnostic: a caller writes one JSON schema
// and both adapters consume it.
func schemaFromProperties(properties map[string]any, required []string) (*genai.Schema, error) {
	object := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		object["required"] = required
	}
	return schemaFromJSON(object)
}

// schemaFromJSON converts one JSON-schema node (the map fragment the callers
// supply) into the Gen AI SDK's typed Schema, reading each field directly. It
// handles the field set the callers' tool schemas use - type, description, enum,
// required, object properties, and array items - and recurses into nested
// properties and items. A type the callers never emit is passed through
// unspecified so the SDK reports the mismatch rather than this layer guessing.
func schemaFromJSON(node map[string]any) (*genai.Schema, error) {
	schema := &genai.Schema{}
	if t, ok := node["type"].(string); ok {
		schema.Type = jsonSchemaType(t)
	}
	if d, ok := node["description"].(string); ok {
		schema.Description = d
	}
	if raw, ok := node["enum"].([]string); ok {
		schema.Enum = raw
	}
	if req, ok := node["required"].([]string); ok {
		schema.Required = req
	}
	if props, ok := node["properties"].(map[string]any); ok {
		schema.Properties = make(map[string]*genai.Schema, len(props))
		for name, child := range props {
			childNode, ok := child.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("property %q is not an object", name)
			}
			childSchema, err := schemaFromJSON(childNode)
			if err != nil {
				return nil, err
			}
			schema.Properties[name] = childSchema
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		itemSchema, err := schemaFromJSON(items)
		if err != nil {
			return nil, err
		}
		schema.Items = itemSchema
	}
	return schema, nil
}

// jsonSchemaType maps a lowercase JSON-schema type name to the Gen AI SDK's
// uppercase Type enum. An unrecognized type is passed through unspecified so the
// SDK reports the mismatch rather than this layer guessing.
func jsonSchemaType(t string) genai.Type {
	switch t {
	case "object":
		return genai.TypeObject
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	default:
		return genai.TypeUnspecified
	}
}
