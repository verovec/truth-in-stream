package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/claimreviewsite"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// outletReader is the subset of the ClaimReview reader the producer depends on, so
// it can be exercised with a fake run in tests. *claimreviewsite.Client satisfies it.
type outletReader interface {
	Run(ctx context.Context, logger *slog.Logger, pub claimreviewsite.Publisher) (claimreviewsite.Stats, error)
}

// claimreviewProducer adapts the ClaimReview JSON-LD outlet reader to the
// crawlnotify.Producer seam so it routes through RunWithAlerts like every fleet
// member. Published records are New; dropped records (missing fields, restrictive
// licence, robots-disallowed) are Skipped; the worker upserts, so Updated stays zero.
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
