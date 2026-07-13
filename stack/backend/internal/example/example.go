// Package example is the in-tree template connector: the minimal, self-contained
// producer a new source is copied from. It demonstrates the source-adapter recipe
// end to end - a producer package that implements crawlnotify.Producer, publishes
// a self-contained job through a transport-free Publisher port, and is declared
// once in the connector registry (as "example") plus one builder in cmd/scheduler
// and one compose service (examplecrawl).
//
// It deliberately reuses the Wikipedia job shape, queue, and worker (a
// crawljob.CrawlJob on crawl.chunks drained by crawlworker), so it needs no new
// consumer - the pattern a source that publishes an existing job shape follows.
// A source that introduces genuinely new evidence emits a connector.EvidenceJob
// instead and declares its own queue and worker. The producer synthesizes a
// bounded number of placeholder chunks; it is disabled by default and exists only
// as a copyable reference, never to ingest real data.
package example

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// sourceName is the connector name and the corpus Source the synthesized chunks
// land under. It matches the registry descriptor's Name.
const sourceName = "example"

// Publisher is the transport-free port the producer publishes job bodies through,
// so the package never imports the broker. The cmd layer adapts a queue client to
// it, exactly as the other producers do.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config tunes one run. Label is the human-readable scope shown in the run alerts
// (the EXAMPLE_LABEL the operator forwards); MaxItems bounds how many placeholder
// chunks the run publishes; MaxPriority is the queue's configured priority ceiling
// the jobs are published at.
type Config struct {
	Label       string
	MaxItems    int
	MaxPriority uint8
}

// Producer publishes a bounded set of placeholder evidence chunks, implementing
// crawlnotify.Producer so the scheduler and the shared alert wiring run it exactly
// like a real source.
type Producer struct {
	pub    Publisher
	logger *slog.Logger
	cfg    Config
}

// New builds a Producer, rejecting a nil publisher and a non-positive item bound
// so a misconfigured run fails fast instead of publishing nothing silently.
func New(pub Publisher, logger *slog.Logger, cfg Config) (*Producer, error) {
	if pub == nil {
		return nil, fmt.Errorf("example: nil publisher")
	}
	if cfg.MaxItems <= 0 {
		return nil, fmt.Errorf("example: MaxItems must be positive, got %d", cfg.MaxItems)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Producer{pub: pub, logger: logger, cfg: cfg}, nil
}

// Name identifies the source in the registry and the run alerts.
func (p *Producer) Name() string { return sourceName }

// Scope is the human-readable description of what this run ingests.
func (p *Producer) Scope() string {
	if p.cfg.Label == "" {
		return "example chunks"
	}
	return p.cfg.Label
}

// Run publishes MaxItems self-contained placeholder chunks to the crawl queue and
// reports how many were published as New. It honors cancellation between items so
// a shutdown stops the run promptly.
func (p *Producer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	var stats crawlnotify.Stats
	for i := range p.cfg.MaxItems {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		job := crawljob.CrawlJob{
			PageID:     int64(i + 1),
			ChunkIndex: 0,
			Title:      fmt.Sprintf("Example item %d", i+1),
			URL:        fmt.Sprintf("https://example.test/item/%d", i+1),
			RevisionID: 1,
			Corpus:     sourceName,
			Content:    fmt.Sprintf("Placeholder evidence chunk %d for %s.", i+1, p.Scope()),
			Kind:       string(domain.EvidenceKindLead),
		}
		body, err := json.Marshal(job)
		if err != nil {
			return stats, fmt.Errorf("example: marshal item %d: %w", i+1, err)
		}
		if err := p.pub.Publish(ctx, body, p.cfg.MaxPriority); err != nil {
			return stats, fmt.Errorf("example: publish item %d: %w", i+1, err)
		}
		stats.New++
	}
	p.logger.InfoContext(ctx, "example producer published placeholder chunks", slog.Int("count", stats.New))
	return stats, nil
}
