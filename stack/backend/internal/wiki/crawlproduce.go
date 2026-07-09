package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// crawlExtractBatch is how many titles are fetched per extracts request; it
// matches the TextExtracts exlimit ceiling so every page gets its text in one
// round trip.
const crawlExtractBatch = extractsBatchMax

// secondsPerMinute converts the gate's requests-per-minute cap into the
// per-second rate the limiter expects.
const secondsPerMinute = 60.0

// ShardCategories selects one shard's slice of categories by round-robin
// position: category i belongs to shard (i mod shards). With shards <= 1 the
// whole list is returned (sharding off). The union of shards 0..shards-1 is
// exactly the input with no overlap, so N producers each given a distinct index
// crawl disjoint categories and never duplicate API work or gate spend. index is
// expected in [0, shards); the config loader validates that, and an out-of-range
// index simply yields an empty slice here. A shard with more shards than
// categories may legitimately be empty.
func ShardCategories(categories []string, shards, index int) []string {
	if shards <= 1 {
		return categories
	}
	out := make([]string, 0, len(categories)/shards+1)
	for i, c := range categories {
		if i%shards == index {
			out = append(out, c)
		}
	}
	return out
}

// CrawlSource is the MediaWiki surface the crawl producer reads: the category
// walk plus lead and (optionally) full-body extracts. *APIClient satisfies it.
type CrawlSource interface {
	CategoryMembers(ctx context.Context, categories []string, maxDepth, maxPages int) ([]CategoryMember, error)
	Extracts(ctx context.Context, titles []string) ([]Extract, error)
	FullExtracts(ctx context.Context, titles []string) ([]Extract, error)
}

// CrawlConfig tunes a crawl run. Corpus is the provenance tag stored on every
// chunk; Project is the wiki project used to build article URLs; MaxPriority
// bounds the per-kind priority; IncludeBody adds kind=body chunks. GateConcurrency
// caps in-flight fact-checkability judgments and GateRPM (0 = unpaced) caps their
// rate; both apply only when RunCrawl is given a non-nil Gate.
type CrawlConfig struct {
	Categories      []string
	Corpus          string
	Project         string
	MaxDepth        int
	MaxPages        int
	IncludeBody     bool
	MaxPriority     uint8
	GateConcurrency int
	GateRPM         int
}

// CrawlStats summarizes a completed crawl run. Dropped counts chunks the
// fact-checkability gate rejected before publishing (always zero when no gate is
// configured).
type CrawlStats struct {
	Pages     int
	Published int
	Dropped   int
}

// Gate judges whether a chunk's passage is fact-checkable evidence worth
// embedding. The crawl producer publishes only chunks the gate passes; a chunk
// the gate judges not fact-checkable is dropped before it reaches the broker, so
// no downstream embedding spend is wasted on non-evidence. A nil Gate disables
// the gate entirely (every chunk is published, the pre-gate behavior). The
// Anthropic-backed implementation lives in internal/evidencegate.
type Gate interface {
	FactCheckable(ctx context.Context, passage string) (bool, error)
}

// priorityForKind maps a chunk kind onto the queue's priority band: a lead is an
// article's summary and its highest-value evidence, so it embeds first at the
// ceiling; body prose follows at half the ceiling. An unknown kind floors to zero.
func priorityForKind(kind domain.EvidenceChunkKind, maxPriority uint8) uint8 {
	switch kind {
	case domain.EvidenceKindLead:
		return maxPriority
	case domain.EvidenceKindBody:
		return maxPriority / 2
	default:
		return 0
	}
}

// RunCrawl walks the configured categories, fetches each article's lead (and,
// when IncludeBody is set, its body), chunks them with the shared chunker, and
// publishes one self-contained CrawlJob per chunk. When gate is non-nil, each
// chunk is judged for fact-checkability after chunking and before publishing:
// chunks the gate rejects are dropped (and counted in CrawlStats.Dropped) so only
// citable evidence reaches the broker; a gate error degrades fail-open (the chunk
// is published and the error logged) so a flaky model never empties the corpus. A
// nil gate publishes every chunk. Chunk indices are assigned by position within a
// page (lead chunks first, then body) before gating, so a chunk keeps the same
// (page_id, chunk_index) primary key across re-crawls even as the gate drops a
// different subset (the published indices may therefore have gaps, but never
// shift). It needs no database: every field a live row requires travels in the
// message. A nil logger falls back to slog.Default.
func RunCrawl(ctx context.Context, logger *slog.Logger, src CrawlSource, pub Publisher, gate Gate, cfg CrawlConfig) (CrawlStats, error) {
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

		var jobs []crawlMessage
		for _, lead := range leads {
			if lead.Missing {
				continue
			}
			pageJobs, err := pageChunkJobs(cfg, lead, full[lead.Title])
			if err != nil {
				return stats, err
			}
			jobs = append(jobs, pageJobs...)
		}

		if gate != nil {
			kept, dropped, err := gateChunks(ctx, logger, gate, jobs, cfg.GateConcurrency, cfg.GateRPM)
			if err != nil {
				return stats, err
			}
			stats.Dropped += dropped
			jobs = kept
		}

		published, err := publishMessages(ctx, pub, jobs)
		stats.Published += published
		if err != nil {
			return stats, err
		}
		logger.InfoContext(ctx, "crawl published page batch",
			slog.String("corpus", cfg.Corpus),
			slog.Int("published", stats.Published),
			slog.Int("dropped", stats.Dropped))
	}
	return stats, nil
}

