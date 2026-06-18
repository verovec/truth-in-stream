package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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

// issueNode is the card shape every query selects: identifier, title, update
// time, and the workflow state's display name and category type.
type issueNode struct {
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	UpdatedAt  time.Time `json:"updatedAt"`
	State      struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
}

func (n issueNode) toCardMove() CardMove {
	return CardMove{
		ID:        n.Identifier,
		Title:     n.Title,
		State:     n.State.Name,
		StateType: n.State.Type,
		UpdatedAt: n.UpdatedAt,
	}
}

// issuesQuery fetches one page of issues. first: 250 is Linear's maximum page
// size; a single project's daily activity (and its concurrent In Progress set,
// which feeds the blocker heuristic) stays well within one page, so the digest
// does not paginate.
const issuesQuery = `query Digest($filter: IssueFilter) {
  issues(filter: $filter, first: 250, orderBy: updatedAt) {
    nodes { identifier title updatedAt state { name type } }
  }
}`

// RecentMoves returns project cards updated within window.
func (c *LinearClient) RecentMoves(ctx context.Context, window time.Duration) ([]CardMove, error) {
	cutoff := c.now().Add(-window).UTC().Format(time.RFC3339)
	filter := map[string]any{
		"project":   map[string]any{"name": map[string]any{"eq": c.project}},
		"updatedAt": map[string]any{"gte": cutoff},
	}
	return c.queryIssues(ctx, filter)
}

// Remaining returns project cards that are not finished or canceled, so the
// digest can show the road ahead. The state category (not its team-specific
// name) is filtered, so renamed states keep working.
func (c *LinearClient) Remaining(ctx context.Context) ([]CardMove, error) {
	filter := map[string]any{
		"project": map[string]any{"name": map[string]any{"eq": c.project}},
		"state":   map[string]any{"type": map[string]any{"nin": []string{"completed", "canceled"}}},
	}
	return c.queryIssues(ctx, filter)
}

// InProgress returns project cards currently in the In Progress state.
func (c *LinearClient) InProgress(ctx context.Context) ([]CardMove, error) {
	filter := map[string]any{
		"project": map[string]any{"name": map[string]any{"eq": c.project}},
		"state":   map[string]any{"name": map[string]any{"eq": "In Progress"}},
	}
	return c.queryIssues(ctx, filter)
}

// epicChildrenQuery resolves an epic by team key and number, then reads its
// title and child cards. The epic is looked up by its human identifier parts so
// no internal UUID is needed.
const epicChildrenQuery = `query EpicChildren($filter: IssueFilter) {
  issues(filter: $filter, first: 1) {
    nodes {
      identifier
      title
      children(first: 250) {
        nodes { identifier title updatedAt state { name type } }
      }
    }
  }
}`

// EpicChildren returns the epic's title and its child cards. epicID is a human
// identifier such as VER-93.
func (c *LinearClient) EpicChildren(ctx context.Context, epicID string) (string, []CardMove, error) {
	teamKey, number, err := splitCardID(epicID)
	if err != nil {
		return "", nil, err
	}
	filter := map[string]any{
		"team":   map[string]any{"key": map[string]any{"eq": teamKey}},
		"number": map[string]any{"eq": number},
	}

	var data struct {
		Issues struct {
			Nodes []struct {
				Identifier string `json:"identifier"`
				Title      string `json:"title"`
				Children   struct {
					Nodes []issueNode `json:"nodes"`
				} `json:"children"`
			} `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, epicChildrenQuery, filter, &data); err != nil {
		return "", nil, err
	}
	if len(data.Issues.Nodes) == 0 {
		return "", nil, fmt.Errorf("linear: epic %s not found", epicID)
	}
	epic := data.Issues.Nodes[0]
	children := make([]CardMove, 0, len(epic.Children.Nodes))
	for _, n := range epic.Children.Nodes {
		children = append(children, n.toCardMove())
	}
	return epic.Title, children, nil
}

// queryIssues runs an issuesQuery filter and maps the nodes to CardMoves.
func (c *LinearClient) queryIssues(ctx context.Context, filter map[string]any) ([]CardMove, error) {
	var data struct {
		Issues struct {
			Nodes []issueNode `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, issuesQuery, filter, &data); err != nil {
		return nil, err
	}
	moves := make([]CardMove, 0, len(data.Issues.Nodes))
	for _, n := range data.Issues.Nodes {
		moves = append(moves, n.toCardMove())
	}
	return moves, nil
}

// do executes a GraphQL query with the given filter variable and decodes the
// response's data field into out, surfacing transport, status, and API errors.
func (c *LinearClient) do(ctx context.Context, query string, filter map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"filter": filter},
	})
	if err != nil {
		return fmt.Errorf("linear: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linear: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("linear: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: unexpected status %d", resp.StatusCode)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("linear: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("linear: api error: %s", strings.Join(msgs, "; "))
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("linear: decode data: %w", err)
	}
	return nil
}

// splitCardID splits a human card identifier such as VER-93 into its team key
// and number.
func splitCardID(id string) (string, int, error) {
	key, numStr, ok := strings.Cut(id, "-")
	if !ok || key == "" {
		return "", 0, fmt.Errorf("linear: invalid card id %q", id)
	}
	number, err := strconv.Atoi(numStr)
	if err != nil {
		return "", 0, fmt.Errorf("linear: invalid card id %q: %w", id, err)
	}
	return key, number, nil
}
