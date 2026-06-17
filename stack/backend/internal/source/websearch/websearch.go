// Package websearch retrieves open-web evidence for open-ended claims (causal,
// comparative, attribution) that no structured source answers. It wraps the
// Brave Search API behind the source.Retriever contract.
//
// Provider decision (verified 2026-06-17 via Context7 + web): Brave Search API,
// REST v1 (api.search.brave.com/res/v1/web/search). Chosen over Bing (the
// standalone Web Search API is retired), SerpAPI (scraping, fragile), Exa
// (embedding search, thinner French coverage), and Tavily (an aggregator, not a
// first-party index). Brave is an independent first-party index with documented
// French support (country=FR, search_lang=fr), a stable JSON v1 endpoint, a free
// tier, and plain JSON well suited to Go. The key is a per-account
// subscription token sent in the X-Subscription-Token header.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// braveBaseURL is the Brave web-search REST endpoint.
const braveBaseURL = "https://api.search.brave.com/res/v1/web/search"

// sourceName is the publisher family shown for web evidence; the per-result host
// is the specific provenance carried on each passage.
const sourceName = "Recherche web"

const (
	// defaultTimeout bounds a single search; web search sits on the latency
	// budget like any live retrieval.
	defaultTimeout = 6 * time.Second
	// defaultCount is how many results to request; enough for the verifier to
	// corroborate without flooding the prompt.
	defaultCount = 5
	// defaultLang / defaultCountry default the search to French results, the
	// jurisdiction this pack serves.
	defaultLang    = "fr"
	defaultCountry = "FR"
)

// Config configures the web-search pack. APIKey is the Brave subscription token
// and is required; it comes from the environment and is never logged. The
// remaining fields fall back to package defaults when zero, and BaseURL
// overrides the endpoint for tests.
type Config struct {
	APIKey  string
	Timeout time.Duration
	Count   int
	Lang    string
	Country string
	BaseURL string
}

// Pack retrieves French web passages as evidence. It satisfies source.Retriever.
type Pack struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	count      int
	lang       string
	country    string
}

// New builds a web-search pack. It returns an error when the API key is empty,
// since the pack cannot call Brave without it; the caller fails fast at wiring
// rather than discovering it on the first claim.
func New(cfg Config) (*Pack, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("websearch: API key is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	count := cfg.Count
	if count <= 0 {
		count = defaultCount
	}
	lang := cfg.Lang
	if lang == "" {
		lang = defaultLang
	}
	country := cfg.Country
	if country == "" {
		country = defaultCountry
	}
	return &Pack{
		httpClient: &http.Client{Timeout: timeout},
		apiKey:     cfg.APIKey,
		baseURL:    cfg.baseEndpoint(),
		count:      count,
		lang:       lang,
		country:    country,
	}, nil
}

func (c Config) baseEndpoint() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return braveBaseURL
}

// Kind reports the source family.
func (p *Pack) Kind() source.Kind { return source.KindWebSearch }

// braveResponse mirrors the Brave web-search wire shape, only the fields this
// pack reads. The vendor types never leak past this package.
type braveResponse struct {
	Web struct {
		Results []braveResult `json:"results"`
	} `json:"web"`
}

type braveResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Description   string   `json:"description"`
	Language      string   `json:"language"`
	Age           string   `json:"age"`
	ExtraSnippets []string `json:"extra_snippets"`
	MetaURL       struct {
		Hostname string `json:"hostname"`
	} `json:"meta_url"`
}

// Retrieve runs a French web search for the claim and returns one evidence
// passage per result, each carrying the result's host as provenance and a stable
// id keyed by that host. An empty query text returns no evidence rather than a
// useless wildcard search.
func (p *Pack) Retrieve(ctx context.Context, q source.Query) ([]source.Evidence, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, nil
	}

	resp, err := p.search(ctx, q)
	if err != nil {
		return nil, err
	}

	out := make([]source.Evidence, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		out = append(out, source.Evidence{
			ID:      source.NewEvidenceID(source.KindWebSearch, hostOf(r), len(out)),
			Passage: renderResult(r),
			Source: source.Source{
				Name: sourceName,
				URL:  r.URL,
				Date: r.Age,
			},
		})
	}
	return out, nil
}

func (p *Pack) search(ctx context.Context, q source.Query) (braveResponse, error) {
	lang := q.Lang
	if lang == "" {
		lang = p.lang
	}

	values := url.Values{}
	values.Set("q", q.Text)
	values.Set("country", p.country)
	values.Set("search_lang", lang)
	values.Set("count", strconv.Itoa(p.count))
	values.Set("extra_snippets", "true")
	reqURL := p.baseURL + "?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return braveResponse{}, fmt.Errorf("building web-search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return braveResponse{}, fmt.Errorf("running web search: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusOK {
		return braveResponse{}, fmt.Errorf("running web search: status %d", httpResp.StatusCode)
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return braveResponse{}, fmt.Errorf("reading web-search response: %w", err)
	}
	var resp braveResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return braveResponse{}, fmt.Errorf("decoding web-search response: %w", err)
	}
	return resp, nil
}

// renderResult joins the title, description, and any extra snippets into one
// passage the verifier reads.
func renderResult(r braveResult) string {
	var b strings.Builder
	b.WriteString(r.Title)
	if r.Description != "" {
		b.WriteString("\n")
		b.WriteString(r.Description)
	}
	for _, s := range r.ExtraSnippets {
		if s == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(s)
	}
	return b.String()
}

// hostOf is the stable source id for a result: its host when known, else the
// raw URL parsed for a hostname, so the same outlet yields the same id
// component. Hostname (not Host) is used so a port is not folded into the id,
// which both keeps the host recoverable and avoids a ':' the EvidenceID would
// rewrite.
func hostOf(r braveResult) string {
	if r.MetaURL.Hostname != "" {
		return r.MetaURL.Hostname
	}
	if u, err := url.Parse(r.URL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return r.URL
}
