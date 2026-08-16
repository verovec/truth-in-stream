// Package rerank is the Voyage AI reranker client: it re-scores a candidate
// document list against a query with a cross-encoder relevance model
// (POST /v1/rerank), so retrieval can widen its candidate pool and let
// relevance, not raw cosine distance, decide which passages survive the cut.
// It mirrors the embed client's conventions: plain net/http, Bearer auth from
// env-supplied config, and bounded retries that honor the caller's context.
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
)

// DefaultBaseURL is the Voyage rerank endpoint.
const DefaultBaseURL = "https://api.voyageai.com/v1/rerank"

// DefaultModel is the current recommended Voyage reranker (verified 2026-08):
// multilingual across 31 languages including French, 32K context. The single
// authority for the default so config and tooling cannot drift.
const DefaultModel = "rerank-2.5"

// maxDocuments is the API's per-request document ceiling for the rerank-2.5
// family; exceeding it is a caller bug, not a retryable condition.
const maxDocuments = 1000

// Config configures the client. APIKey and Model are required; BaseURL and
// HTTPClient default to the production endpoint and a plain client (timeouts
// come from the caller's context, matching the embed client).
type Config struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
	// MaxRetries bounds re-attempts after a rate-limited, server-side, or
	// transport failure. The rerank call sits inside a live-loop deadline, so it
	// defaults low (1); the caller's fail-open fallback is the real safety net.
	MaxRetries int
}

// Client calls the Voyage rerank API.
type Client struct {
	cfg Config
}

// New validates the config and returns a client.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, errors.New("rerank: api key is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("rerank: model is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.MaxRetries < 0 {
		return nil, fmt.Errorf("rerank: max retries must be non-negative, got %d", cfg.MaxRetries)
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 1
	}
	return &Client{cfg: cfg}, nil
}

// Result is one reranked document: its index in the request's documents slice
// and the model's relevance score.
type Result struct {
	Index int
	Score float64
}

type rerankRequest struct {
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	Model           string   `json:"model"`
	Truncation      bool     `json:"truncation"`
	ReturnDocuments bool     `json:"return_documents"`
}

type rerankResponse struct {
	Data []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"data"`
}

// Rerank scores documents against query and returns the results in relevance
// order, best first. Truncation is left on so an over-long passage is clipped
// server-side rather than failing the whole call; return_documents stays off
// because the caller keeps its own candidates keyed by index.
func (c *Client) Rerank(ctx context.Context, query string, documents []string) ([]Result, error) {
	if query == "" {
		return nil, errors.New("rerank: query is empty")
	}
	if len(documents) == 0 {
		return nil, errors.New("rerank: documents are empty")
	}
	if len(documents) > maxDocuments {
		return nil, fmt.Errorf("rerank: %d documents exceed the API limit of %d", len(documents), maxDocuments)
	}

	body, err := json.Marshal(rerankRequest{
		Query:      query,
		Documents:  documents,
		Model:      c.cfg.Model,
		Truncation: true,
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: encode request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, fmt.Errorf("rerank: waiting to retry: %w", err)
			}
		}
		results, err := c.do(ctx, body, len(documents))
		if err == nil {
			return results, nil
		}
		lastErr = err
		if !isRetriable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("rerank: retries exhausted: %w", lastErr)
}

// Rank is Rerank reduced to the document order, best first: the shape the
// matcher's Reranker port consumes.
func (c *Client) Rank(ctx context.Context, query string, documents []string) ([]int, error) {
	results, err := c.Rerank(ctx, query, documents)
	if err != nil {
		return nil, err
	}
	order := make([]int, len(results))
	for i, r := range results {
		order[i] = r.Index
	}
	return order, nil
}

// statusError marks an HTTP failure with its status so the retry loop can tell
// rate limits and server errors (retryable) from caller bugs (not).
type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("rerank: api status %d: %s", e.status, e.body)
}

func (c *Client) do(ctx context.Context, body []byte, docCount int) ([]Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank: call api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("rerank: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{status: resp.StatusCode, body: truncate(payload)}
	}

	var decoded rerankResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("rerank: decode response: %w", err)
	}
	if len(decoded.Data) != docCount {
		return nil, fmt.Errorf("rerank: got %d results, want %d", len(decoded.Data), docCount)
	}
	results := make([]Result, len(decoded.Data))
	seen := make([]bool, docCount)
	for i, d := range decoded.Data {
		if d.Index < 0 || d.Index >= docCount || seen[d.Index] {
			return nil, fmt.Errorf("rerank: result index %d invalid or duplicated over %d documents", d.Index, docCount)
		}
		seen[d.Index] = true
		results[i] = Result{Index: d.Index, Score: d.RelevanceScore}
	}
	return results, nil
}

// isRetriable reports whether a failure is worth one more attempt: a rate
// limit, a server-side error, or a transport failure. Context expiry and 4xx
// caller errors are terminal. The API documents no Retry-After header on 429,
// so the backoff is client-side only.
func isRetriable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.status == http.StatusTooManyRequests || se.status >= 500
	}
	return true
}

// backoff is a short full-jitter delay: the call runs inside a strict
// live-loop timeout, so the retry must stay cheap rather than thorough.
func backoff(attempt int) time.Duration {
	base := 100 * time.Millisecond << (attempt - 1)
	return time.Duration(rand.Int64N(int64(base) + 1))
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncate(b []byte) string {
	const limit = 512
	if len(b) <= limit {
		return string(b)
	}
	return string(b[:limit]) + "..."
}
