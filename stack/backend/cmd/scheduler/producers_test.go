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

// fakeArchive is a fake archiveRunner that records each query and returns a fixed
// per-query result, optionally erroring on a chosen query to exercise the
// stop-at-first-error path.
type fakeArchive struct {
	seen    []string
	stats   factcheckarchive.Stats
	errOn   string
	failErr error
}

func (f *fakeArchive) Run(_ context.Context, _ *slog.Logger, _ factcheckarchive.Publisher, cfg factcheckarchive.RunConfig) (factcheckarchive.Stats, error) {
	f.seen = append(f.seen, cfg.Query)
	if cfg.Query == f.errOn {
		return f.stats, f.failErr
	}
	return f.stats, nil
}

func TestFactcheckProducerSumsQueries(t *testing.T) {
	t.Parallel()
	archive := &fakeArchive{stats: factcheckarchive.Stats{Published: 4, Skipped: 1}}
	producer := factcheckProducer{
		client:  archive,
		queries: []string{"macron", "le pen", "melenchon"},
	}

	if got := producer.Name(); got != "factcheck" {
		t.Fatalf("Name() = %q, want factcheck", got)
	}
	if got := producer.Scope(); got != "macron, le pen, melenchon" {
		t.Fatalf("Scope() = %q, want the joined queries", got)
	}

	stats, err := producer.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Three queries, each 4 published + 1 skipped.
	if stats.New != 12 || stats.Skipped != 3 {
		t.Fatalf("stats = %+v, want {New:12 Skipped:3}", stats)
	}
	if len(archive.seen) != 3 {
		t.Fatalf("ran %d queries, want 3", len(archive.seen))
	}
}

func TestFactcheckProducerStopsAtFirstError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("api down")
	archive := &fakeArchive{
		stats:   factcheckarchive.Stats{Published: 5},
		errOn:   "le pen",
		failErr: wantErr,
	}
	producer := factcheckProducer{
		client:  archive,
		queries: []string{"macron", "le pen", "melenchon"},
	}

	stats, err := producer.Run(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	// macron (5) then le pen (5, errors); melenchon never runs.
	if stats.New != 10 {
		t.Fatalf("stats.New = %d, want 10 (stopped after the failing query)", stats.New)
	}
	if len(archive.seen) != 2 {
		t.Fatalf("ran %d queries, want 2 (stopped at the error)", len(archive.seen))
	}
}

// Compile-time checks that the producers satisfy the fleet seam.
var (
	_ crawlnotify.Producer = wikiProducer{}
	_ crawlnotify.Producer = factcheckProducer{}
)
