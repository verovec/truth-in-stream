package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/wiki"
)

// recordingNotifier captures every event handed to it, in order, so a test can
// assert the start-then-finish|fail sequence and the reported counts.
type recordingNotifier struct {
	events []crawlnotify.CrawlEvent
}

func (r *recordingNotifier) Notify(_ context.Context, e crawlnotify.CrawlEvent) error {
	r.events = append(r.events, e)
	return nil
}

func TestWikiProducerScope(t *testing.T) {
	t.Parallel()
	p := wikiProducer{cfg: wiki.CrawlConfig{Categories: []string{"Category:Physics", "Category:Chemistry"}}}
	if got := p.Name(); got != "wikipedia" {
		t.Errorf("Name() = %q, want wikipedia", got)
	}
	if got, want := p.Scope(), "Category:Physics, Category:Chemistry"; got != want {
		t.Errorf("Scope() = %q, want %q", got, want)
	}
}

func TestWikiProducerRunMapsStats(t *testing.T) {
	t.Parallel()

	var gotCfg wiki.CrawlConfig
	p := wikiProducer{
		logger: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		cfg:    wiki.CrawlConfig{Categories: []string{"Category:Physics"}, Corpus: "simplewiki-crawl", MaxPriority: 5},
		run: func(_ context.Context, _ *slog.Logger, _ wiki.CrawlSource, _ wiki.Publisher, _ wiki.Gate, cfg wiki.CrawlConfig) (wiki.CrawlStats, error) {
			gotCfg = cfg
			return wiki.CrawlStats{Pages: 7, Published: 12, Dropped: 3}, nil
		},
	}

	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (crawlnotify.Stats{New: 12, Updated: 0, Skipped: 3}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	// The crawl config is threaded through unchanged, so pagination/sharding is untouched.
	if gotCfg.Corpus != "simplewiki-crawl" || gotCfg.MaxPriority != 5 {
		t.Errorf("crawl cfg altered: %+v", gotCfg)
	}
}

func TestWikiProducerRunPropagatesError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("category walk failed")
	p := wikiProducer{
		cfg: wiki.CrawlConfig{Categories: []string{"Category:Physics"}},
		run: func(context.Context, *slog.Logger, wiki.CrawlSource, wiki.Publisher, wiki.Gate, wiki.CrawlConfig) (wiki.CrawlStats, error) {
			return wiki.CrawlStats{Published: 2, Dropped: 1}, wantErr
		},
	}
	stats, err := p.Run(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	// Even on a partial-failure run, the counts gathered so far are reported.
	if want := (crawlnotify.Stats{New: 2, Skipped: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

// TestWikiProducerThroughRunWithAlerts is the end-to-end exercise: the producer
// runs through the seam exactly as the command does, and a fake notifier observes
// the start + finish alerts carrying the real scope and counts.
func TestWikiProducerThroughRunWithAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	p := wikiProducer{
		cfg: wiki.CrawlConfig{Categories: []string{"Category:Physics"}},
		run: func(context.Context, *slog.Logger, wiki.CrawlSource, wiki.Publisher, wiki.Gate, wiki.CrawlConfig) (wiki.CrawlStats, error) {
			return wiki.CrawlStats{Published: 4, Dropped: 1}, nil
		},
	}

	stats, err := crawlnotify.RunWithAlerts(t.Context(), rec, p)
	if err != nil {
		t.Fatalf("RunWithAlerts: %v", err)
	}
	if want := (crawlnotify.Stats{New: 4, Skipped: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + finish", len(rec.events))
	}
	start, ok := rec.events[0].(crawlnotify.RunStarted)
	if !ok || start.Source != "wikipedia" || start.Scope != "Category:Physics" {
		t.Errorf("start event = %#v", rec.events[0])
	}
	fin, ok := rec.events[1].(crawlnotify.RunFinished)
	if !ok || fin.Source != "wikipedia" || fin.Scope != "Category:Physics" || fin.New != 4 || fin.Skipped != 1 {
		t.Errorf("finish event = %#v", rec.events[1])
	}
}

func TestWikiProducerFailureAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	wantErr := errors.New("boom")
	p := wikiProducer{
		cfg: wiki.CrawlConfig{Categories: []string{"Category:Physics"}},
		run: func(context.Context, *slog.Logger, wiki.CrawlSource, wiki.Publisher, wiki.Gate, wiki.CrawlConfig) (wiki.CrawlStats, error) {
			return wiki.CrawlStats{}, wantErr
		},
	}

	_, err := crawlnotify.RunWithAlerts(t.Context(), rec, p)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + fail", len(rec.events))
	}
	fail, ok := rec.events[1].(crawlnotify.RunFailed)
	if !ok || fail.Source != "wikipedia" || fail.Scope != "Category:Physics" || !errors.Is(fail.Err, wantErr) {
		t.Errorf("fail event = %#v", rec.events[1])
	}
}

// testWriter routes a logger's output to the test log so a producer test never
// prints to stdout.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
