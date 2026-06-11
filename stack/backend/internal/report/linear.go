package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	linearEndpoint    = "https://api.linear.app/graphql"
	linearHTTPTimeout = 10 * time.Second
)

// LinearClient reads card activity from the Linear GraphQL API. The API key is
// sent in the Authorization header and never logged.
type LinearClient struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	project    string
	now        func() time.Time
}

// NewLinearClient builds a client scoped to a Linear project.
func NewLinearClient(apiKey, project string) *LinearClient {
	return &LinearClient{
		httpClient: &http.Client{Timeout: linearHTTPTimeout},
		endpoint:   linearEndpoint,
		apiKey:     apiKey,
		project:    project,
		now:        time.Now,
	}
}

const issuesQuery = `query Digest($filter: IssueFilter) {
  issues(filter: $filter, first: 50, orderBy: updatedAt) {
    nodes { identifier title updatedAt state { name } }
  }
}`

// RecentMoves returns project cards updated within window.
func (c *LinearClient) RecentMoves(ctx context.Context, window time.Duration) ([]CardMove, error) {
	cutoff := c.now().Add(-window).UTC().Format(time.RFC3339)
	filter := map[string]any{
		"project":   map[string]any{"name": map[string]any{"eq": c.project}},
		"updatedAt": map[string]any{"gte": cutoff},
	}
	return c.query(ctx, filter)
}

// InProgress returns project cards currently in the In Progress state.
func (c *LinearClient) InProgress(ctx context.Context) ([]CardMove, error) {
	filter := map[string]any{
		"project": map[string]any{"name": map[string]any{"eq": c.project}},
		"state":   map[string]any{"name": map[string]any{"eq": "In Progress"}},
	}
	return c.query(ctx, filter)
}

func (c *LinearClient) query(ctx context.Context, filter map[string]any) ([]CardMove, error) {
	body, err := json.Marshal(map[string]any{
		"query":     issuesQuery,
		"variables": map[string]any{"filter": filter},
	})
	if err != nil {
		return nil, fmt.Errorf("linear: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("linear: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear: unexpected status %d", resp.StatusCode)
	}

	var out struct {
		Data struct {
			Issues struct {
				Nodes []struct {
					Identifier string    `json:"identifier"`
					Title      string    `json:"title"`
					UpdatedAt  time.Time `json:"updatedAt"`
					State      struct {
						Name string `json:"name"`
					} `json:"state"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("linear: decode response: %w", err)
	}
	if len(out.Errors) > 0 {
		msgs := make([]string, len(out.Errors))
		for i, e := range out.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("linear: api error: %s", strings.Join(msgs, "; "))
	}

	moves := make([]CardMove, 0, len(out.Data.Issues.Nodes))
	for _, n := range out.Data.Issues.Nodes {
		moves = append(moves, CardMove{
			ID:        n.Identifier,
			Title:     n.Title,
			State:     n.State.Name,
			UpdatedAt: n.UpdatedAt,
		})
	}
	return moves, nil
}
