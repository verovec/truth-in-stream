// Package example is the compile-checked template connector: the minimal,
// self-contained producer a new source is copied from. It demonstrates the
// source-adapter recipe - a producer package that implements crawlnotify.Producer
// and publishes the generic connector.EvidenceJob through a transport-free
// Publisher port.
//
// It is deliberately kept OUT of the live connector registry (connector.All) and
// out of docker-compose.ingest.yml, so no operator action - not the scheduler, not
// scripts/ingest-host.sh - can run it against a real environment. As defense in
// depth it also publishes to a dedicated queue (config.LoadExampleQueue, base
// "example.evidence") that no production worker drains, so even hand-running the
// cmd/examplecrawl binary cannot reach the live crawl.chunks queue or the
// production evidence_chunks corpus. It exists purely as a copyable, buildable
// reference; connector_test validates a test-only descriptor for it to prove the
// registry entry a real source adds is well-formed.
package example

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/verovec/truth-in-stream/backend/internal/connector"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// sourceName is the connector name and the evidence Source the synthesized chunks
// land under. A real source uses its own stable name here and in its descriptor.
const sourceName = "example"

// Publisher is the transport-free port the producer publishes job bodies through,
// so the package never imports the broker. The cmd layer adapts a queue client to
// it, exactly as the other producers do.
type Publisher interface {
	Publish(ctx context.Context, body []byte, priority uint8) error
}

// Config tunes one run. Label is the human-readable scope shown in the run alerts
// (the EXAMPLE_LABEL a real source would forward); MaxItems bounds how many
// placeholder chunks the run publishes; MaxPriority is the queue's configured
// priority ceiling the jobs are published at.
type Config struct {
	Label       string
	MaxItems    int
	MaxPriority uint8
}

// Producer publishes a bounded set of placeholder evidence chunks as the generic
// connector.EvidenceJob, implementing crawlnotify.Producer so it runs through the
// shared scheduler and alert wiring exactly like a real source would.
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

// Name identifies the source in the run alerts. It matches the template
// descriptor's Name.
func (p *Producer) Name() string { return sourceName }

// Scope is the human-readable description of what this run ingests.
func (p *Producer) Scope() string {
	if p.cfg.Label == "" {
		return "example chunks"
	}
	return p.cfg.Label
}

// Run publishes MaxItems self-contained placeholder EvidenceJobs and reports how
// many were published as New. It honors cancellation between items so a shutdown
// stops the run promptly, and validates each job before publishing so the template
// shows the same guard a real producer applies.
func (p *Producer) Run(ctx context.Context) (crawlnotify.Stats, error) {
	var stats crawlnotify.Stats
	for i := range p.cfg.MaxItems {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		job := connector.EvidenceJob{
			Source:     sourceName,
			ExternalID: fmt.Sprintf("item-%d", i+1),
			ChunkIndex: 0,
			Title:      fmt.Sprintf("Example item %d", i+1),
			URL:        fmt.Sprintf("https://example.test/item/%d", i+1),
			Content:    fmt.Sprintf("Placeholder evidence chunk %d for %s.", i+1, p.Scope()),
			Kind:       string(domain.EvidenceKindLead),
			Metadata:   map[string]any{"template": true},
		}
		if err := job.Validate(); err != nil {
			return stats, fmt.Errorf("example: invalid item %d: %w", i+1, err)
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
