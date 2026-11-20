package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/datacommons"
)

// feedRunner is the subset of the DataCommons client the producer depends on, so
// the producer can be exercised with a fake run in tests without a live feed or
// broker. *datacommons.Client satisfies it.
type feedRunner interface {
	Run(ctx context.Context, logger *slog.Logger, pub datacommons.Publisher) (datacommons.Stats, error)
}

// datacommonsProducer adapts the DataCommons feed crawl to the crawlnotify.Producer
// seam so it routes through RunWithAlerts like every fleet member. Run is a thin
// call into the feed client that maps its Stats onto the fleet's New/Skipped shape
// (published claims are New, dropped records are Skipped; the worker upserts, so
// Updated stays zero).
type datacommonsProducer struct {
	client  feedRunner
	logger  *slog.Logger
	pub     datacommons.Publisher
	outlets []string
}

// Name identifies the source in alerts and matches the connector descriptor.
func (datacommonsProducer) Name() string { return "datacommons" }

// Scope is the human-readable subject of the start/finish alerts: the outlet
// allowlist size, or "all outlets" when unfiltered.
func (p datacommonsProducer) Scope() string {
	if len(p.outlets) == 0 {
		return "all outlets"
	}
	return strings.Join(p.outlets, ", ") + " (" + strconv.Itoa(len(p.outlets)) + " outlets)"
}

// Run ingests the feed once and reports the counts. Published claims are New and
// dropped records are Skipped.
func (p datacommonsProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	stats, err := p.client.Run(ctx, p.logger, p.pub)
	return crawlnotify.Stats{New: stats.Published, Skipped: stats.Skipped}, err
}
