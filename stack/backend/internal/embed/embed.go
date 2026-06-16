// Package embed is a client for the Voyage AI text embeddings API.
//
// Voyage exposes no official Go SDK, so this is a direct net/http client.
// Verified against docs.voyageai.com (2026-06): POST
// https://api.voyageai.com/v1/embeddings, Bearer auth, request fields
// input/model/input_type/output_dimension/output_dtype, response
// {data:[{index,embedding}], usage:{total_tokens}}. voyage-4-large defaults to
// and supports 1024 output dimensions; input_type is "document" for stored text
// and "query" for retrieval queries.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.voyageai.com/v1/embeddings"
	defaultTimeout = 30 * time.Second
	// outputDtype "float" returns float32 vectors; there is no half-precision
	// dtype, so the halfvec narrowing happens in pgvector on write.
	outputDtype = "float"
)

type inputType string

const (
	inputTypeDocument inputType = "document"
	inputTypeQuery    inputType = "query"
)

// Config configures a Client. APIKey, Model, and Dim are required; BaseURL and
// HTTPClient are optional and default to the Voyage endpoint and a client with
// a sane timeout.
type Config struct {
	APIKey     string
	Model      string
	Dim        int
	BaseURL    string
	HTTPClient *http.Client
}

// Client calls the Voyage embeddings API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	model      string
	dim        int
}

// New builds a Client from cfg, applying defaults for the optional fields.
func New(cfg Config) *Client {
	c := &Client{
		httpClient: cfg.HTTPClient,
		baseURL:    cfg.BaseURL,
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		dim:        cfg.Dim,
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultTimeout}
	}
	if c.baseURL == "" {
		c.baseURL = defaultBaseURL
	}
	return c
}

// APIError is a non-2xx response from the Voyage API. Callers can match it with
// errors.As to distinguish provider failures from store failures. RetryAfter
// carries the server's requested back-off when a 429 response sets the
// Retry-After header, so the retry decorator can honor the provider's own
// pacing instead of guessing; it is zero when the header is absent or
// unparseable.
type APIError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("voyage: api status %d: %s", e.StatusCode, e.Body)
}

// parseRetryAfter reads an HTTP Retry-After header in either of its standard
// forms - a delay in whole seconds or an HTTP-date - returning the delay until
// the client may retry. now is passed in so the date form is testable. An empty,
// malformed, or past value yields zero, meaning "no server-provided back-off".
func parseRetryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

type embedRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension"`
	OutputDtype     string   `json:"output_dtype"`
}

type embedData struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type embedResponse struct {
	Data  []embedData `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// EmbedDocuments embeds texts for storage (input_type=document).
func (c *Client) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts, inputTypeDocument)
}

// EmbedQueries embeds texts for retrieval (input_type=query).
func (c *Client) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts, inputTypeQuery)
}

func (c *Client) embed(ctx context.Context, texts []string, it inputType) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	payload, err := json.Marshal(embedRequest{
		Input:           texts,
		Model:           c.model,
		InputType:       string(it),
		OutputDimension: c.dim,
		OutputDtype:     outputDtype,
	})
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("voyage: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}

	var decoded embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("voyage: decode response: %w", err)
	}
	return c.orderEmbeddings(decoded.Data, len(texts))
}

// orderEmbeddings places each returned embedding at its declared input index,
// validating count and dimension. The API guarantees index maps to input[i]
// but does not guarantee array order, so we reorder explicitly.
func (c *Client) orderEmbeddings(data []embedData, n int) ([][]float32, error) {
	if len(data) != n {
		return nil, fmt.Errorf("voyage: got %d embeddings, want %d", len(data), n)
	}
	out := make([][]float32, n)
	for _, d := range data {
		if d.Index < 0 || d.Index >= n {
			return nil, fmt.Errorf("voyage: response index %d out of range [0,%d)", d.Index, n)
		}
		if out[d.Index] != nil {
			return nil, fmt.Errorf("voyage: duplicate response index %d", d.Index)
		}
		if len(d.Embedding) != c.dim {
			return nil, fmt.Errorf("voyage: embedding %d has %d dims, want %d", d.Index, len(d.Embedding), c.dim)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