// gateChunks judges each chunk's passage for fact-checkability with up to
// concurrency calls in flight, returning the chunks to publish and how many were
// dropped. A chunk the gate judges not fact-checkable is dropped; a chunk whose
// gate call errors is kept (fail-open) and the error logged, so a flaky model
// never silently empties the corpus. When rpm > 0 the gate-call rate is capped to
// bound Anthropic spend. A canceled context aborts the run rather than
// publishing a partially-gated batch.
func gateChunks(ctx context.Context, logger *slog.Logger, gate Gate, msgs []crawlMessage, concurrency, rpm int) ([]crawlMessage, int, error) {
	if len(msgs) == 0 {
		return msgs, 0, nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	var limiter *rate.Limiter
	if rpm > 0 {
		// Burst = concurrency, so the configured number of judgments can actually
		// run in parallel; the token-bucket refill at rpm/60 per second still caps
		// the long-run rate. A burst of 1 would serialize every call regardless of
		// concurrency, throttling throughput far below the rate the operator set.
		limiter = rate.NewLimiter(rate.Limit(float64(rpm)/secondsPerMinute), concurrency)
	}

	keep := make([]bool, len(msgs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)
	for i, m := range msgs {
		g.Go(func() error {
			if limiter != nil {
				if err := limiter.Wait(gctx); err != nil {
					return fmt.Errorf("wiki: gate rate wait: %w", err)
				}
			}
			ok, err := gate.FactCheckable(gctx, m.passage)
			if err != nil {
				if gctx.Err() != nil {
					return fmt.Errorf("wiki: gate canceled: %w", gctx.Err())
				}
				logger.WarnContext(gctx, "fact-checkability gate errored, publishing chunk (fail-open)",
					slog.Any("err", err))
				keep[i] = true
				return nil
			}
			keep[i] = ok
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, 0, err
	}

	kept := make([]crawlMessage, 0, len(msgs))
	dropped := 0
	for i, m := range msgs {
		if keep[i] {
			kept = append(kept, m)
			continue
		}
		dropped++
	}
	return kept, dropped, nil
}

// crawlMessage is one ready-to-publish chunk job: its marshaled body, the queue
// priority for its kind, and the raw passage text the fact-checkability gate
// judges (carried alongside the body so the gate never re-decodes the job).
type crawlMessage struct {
	body     []byte
	priority uint8
	passage  string
}

// pageChunkJobs chunks one page's lead and body into ready-to-publish CrawlJobs,
// assigning contiguous chunk indices (lead first, then body) so the
// (page_id, chunk_index) primary key is stable. The revision id comes from the
// lead extract, falling back to the body extract.
func pageChunkJobs(cfg CrawlConfig, lead, full Extract) ([]crawlMessage, error) {
	revID := lead.RevisionID
	if revID == 0 {
		revID = full.RevisionID
	}
	articleURL := pageURL(cfg.Project, lead.Title)

	contents := make([]chunkContent, 0)
	for _, c := range Chunk(lead.Title, lead.Text) {
		contents = append(contents, chunkContent{text: c, kind: domain.EvidenceKindLead})
	}
	if cfg.IncludeBody {
		for _, c := range Chunk(lead.Title, bodyText(full.Text, lead.Text)) {
			contents = append(contents, chunkContent{text: c, kind: domain.EvidenceKindBody})
		}
	}

	jobs := make([]crawlMessage, 0, len(contents))
	for idx, c := range contents {
		job := crawljob.CrawlJob{
			PageID: lead.PageID, ChunkIndex: idx, Title: lead.Title, URL: articleURL,
			RevisionID: revID, Corpus: cfg.Corpus, Content: c.text, Section: "", Kind: string(c.kind),
		}
		body, err := json.Marshal(job)
		if err != nil {
			return nil, fmt.Errorf("wiki: encode crawl job page %d chunk %d: %w", lead.PageID, idx, err)
		}
		jobs = append(jobs, crawlMessage{body: body, priority: priorityForKind(c.kind, cfg.MaxPriority), passage: c.text})
	}
	return jobs, nil
}

// chunkContent pairs one chunk's text with its kind before it is turned into a
// job, so lead and body chunks share one contiguous index space.
type chunkContent struct {
	text string
	kind domain.EvidenceChunkKind
}

// publishMessages publishes a batch of chunk jobs with up to publishConcurrency
// confirms in flight, mirroring the dump producer's windowed enqueue so a
// high-latency broker is paid the round-trip in parallel rather than once per
// chunk. It returns how many were confirmed; a failed publish cancels the rest
// and fails the run (a re-run re-publishes the remainder, idempotently).
func publishMessages(ctx context.Context, pub Publisher, jobs []crawlMessage) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}
	var published atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(publishConcurrency)
	for _, j := range jobs {
		g.Go(func() error {
			if err := pub.Publish(gctx, j.body, j.priority); err != nil {
				return fmt.Errorf("wiki: publish crawl job: %w", err)
			}
			published.Add(1)
			return nil
		})
	}
	err := g.Wait()
	return int(published.Load()), err
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
