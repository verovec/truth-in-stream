package legifrance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// getArticlePath is the consult endpoint that returns one article's consolidated
// text by its identifier.
const getArticlePath = "/consult/getArticle"

// Article is the subset of a getArticle response's article object this connector
// reads, matching the documented PISTE Legifrance shape. Texte is the
// consolidated article text; the other fields carry the citation provenance.
type Article struct {
	ID        string `json:"id"`
	Num       string `json:"num"`
	Etat      string `json:"etat"`
	Type      string `json:"type"`
	Cid       string `json:"cid"`
	DateDebut string `json:"dateDebut"`
	DateFin   string `json:"dateFin"`
	Texte     string `json:"texte"`
}

// getArticleResponse is the getArticle response envelope.
type getArticleResponse struct {
	ExecutionTime int      `json:"executionTime"`
	Dereferenced  bool     `json:"dereferenced"`
	Article       *Article `json:"article"`
}

// TokenProvider yields a valid bearer token. *TokenSource satisfies it; a test
// supplies a stub.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// Client calls the PISTE Legifrance consult API, attaching a fresh bearer token
// to each request. It owns only the transport; the producer owns the corpus and
// the diff.
type Client struct {
	baseURL    string
	tokens     TokenProvider
	httpClient *http.Client
}

// NewClient builds a consult client. An empty baseURL defaults to the PISTE
// production base.
func NewClient(baseURL string, tokens TokenProvider, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: baseURL, tokens: tokens, httpClient: httpClient}
}

// GetArticle fetches one article by its Legifrance identifier (a LEGIARTI id). It
// returns a nil article and no error when the API dereferences the id to nothing
// (a repealed or unknown article), so the producer skips it rather than
// publishing an empty passage.
func (c *Client) GetArticle(ctx context.Context, id string) (*Article, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return nil, fmt.Errorf("legifrance: encode getArticle request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+getArticlePath, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("legifrance: build getArticle request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("legifrance: getArticle %q: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("legifrance: getArticle %q: unexpected status %s", id, resp.Status)
	}
	var gr getArticleResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("legifrance: decode getArticle %q: %w", id, err)
	}
	if gr.Article == nil || gr.Article.Texte == "" {
		return nil, nil
	}
	return gr.Article, nil
}
