package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/verovec/truth-in-stream/backend/internal/claimskg"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// claimskgProducer adapts the one-shot ClaimsKG seed to the crawlnotify.Producer
// seam so it routes through RunWithAlerts. It opens the seed file and streams it into
// the seeder. Published rows are New; skipped rows are Skipped.
type claimskgProducer struct {
	seeder   *claimskg.Client
	logger   *slog.Logger
	pub      claimskg.Publisher
	seedFile string
	vintage  string
}

func (claimskgProducer) Name() string { return "claimskg" }

func (p claimskgProducer) Scope() string {
	return fmt.Sprintf("ClaimsKG %s seed from %s", p.vintage, p.seedFile)
}

func (p claimskgProducer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	f, err := os.Open(p.seedFile)
	if err != nil {
		return crawlnotify.Stats{}, fmt.Errorf("claimskg: open seed file: %w", err)
	}
	defer func() { _ = f.Close() }()
	stats, err := p.seeder.Run(ctx, p.logger, p.pub, f)
	return crawlnotify.Stats{New: stats.Published, Skipped: stats.Skipped}, err
}
