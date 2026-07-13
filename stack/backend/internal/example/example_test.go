package example

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawljob"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// capturePublisher records every published body so a test can assert the wire
// shape and count.
type capturePublisher struct {
	bodies [][]byte
	err    error
}

func (c *capturePublisher) Publish(_ context.Context, body []byte, _ uint8) error {
	if c.err != nil {
		return c.err
	}
	c.bodies = append(c.bodies, append([]byte(nil), body...))
	return nil
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, discardLogger(), Config{MaxItems: 1}); err == nil {
		t.Error("New with nil publisher = nil error, want error")
	}
	if _, err := New(&capturePublisher{}, discardLogger(), Config{MaxItems: 0}); err == nil {
		t.Error("New with zero MaxItems = nil error, want error")
	}
}

func TestRunPublishesValidCrawlJobs(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	p, err := New(pub, discardLogger(), Config{Label: "demo", MaxItems: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 3 {
		t.Fatalf("stats.New = %d, want 3", stats.New)
	}
	if len(pub.bodies) != 3 {
		t.Fatalf("published %d bodies, want 3", len(pub.bodies))
	}
	// Every body must be a crawljob.CrawlJob the existing crawl worker accepts,
	// so the example reuses the worker with no new consumer.
	for i, body := range pub.bodies {
		var job crawljob.CrawlJob
		if err := json.Unmarshal(body, &job); err != nil {
			t.Fatalf("body %d not a CrawlJob: %v", i, err)
		}
		if job.Corpus != sourceName || job.Content == "" || job.PageID <= 0 {
			t.Errorf("body %d invalid: %+v", i, job)
		}
	}
}

func TestRunPropagatesPublishError(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{err: errors.New("broker down")}
	p, err := New(pub, discardLogger(), Config{MaxItems: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.Run(t.Context()); err == nil {
		t.Fatal("Run with a failing publisher = nil error, want error")
	}
}

func TestRunStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	pub := &capturePublisher{}
	p, err := New(pub, discardLogger(), Config{MaxItems: 5})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Run(ctx); err == nil {
		t.Fatal("Run with a canceled context = nil error, want error")
	}
	if len(pub.bodies) != 0 {
		t.Errorf("published %d bodies after cancel, want 0", len(pub.bodies))
	}
}
