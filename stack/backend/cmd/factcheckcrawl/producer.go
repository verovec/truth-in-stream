package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
)

// streamRunner is the subset of the fact-check archive client the producer depends
// on, so the producer can be exercised with a fake run in tests without a live Fact
// Check Tools API or broker. *factcheckarchive.Client satisfies it.
type streamRunner interface {
	RunStreams(ctx context.Context, logger *slog.Logger, pub factcheckarchive.Publisher, streams []factcheckarchive.RunConfig, cp factcheckarchive.StreamCheckpoint) (factcheckarchive.Stats, error)
}

// factcheckProducer adapts the broadened fact-check-archive crawl to the
// crawlnotify.Producer seam so it routes through RunWithAlerts like every fleet
// member. Run walks every configured stream (topic queries + publisher-scoped
// queries) through the checkpointed RunStreams, then clears the checkpoint on full
// success so the next scheduled run starts fresh. Each published claim job is New; a
// claim dropped for lacking a mappable verdict is Skipped; the worker upserts, so
// Updated stays zero.
type factcheckProducer struct {
	client     streamRunner
	logger     *slog.Logger
	pub        factcheckarchive.Publisher
	streams    []factcheckarchive.RunConfig
	checkpoint factcheckarchive.StreamCheckpoint
}

// Name identifies the source in alerts.
func (factcheckProducer) Name() string { return "factcheck" }

// Scope is the human-readable subject of the start and finish alerts: the number of
// topic streams and publisher-scoped streams this run walks.
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

// Run walks every stream through the checkpointed runner, reports the summed counts,
// and clears the checkpoint once every stream has drained. On a stream error it
// returns the counts gathered so far and the error, leaving the checkpoint holding
// the completed streams so a rerun resumes where it stopped.
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
