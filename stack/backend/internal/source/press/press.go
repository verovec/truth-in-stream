// Package press retrieves quote evidence for attribution claims ("Z said ...")
// from the open web's press and transcript coverage. No structured store can
// answer "who said what": the corpus is open-ended and huge, so attribution
// claims are answered by a live search over published press, returning passages
// that carry the quote in context with the outlet's name and the publication
// date a reader can verify against.
//
// It wraps the same Brave Search REST API as internal/source/websearch (provider
// decision recorded there: an independent first-party index with documented
// French support and a stable JSON v1 endpoint), but is a distinct pack: it
// advertises source.KindAttribution so the routing layer (card J) selects it for
// attribution claims, and it shapes each result as a press citation - outlet name
// as the source, the article's publication date, and the quote-bearing snippets
// as the passage - rather than a generic web result.
//
// The pack is inert until wired by the routing layer; it makes no call until
// Retrieve is invoked, and the API key comes from the environment only and is
// never logged.
package press

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

// braveBaseURL is the Brave web-search REST endpoint, the same first-party index
// the websearch pack uses.
const braveBaseURL = "https://api.search.brave.com/res/v1/web/search"

const (
	// defaultTimeout bounds a single search; attribution retrieval sits on the
	// same live fact-check latency budget as any other source.
	defaultTimeout = 6 * time.Second
	// defaultCount is how many press results to request: enough for the verifier
	// to corroborate a quote across outlets without flooding the prompt.
	defaultCount = 5
	// defaultLang / defaultCountry default the search to French press, the
	// jurisdiction this pack serves.
	defaultLang    = "fr"
	defaultCountry = "FR"
)

// Config configures the press pack. APIKey is the Brave subscription token and is
// required; it comes from the environment and is never logged. The remaining
// fields fall back to package defaults when zero, and BaseURL overrides the
// endpoint for tests.
type Config struct {
	APIKey  string
	Timeout time.Duration
	Count   int
	Lang    string
	Country string
	BaseURL string
}

// Pack retrieves French press quote passages as evidence. It satisfies
// source.Retriever.
type Pack struct {
	httpClient *http.Client
	apiKey     string
	baseURL    string
	count      int
	lang       string
	country    string
}

// New builds a press pack. It returns an error when the API key is empty, since
// the pack cannot call the search API without it; the caller fails fast at wiring
// rather than discovering it on the first claim.
func New(cfg Config) (*Pack, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("press: API key is required")
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
func (p *Pack) Kind() source.Kind { return source.KindAttribution }

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
	Age           string   `json:"age"`
	PageAge       string   `json:"page_age"`
	ExtraSnippets []string `json:"extra_snippets"`
	Profile       struct {
		Name string `json:"name"`
	} `json:"profile"`
	MetaURL struct {
		Hostname string `json:"hostname"`
	} `json:"meta_url"`
}

// Retrieve runs a French press search for the attribution claim and returns one
// quote passage per result, each carrying the outlet name and publication date as
// provenance and a stable id keyed by the result host. An empty query text
// returns no evidence rather than a useless wildcard search.
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
			ID:      source.NewEvidenceID(source.KindAttribution, hostOf(r), len(out)),
			Passage: renderResult(r),
			Source: source.Source{
				Name: outletName(r),
				URL:  r.URL,
				Date: publishedDate(r),
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
		return braveResponse{}, fmt.Errorf("building press-search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	httpResp, err := p.httpClient.Do(req)
	if err != nil {
		return braveResponse{}, fmt.Errorf("running press search: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusOK {
		return braveResponse{}, fmt.Errorf("running press search: status %d", httpResp.StatusCode)
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return braveResponse{}, fmt.Errorf("reading press-search response: %w", err)
	}
	var resp braveResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return braveResponse{}, fmt.Errorf("decoding press-search response: %w", err)
	}
	return resp, nil
}

// renderResult joins the headline, lede, and any quote-bearing snippets into one
// passage the verifier reads for the attributed quote in context.
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

// outletName is the press provenance shown for a quote: the publisher profile
// name when the index carries it, else the result host so a citation always names
// a source.
func outletName(r braveResult) string {
	if r.Profile.Name != "" {
		return r.Profile.Name
	}
	return hostOf(r)
}

// publishedDate prefers the article's page_age (a full publication timestamp)
// over the coarser age the index reports for the result, so a quote carries the
// most precise date available.
func publishedDate(r braveResult) string {
	if r.PageAge != "" {
		return r.PageAge
	}
	return r.Age
}

// hostOf is the stable source id for a result: its host when known, else the raw
// URL parsed for a hostname, so the same outlet yields the same id component.
// Hostname (not Host) is used so a port is not folded into the id, which both
// keeps the host recoverable and avoids a ':' the EvidenceID would rewrite.
func hostOf(r braveResult) string {
	if r.MetaURL.Hostname != "" {
		return r.MetaURL.Hostname
	}
	if u, err := url.Parse(r.URL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return r.URL
}
