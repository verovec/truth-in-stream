package report

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	githubBaseURL     = "https://api.github.com"
	githubHTTPTimeout = 10 * time.Second
)

// GitHubClient reads open pull requests from the GitHub REST API. A token is
// optional for a public repository but raises the rate limit; it is never
// logged.
type GitHubClient struct {
	httpClient *http.Client
	baseURL    string
	repo       string
	token      string
}

// NewGitHubClient builds a client for repo ("owner/name").
func NewGitHubClient(repo, token string) *GitHubClient {
	return &GitHubClient{
		httpClient: &http.Client{Timeout: githubHTTPTimeout},
		baseURL:    githubBaseURL,
		repo:       repo,
		token:      token,
	}
}

// OpenPRs returns the repository's open pull requests.
func (c *GitHubClient) OpenPRs(ctx context.Context) ([]PullRequest, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=open&per_page=100", c.baseURL, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: unexpected status %d", resp.StatusCode)
	}

	var raw []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("github: decode response: %w", err)
	}

	prs := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		prs = append(prs, PullRequest{
			Number: r.Number,
			Title:  r.Title,
			Author: r.User.Login,
			URL:    r.HTMLURL,
			Draft:  r.Draft,
		})
	}
	return prs, nil
}
