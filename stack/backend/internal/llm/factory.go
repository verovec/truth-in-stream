package llm

import (
	"errors"
	"fmt"
	"net/http"
)

// ProviderName identifies an LLM backend. It is the parsed form of the
// LLM_PROVIDER environment variable; an unknown value fails fast at
// construction rather than silently falling back.
type ProviderName string

const (
	// ProviderDeepSeek is the default backend: DeepSeek's cheap chat model over the
	// official OpenAI Go SDK pointed at DeepSeek's OpenAI-compatible endpoint,
	// keyed on DEEPSEEK_API_KEY. With LLM_PROVIDER unset or "deepseek", every
	// forced-tool stage runs on DeepSeek.
	ProviderDeepSeek ProviderName = "deepseek"
	// ProviderAnthropic routes the same forced-tool calls to Claude Haiku over the
	// official Anthropic SDK, selected by LLM_PROVIDER=anthropic and keyed on each
	// stage's Anthropic key.
	ProviderAnthropic ProviderName = "anthropic"
	// ProviderGemini routes the same forced-tool calls to Google Gemini over the
	// official Google Gen AI SDK, selected by LLM_PROVIDER=gemini and keyed on
	// GEMINI_API_KEY.
	ProviderGemini ProviderName = "gemini"
)

// Config selects and keys a provider. Provider defaults to ProviderDeepSeek when
// empty. APIKey is the Anthropic key; GeminiAPIKey is the Gemini key;
// DeepSeekAPIKey is the DeepSeek key - the three are separate secrets and only
// the selected provider's key is required. Model is the per-stage model override;
// an empty model falls back to the selected provider's default. No field is ever
// logged.
type Config struct {
	Provider       ProviderName
	APIKey         string
	GeminiAPIKey   string
	DeepSeekAPIKey string
	Model          string
}

// options is the provider-agnostic transport configuration a caller can tweak,
// chiefly for tests: a base URL pointing at a fake server and an HTTP client.
// Each provider maps these onto its own SDK; a caller never names a provider's
// option type.
type options struct {
	baseURL    string
	httpClient *http.Client
	maxRetries *int
}

// Option configures the transport in a provider-agnostic way. The only options
// today exist so tests can point a client at a fake server without depending on
// a provider's SDK option type.
type Option func(*options)

// WithBaseURL points the client at an alternate API endpoint - a fake server in
// tests. It maps to the selected provider's base-URL knob.
func WithBaseURL(url string) Option {
	return func(o *options) { o.baseURL = url }
}

// WithHTTPClient supplies the HTTP client the transport uses, so a test can
// inject a transport that never reaches the network.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// WithMaxRetries caps the transport's automatic retries. Tests set it to zero so
// an error response fails fast rather than waiting on backoff; production leaves
// it unset to keep each provider's default retry behavior.
func WithMaxRetries(n int) Option {
	return func(o *options) { o.maxRetries = &n }
}

// NewClient builds a Client for the configured provider. An unknown provider is
// a fatal misconfiguration and errors here rather than at the first request. A
// missing key for the selected provider errors the same way an empty key does
// for the single-provider transport the codebase shipped with, so an
// enabled-but-keyless feature degrades exactly as before. Extra options (a test
// base URL, an HTTP client) are mapped onto the selected provider's SDK.
func NewClient(cfg Config, opts ...Option) (*Client, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	provider := cfg.Provider
	if provider == "" {
		provider = ProviderDeepSeek
	}

	switch provider {
	case ProviderDeepSeek:
		p, err := newDeepSeekProvider(cfg.DeepSeekAPIKey, cfg.Model, o)
		if err != nil {
			return nil, err
		}
		return &Client{provider: p}, nil
	case ProviderAnthropic:
		p, err := newAnthropicProvider(cfg.APIKey, cfg.Model, o)
		if err != nil {
			return nil, err
		}
		return &Client{provider: p}, nil
	case ProviderGemini:
		p, err := newGeminiProvider(cfg.GeminiAPIKey, cfg.Model, o)
		if err != nil {
			return nil, err
		}
		return &Client{provider: p}, nil
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", provider)
	}
}

// errNoAPIKey is the shared "feature reached the transport without its key"
// error. Both providers return it so the degrade-on-missing-key behavior is
// identical regardless of which backend is selected.
var errNoAPIKey = errors.New("llm: api key is required")
