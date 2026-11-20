package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/claimreviewsite"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/datacommons"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// qPublisher adapts a queue.Client to the producer packages' Publisher port, so
// those packages never import the broker transport. It satisfies wiki.Publisher,
// factcheckarchive.Publisher, and scrutinsarchive.Publisher (one method each).
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}

// wikiCrawler is the subset of the wiki crawl entry point the producer depends
// on, so the producer can be exercised with a fake crawl in tests without a live
// MediaWiki API or broker. wiki.RunCrawl satisfies it.
type wikiCrawler func(ctx context.Context, logger *slog.Logger, src wiki.CrawlSource, pub wiki.Publisher, gate wiki.Gate, cfg wiki.CrawlConfig) (wiki.CrawlStats, error)

// wikiProducer adapts the Wikipedia category crawl to the crawlnotify.Producer
// seam, mirroring cmd/wikicrawl: Run is a thin call into the crawl func that maps
// its CrawlStats onto the fleet's New/Skipped shape (published chunks are New,
// gate-dropped chunks are Skipped; workers upsert, so Updated stays zero).
type wikiProducer struct {
	run    wikiCrawler
	logger *slog.Logger
	src    wiki.CrawlSource
	pub    wiki.Publisher
	gate   wiki.Gate
	cfg    wiki.CrawlConfig
}

func (wikiProducer) Name() string { return "wikipedia" }

func (p wikiProducer) Scope() string { return strings.Join(p.cfg.Categories, ", ") }

func (p wikiProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	stats, err := p.run(ctx, p.logger, p.src, p.pub, p.gate, p.cfg)
	return crawlnotify.Stats{New: stats.Published, Skipped: stats.Dropped}, err
}

// streamRunner is the subset of the fact-check archive client the producer depends
// on, so it can be exercised with a fake run in tests without a live Fact Check
// Tools API or broker. *factcheckarchive.Client satisfies it.
type streamRunner interface {
	RunStreams(ctx context.Context, logger *slog.Logger, pub factcheckarchive.Publisher, streams []factcheckarchive.RunConfig, cp factcheckarchive.StreamCheckpoint) (factcheckarchive.Stats, error)
}

// factcheckProducer adapts the broadened fact-check-archive crawl to the
// crawlnotify.Producer seam, mirroring cmd/factcheckcrawl: Run walks every stream
// (topic + publisher-scoped) through the checkpointed RunStreams, summing published
// (New) and dropped (Skipped) claims, and clears the checkpoint on full success.
type factcheckProducer struct {
	client     streamRunner
	logger     *slog.Logger
	pub        factcheckarchive.Publisher
	streams    []factcheckarchive.RunConfig
	checkpoint factcheckarchive.StreamCheckpoint
}

func (factcheckProducer) Name() string { return "factcheck" }

func (p factcheckProducer) Scope() string {
	topics, sites := 0, 0
	for _, s := range p.streams {
		if s.PublisherSite != "" {
			sites++
		} else {
			topics++
		}
	}
	return fmt.Sprintf("%d topics + %d publisher streams", topics, sites)
}

func (p factcheckProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	cp := p.checkpoint
	if cp == nil {
		cp = factcheckarchive.NoStreamCheckpoint{}
	}
	stats, err := p.client.RunStreams(ctx, p.logger, p.pub, p.streams, cp)
	total := crawlnotify.Stats{New: stats.Published, Skipped: stats.Skipped}
	if err != nil {
		return total, err
	}
	if clearErr := cp.Clear(); clearErr != nil {
		return total, fmt.Errorf("factcheck: clear checkpoint after full run: %w", clearErr)
	}
	return total, nil
}

// feedRunner is the subset of the DataCommons client the producer depends on, so
// it can be exercised with a fake run in tests. *datacommons.Client satisfies it.
type feedRunner interface {
	Run(ctx context.Context, logger *slog.Logger, pub datacommons.Publisher) (datacommons.Stats, error)
}

// datacommonsProducer adapts the DataCommons ClaimReview feed crawl to the
// crawlnotify.Producer seam, mirroring cmd/datacommonscrawl: Run ingests the feed
// once and maps its Stats onto the fleet's New/Skipped shape (published claims are
// New, dropped records are Skipped; the worker upserts, so Updated stays zero).
type datacommonsProducer struct {
	client  feedRunner
	logger  *slog.Logger
	pub     datacommons.Publisher
	outlets []string
}

func (datacommonsProducer) Name() string { return "datacommons" }

func (p datacommonsProducer) Scope() string {
	if len(p.outlets) == 0 {
		return "all outlets"
	}
	return strings.Join(p.outlets, ", ") + " (" + strconv.Itoa(len(p.outlets)) + " outlets)"
}

func (p datacommonsProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	stats, err := p.client.Run(ctx, p.logger, p.pub)
	return crawlnotify.Stats{New: stats.Published, Skipped: stats.Skipped}, err
}

// outletReader is the subset of the ClaimReview outlet reader the producer depends
// on. *claimreviewsite.Client satisfies it.
type outletReader interface {
	Run(ctx context.Context, logger *slog.Logger, pub claimreviewsite.Publisher) (claimreviewsite.Stats, error)
}

// claimreviewProducer adapts the ClaimReview JSON-LD outlet reader to the
// crawlnotify.Producer seam, mirroring cmd/claimreviewcrawl.
type claimreviewProducer struct {
	client  outletReader
	logger  *slog.Logger
	pub     claimreviewsite.Publisher
	outlets int
}

func (claimreviewProducer) Name() string { return "claimreview" }

func (p claimreviewProducer) Scope() string {
	return fmt.Sprintf("%d allowlisted outlets", p.outlets)
}

func (p claimreviewProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	stats, err := p.client.Run(ctx, p.logger, p.pub)
	return crawlnotify.Stats{New: stats.Published, Skipped: stats.Skipped}, err
}
