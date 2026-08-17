package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

func TestWikiProducerMapsStats(t *testing.T) {
	t.Parallel()
	producer := wikiProducer{
		run: func(context.Context, *slog.Logger, wiki.CrawlSource, wiki.Publisher, wiki.Gate, wiki.CrawlConfig) (wiki.CrawlStats, error) {
			return wiki.CrawlStats{Published: 7, Dropped: 3}, nil
		},
		cfg: wiki.CrawlConfig{Categories: []string{"Category:Physics", "Category:Chemistry"}},
	}

	if got := producer.Name(); got != "wikipedia" {
		t.Fatalf("Name() = %q, want wikipedia", got)
	}
	if got := producer.Scope(); got != "Category:Physics, Category:Chemistry" {
		t.Fatalf("Scope() = %q, want the joined categories", got)
	}

	stats, err := producer.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 7 || stats.Skipped != 3 || stats.Updated != 0 {
		t.Fatalf("stats = %+v, want {New:7 Skipped:3 Updated:0}", stats)
	}
}

func TestWikiProducerPropagatesError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("crawl failed")
	producer := wikiProducer{
		run: func(context.Context, *slog.Logger, wiki.CrawlSource, wiki.Publisher, wiki.Gate, wiki.CrawlConfig) (wiki.CrawlStats, error) {
			return wiki.CrawlStats{Published: 2}, wantErr
		},
	}

	stats, err := producer.Run(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if stats.New != 2 {
		t.Fatalf("stats.New = %d, want the partial count 2", stats.New)
	}
}

// fakeArchive is a fake streamRunner that records how many streams it ran and
// returns a fixed aggregate result, optionally erroring to exercise the
// keep-checkpoint-on-error path.
type fakeArchive struct {
	ranStreams int
	stats      factcheckarchive.Stats
	failErr    error
}

func (f *fakeArchive) RunStreams(_ context.Context, _ *slog.Logger, _ factcheckarchive.Publisher, streams []factcheckarchive.RunConfig, _ factcheckarchive.StreamCheckpoint) (factcheckarchive.Stats, error) {
	f.ranStreams = len(streams)
	return f.stats, f.failErr
}

// clearSpy records whether Clear ran, so a test can assert the checkpoint is only
// cleared on a fully successful run.
type clearSpy struct {
	factcheckarchive.NoStreamCheckpoint
	cleared bool
}

func (c *clearSpy) Clear() error { c.cleared = true; return nil }

func TestFactcheckProducerRunsStreamsAndClears(t *testing.T) {
	t.Parallel()
	archive := &fakeArchive{stats: factcheckarchive.Stats{Published: 12, Skipped: 3}}
	cp := &clearSpy{}
	producer := factcheckProducer{
		client:     archive,
		streams:    []factcheckarchive.RunConfig{{Query: "macron"}, {Query: "le pen"}, {PublisherSite: "lemonde.fr"}},
		checkpoint: cp,
	}

	if got := producer.Name(); got != "factcheck" {
		t.Fatalf("Name() = %q, want factcheck", got)
	}
	if got := producer.Scope(); got != "2 topics + 1 publisher streams" {
		t.Fatalf("Scope() = %q", got)
	}

	stats, err := producer.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 12 || stats.Skipped != 3 {
		t.Fatalf("stats = %+v, want {New:12 Skipped:3}", stats)
	}
	if archive.ranStreams != 3 {
		t.Fatalf("ran %d streams, want 3", archive.ranStreams)
	}
	if !cp.cleared {
		t.Fatal("checkpoint not cleared after a full successful run")
	}
}

func TestFactcheckProducerKeepsCheckpointOnError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("api down")
	archive := &fakeArchive{stats: factcheckarchive.Stats{Published: 10}, failErr: wantErr}
	cp := &clearSpy{}
	producer := factcheckProducer{
		client:     archive,
		streams:    []factcheckarchive.RunConfig{{Query: "macron"}, {Query: "le pen"}},
		checkpoint: cp,
	}

	stats, err := producer.Run(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if stats.New != 10 {
		t.Fatalf("stats.New = %d, want the partial count 10", stats.New)
	}
	if cp.cleared {
		t.Fatal("checkpoint cleared despite an error; a rerun must resume")
	}
}

// Compile-time checks that the producers satisfy the fleet seam.
var (
	_ crawlnotify.Producer = wikiProducer{}
	_ crawlnotify.Producer = factcheckProducer{}
)
