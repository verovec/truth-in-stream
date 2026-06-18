package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// wikiCrawler is the subset of the wiki crawl entry point the producer depends
// on, so the producer can be exercised with a fake crawl in tests without a live
// MediaWiki API or broker.
type wikiCrawler func(ctx context.Context, logger *slog.Logger, src wiki.CrawlSource, pub wiki.Publisher, gate wiki.Gate, cfg wiki.CrawlConfig) (wiki.CrawlStats, error)

// wikiProducer adapts the Wikipedia category crawl to the crawlnotify.Producer
// seam so it routes through RunWithAlerts like every fleet member. It owns no
// discovery or publish logic of its own; Run is a thin call into wiki.RunCrawl
// that maps its CrawlStats onto the fleet's New/Updated/Skipped shape. The crawl
// publishes one self-contained job per kept chunk (counted as New) and drops the
// chunks the fact-checkability gate rejects (counted as Skipped); there is no
// per-chunk update at the producer, so Updated stays zero (workers upsert).
type wikiProducer struct {
	run    wikiCrawler
	logger *slog.Logger
	src    wiki.CrawlSource
	pub    wiki.Publisher
	gate   wiki.Gate
	cfg    wiki.CrawlConfig
}

// Name identifies the source in alerts.
func (wikiProducer) Name() string { return "wikipedia" }

// Scope is the categories this run ingests, the human-readable subject of the
// start and finish alerts.
func (p wikiProducer) Scope() string {
	return strings.Join(p.cfg.Categories, ", ")
}

// Run executes the crawl once and maps its outcome onto crawlnotify.Stats:
// published chunks are New, gate-dropped chunks are Skipped. The error is
// returned unchanged so RunWithAlerts can fail the run and alert.
func (p wikiProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	stats, err := p.run(ctx, p.logger, p.src, p.pub, p.gate, p.cfg)
	return crawlnotify.Stats{New: stats.Published, Skipped: stats.Dropped}, err
}
