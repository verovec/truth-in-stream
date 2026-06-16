package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// crawlExtractBatch is how many titles are fetched per extracts request; it
// matches the TextExtracts exlimit ceiling so every page gets its text in one
// round trip.
const crawlExtractBatch = extractsBatchMax

// CrawlSource is the MediaWiki surface the crawl producer reads: the category
// walk plus lead and (optionally) full-body extracts. *APIClient satisfies it.
type CrawlSource interface {
	CategoryMembers(ctx context.Context, categories []string, maxDepth, maxPages int) ([]CategoryMember, error)
	Extracts(ctx context.Context, titles []string) ([]Extract, error)
	FullExtracts(ctx context.Context, titles []string) ([]Extract, error)
}

// CrawlConfig tunes a crawl run. Corpus is the provenance tag stored on every
// chunk; Project is the wiki project used to build article URLs; MaxPriority
// bounds the per-kind priority; IncludeBody adds kind=body chunks.
type CrawlConfig struct {
	Categories  []string
	Corpus      string
	Project     string
	MaxDepth    int
	MaxPages    int
	IncludeBody bool
	MaxPriority uint8
}

// CrawlStats summarizes a completed crawl run.
type CrawlStats struct {
	Pages     int
	Published int
}

// priorityForKind maps a chunk kind onto the queue's priority band: a lead is an
// article's summary and its highest-value evidence, so it embeds first at the
// ceiling; body prose follows at half the ceiling. An unknown kind floors to zero.
func priorityForKind(kind domain.WikiChunkKind, maxPriority uint8) uint8 {
	switch kind {
	case domain.WikiChunkKindLead:
		return maxPriority
	case domain.WikiChunkKindBody:
		return maxPriority / 2
	default:
		return 0
	}
}

// RunCrawl walks the configured categories, fetches each article's lead (and,
// when IncludeBody is set, its body), chunks them with the shared chunker, and
// publishes one self-contained CrawlJob per chunk. Chunk indices are a single
// contiguous space per page (lead chunks first, then body), so the
// (page_id, chunk_index) primary key is stable across re-crawls. It needs no
// database: every field a live row requires travels in the message. A nil logger
// falls back to slog.Default.
func RunCrawl(ctx context.Context, logger *slog.Logger, src CrawlSource, pub Publisher, cfg CrawlConfig) (CrawlStats, error) {
	if cfg.MaxPriority < 1 {
		return CrawlStats{}, fmt.Errorf("wiki: crawl needs a positive max priority, got %d", cfg.MaxPriority)
	}
	if cfg.Corpus == "" {
		return CrawlStats{}, fmt.Errorf("wiki: crawl needs a corpus tag")
	}
	if logger == nil {
		logger = slog.Default()
	}

	members, err := src.CategoryMembers(ctx, cfg.Categories, cfg.MaxDepth, cfg.MaxPages)
	if err != nil {
		return CrawlStats{}, fmt.Errorf("wiki: crawl categories: %w", err)
	}
	logger.InfoContext(ctx, "crawl collected category members",
		slog.String("corpus", cfg.Corpus), slog.Int("pages", len(members)))

	stats := CrawlStats{Pages: len(members)}
	for start := 0; start < len(members); start += crawlExtractBatch {
		end := min(start+crawlExtractBatch, len(members))
		titles := make([]string, 0, end-start)
		for _, m := range members[start:end] {
			titles = append(titles, m.Title)
		}

		leads, err := src.Extracts(ctx, titles)
		if err != nil {
			return stats, fmt.Errorf("wiki: crawl lead extracts: %w", err)
		}
		full := map[string]Extract{}
		if cfg.IncludeBody {
			fulls, err := src.FullExtracts(ctx, titles)
			if err != nil {
				return stats, fmt.Errorf("wiki: crawl body extracts: %w", err)
			}
			for _, f := range fulls {
				full[f.Title] = f
			}
		}

		for _, lead := range leads {
			if lead.Missing {
				continue
			}
			published, err := publishPageChunks(ctx, pub, cfg, lead, full[lead.Title])
			if err != nil {
				return stats, err
			}
			stats.Published += published
		}
		logger.InfoContext(ctx, "crawl published page batch",
			slog.String("corpus", cfg.Corpus), slog.Int("published", stats.Published))
	}
	return stats, nil
}

// publishPageChunks chunks one page's lead and body and publishes a CrawlJob per
// chunk, assigning contiguous chunk indices (lead first). The revision id comes
// from the lead extract, falling back to the body extract.
func publishPageChunks(ctx context.Context, pub Publisher, cfg CrawlConfig, lead, full Extract) (int, error) {
	revID := lead.RevisionID
	if revID == 0 {
		revID = full.RevisionID
	}
	articleURL := pageURL(cfg.Project, lead.Title)

	var (
		idx       int
		published int
	)
	emit := func(content string, kind domain.WikiChunkKind) error {
		job := crawljob.CrawlJob{
			PageID: lead.PageID, ChunkIndex: idx, Title: lead.Title, URL: articleURL,
			RevisionID: revID, Corpus: cfg.Corpus, Content: content, Section: "", Kind: string(kind),
		}
		body, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("wiki: encode crawl job page %d chunk %d: %w", lead.PageID, idx, err)
		}
		if err := pub.Publish(ctx, body, priorityForKind(kind, cfg.MaxPriority)); err != nil {
			return fmt.Errorf("wiki: publish crawl job page %d chunk %d: %w", lead.PageID, idx, err)
		}
		idx++
		published++
		return nil
	}

	for _, content := range Chunk(lead.Title, lead.Text) {
		if err := emit(content, domain.WikiChunkKindLead); err != nil {
			return published, err
		}
	}
	if cfg.IncludeBody {
		for _, content := range Chunk(lead.Title, bodyText(full.Text, lead.Text)) {
			if err := emit(content, domain.WikiChunkKindBody); err != nil {
				return published, err
			}
		}
	}
	return published, nil
}

// bodyText returns the article body with the lead stripped from the front so the
// lead is not embedded twice. When the lead is not a clean prefix of the full
// text (rare formatting differences), it returns the full text unchanged.
func bodyText(full, lead string) string {
	full = strings.TrimSpace(full)
	lead = strings.TrimSpace(lead)
	if lead != "" && strings.HasPrefix(full, lead) {
		return strings.TrimSpace(full[len(lead):])
	}
	return full
}
