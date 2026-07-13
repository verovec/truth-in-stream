package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/datacommons"
)

// recordingNotifier captures every event handed to it, in order.
type recordingNotifier struct {
	events []crawlnotify.CrawlEvent
}

func (r *recordingNotifier) Notify(_ context.Context, e crawlnotify.CrawlEvent) error {
	r.events = append(r.events, e)
	return nil
}

// fakeFeed returns a fixed result, so a test can prove the producer maps the feed
// Stats onto the fleet shape without a live feed or broker.
type fakeFeed struct {
	stats datacommons.Stats
	err   error
	ran   int
}

func (f *fakeFeed) Run(_ context.Context, _ *slog.Logger, _ datacommons.Publisher) (datacommons.Stats, error) {
	f.ran++
	return f.stats, f.err
}

func TestDatacommonsProducerScope(t *testing.T) {
	t.Parallel()
	if got := (datacommonsProducer{}).Name(); got != "datacommons" {
		t.Errorf("Name() = %q, want datacommons", got)
	}
	p := datacommonsProducer{outlets: []string{"factuel.afp.com", "lemonde.fr"}}
	if got, want := p.Scope(), "factuel.afp.com, lemonde.fr (2 outlets)"; got != want {
		t.Errorf("Scope() = %q, want %q", got, want)
	}
	if got := (datacommonsProducer{}).Scope(); got != "all outlets" {
		t.Errorf("empty-allowlist Scope() = %q, want all outlets", got)
	}
}

func TestDatacommonsProducerRunMapsStats(t *testing.T) {
	t.Parallel()
	feed := &fakeFeed{stats: datacommons.Stats{Published: 6, Unverifiable: 2, Skipped: 3}}
	p := datacommonsProducer{client: feed, outlets: []string{"lemonde.fr"}}

	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (crawlnotify.Stats{New: 6, Updated: 0, Skipped: 3}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if feed.ran != 1 {
		t.Errorf("feed ran %d times, want 1", feed.ran)
	}
}

// TestDatacommonsProducerThroughRunWithAlerts exercises the producer through the
// alert seam exactly as the command does.
func TestDatacommonsProducerThroughRunWithAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	feed := &fakeFeed{stats: datacommons.Stats{Published: 4, Skipped: 1}}
	p := datacommonsProducer{client: feed, outlets: []string{"lemonde.fr"}}

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
	if !ok || start.Source != "datacommons" {
		t.Errorf("start event = %#v", rec.events[0])
	}
	fin, ok := rec.events[1].(crawlnotify.RunFinished)
	if !ok || fin.Source != "datacommons" || fin.New != 4 || fin.Skipped != 1 {
		t.Errorf("finish event = %#v", rec.events[1])
	}
}

func TestDatacommonsProducerFailureAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	wantErr := errors.New("feed down")
	feed := &fakeFeed{err: wantErr}
	p := datacommonsProducer{client: feed, outlets: []string{"lemonde.fr"}}

	if _, err := crawlnotify.RunWithAlerts(t.Context(), rec, p); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + fail", len(rec.events))
	}
	fail, ok := rec.events[1].(crawlnotify.RunFailed)
	if !ok || fail.Source != "datacommons" || !errors.Is(fail.Err, wantErr) {
		t.Errorf("fail event = %#v", rec.events[1])
	}
}
