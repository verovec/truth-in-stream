package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
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

// archiveRunner is the subset of the fact-check archive client the producer
// depends on, so the producer can be exercised with a fake run in tests without a
// live Fact Check Tools API or broker. *factcheckarchive.Client satisfies it.
type archiveRunner interface {
	Run(ctx context.Context, logger *slog.Logger, pub factcheckarchive.Publisher, cfg factcheckarchive.RunConfig) (factcheckarchive.Stats, error)
}

// factcheckProducer adapts the fact-check-archive crawl to the crawlnotify.Producer
// seam, mirroring cmd/factcheckcrawl: Run walks every configured query through the
// archive client, summing published claims (New) and dropped claims (Skipped). It
// stops at the first query that errors, returning the counts gathered so far.
type factcheckProducer struct {
	client   archiveRunner
	logger   *slog.Logger
	pub      factcheckarchive.Publisher
	queries  []string
	maxPages int
}

func (factcheckProducer) Name() string { return "factcheck" }

func (p factcheckProducer) Scope() string { return strings.Join(p.queries, ", ") }

func (p factcheckProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	var total crawlnotify.Stats
	for _, query := range p.queries {
		stats, err := p.client.Run(ctx, p.logger, p.pub, factcheckarchive.RunConfig{
			Query:    query,
			MaxPages: p.maxPages,
		})
		total.New += stats.Published
		total.Skipped += stats.Skipped
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
