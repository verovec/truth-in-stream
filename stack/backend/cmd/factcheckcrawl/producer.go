package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
)

// archiveRunner is the subset of the fact-check archive client the producer
// depends on, so the producer can be exercised with a fake run in tests without a
// live Fact Check Tools API or broker.
type archiveRunner interface {
	Run(ctx context.Context, logger *slog.Logger, pub factcheckarchive.Publisher, cfg factcheckarchive.RunConfig) (factcheckarchive.Stats, error)
}

// factcheckProducer adapts the fact-check-archive crawl to the
// crawlnotify.Producer seam so it routes through RunWithAlerts like every fleet
// member. It owns no API or publish logic of its own; Run walks the configured
// queries through factcheckarchive.Run (preserving the per-query pagination
// exactly) and sums their outcomes onto the fleet's New/Updated/Skipped shape.
// Each published claim job is New; a claim dropped for lacking a mappable verdict
// is Skipped; there is no per-claim update at the producer, so Updated stays zero
// (the worker upserts).
type factcheckProducer struct {
	client   archiveRunner
	logger   *slog.Logger
	pub      factcheckarchive.Publisher
	queries  []string
	maxPages int
}

// Name identifies the source in alerts.
func (factcheckProducer) Name() string { return "factcheck" }

// Scope is the queries this run ingests, the human-readable subject of the start
// and finish alerts.
func (p factcheckProducer) Scope() string {
	return strings.Join(p.queries, ", ")
}

// Run walks every configured query, publishing its claim jobs, and reports the
// summed counts. It stops at the first query that errors, returning the counts
// gathered so far and the error unchanged so RunWithAlerts can fail the run and
// alert.
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
