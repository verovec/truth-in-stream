package wiki

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
)

// cmPageLimit is the categorymembers page size for anonymous clients (the API
// caps cmlimit at 500); the rest follow via the continuation token.
const cmPageLimit = 500

// categoryProgressInterval is the default number of articles collected between
// progress log lines during the category walk. The walk is otherwise silent
// until it returns, which on a broad, deep crawl is minutes of no output; a
// bounded heartbeat lets an operator tell a healthy walk from a hung one. Tests
// override the cadence through APIClient.progressEvery.
const categoryProgressInterval = 1000

// MediaWiki namespace ids used by the crawler: main-namespace articles and the
// Category namespace (subcategory members come back tagged ns 14).
const (
	nsMain     = 0
	nsCategory = 14
)

// CategoryMember is one main-namespace article discovered while crawling a
// category: the page id to ingest and its title to fetch extracts by.
type CategoryMember struct {
	PageID int64
	Title  string
}

// CategoryMembers walks the given categories breadth-first, collecting distinct
// main-namespace articles. Subcategories are followed up to maxDepth (0 = only
// the seed categories' direct pages); page ids are deduped across the whole walk;
// the walk stops once maxPages distinct articles are collected. It follows the
// API continuation token and inherits getJSON's maxlag/Retry-After etiquette.
func (c *APIClient) CategoryMembers(ctx context.Context, categories []string, maxDepth, maxPages int) ([]CategoryMember, error) {
	type frontier struct {
		title string
		depth int
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	interval := c.progressEvery
	if interval <= 0 {
		interval = categoryProgressInterval
	}
	nextProgressAt := interval

	seenPages := make(map[int64]struct{})
	seenCats := make(map[string]struct{})
	var out []CategoryMember

	queue := make([]frontier, 0, len(categories))
	for _, cat := range categories {
		if _, ok := seenCats[cat]; ok {
			continue
		}
		seenCats[cat] = struct{}{}
		queue = append(queue, frontier{title: cat, depth: 0})
	}

	for len(queue) > 0 && len(out) < maxPages {
		f := queue[0]
		queue = queue[1:]

		if f.depth == 0 {
			logger.InfoContext(ctx, "crawl walking seed category",
				slog.String("project", c.Corpus), slog.String("category", f.title))
		}

		pages, subcats, err := c.categoryMembersOf(ctx, f.title)
		if err != nil {
			return nil, err
		}
		for _, p := range pages {
			if len(out) >= maxPages {
				break
			}
			if _, ok := seenPages[p.PageID]; ok {
				continue
			}
			seenPages[p.PageID] = struct{}{}
			out = append(out, p)
			if len(out) >= nextProgressAt {
				logger.InfoContext(ctx, "crawl enumerating category members",
					slog.String("project", c.Corpus),
					slog.Int("pages", len(out)),
					slog.Int("depth", f.depth),
					slog.Int("frontier", len(queue)))
				nextProgressAt += interval
			}
		}
		if f.depth < maxDepth {
			for _, sub := range subcats {
				if _, ok := seenCats[sub]; ok {
					continue
				}
				seenCats[sub] = struct{}{}
				queue = append(queue, frontier{title: sub, depth: f.depth + 1})
			}
		}
	}
	return out, nil
}

// categoryMembersOf reads every member of one category, following continuation,
// splitting them into main-namespace pages and subcategory titles.
func (c *APIClient) categoryMembersOf(ctx context.Context, category string) (pages []CategoryMember, subcats []string, err error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("list", "categorymembers")
	params.Set("cmtitle", category)
	params.Set("cmtype", "page|subcat")
	params.Set("cmprop", "ids|title|type")
	params.Set("cmlimit", strconv.Itoa(cmPageLimit))

	for {
		var resp cmResponse
		if err := c.getJSON(ctx, params, &resp); err != nil {
			return nil, nil, err
		}
		if resp.Error != nil {
			return nil, nil, fmt.Errorf("wiki: categorymembers api error %s: %s", resp.Error.Code, resp.Error.Info)
		}
		for _, m := range resp.Query.CategoryMembers {
			switch m.NS {
			case nsMain:
				if m.PageID > 0 {
					pages = append(pages, CategoryMember{PageID: m.PageID, Title: m.Title})
				}
			case nsCategory:
				subcats = append(subcats, m.Title)
			}
		}
		if len(resp.Continue) == 0 {
			return pages, subcats, nil
		}
		for k, v := range resp.Continue {
			params.Set(k, v)
		}
	}
}

// cmResponse mirrors the categorymembers Action API response.
type cmResponse struct {
	Continue map[string]string `json:"continue"`
	Query    struct {
		CategoryMembers []cmEntry `json:"categorymembers"`
	} `json:"query"`
	Error *apiErr `json:"error"`
}

// cmEntry is one categorymembers row. NS distinguishes an article (0) from a
// subcategory (14); the API's redundant "type" field is not decoded.
type cmEntry struct {
	PageID int64  `json:"pageid"`
	NS     int    `json:"ns"`
	Title  string `json:"title"`
}
