package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
	"github.com/verovec/truth-in-stream/backend/internal/factcheckarchive"
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

// fakeArchive returns a fixed result per query and records the queries it was
// asked to run, so a test can prove the producer walks every query in order.
type fakeArchive struct {
	results map[string]struct {
		stats factcheckarchive.Stats
		err   error
	}
	ranQueries []string
}

func (f *fakeArchive) Run(_ context.Context, _ *slog.Logger, _ factcheckarchive.Publisher, cfg factcheckarchive.RunConfig) (factcheckarchive.Stats, error) {
	f.ranQueries = append(f.ranQueries, cfg.Query)
	r := f.results[cfg.Query]
	return r.stats, r.err
}

func TestFactcheckProducerScope(t *testing.T) {
	t.Parallel()
	p := factcheckProducer{queries: []string{"Macron", "retraites"}}
	if got := p.Name(); got != "factcheck" {
		t.Errorf("Name() = %q, want factcheck", got)
	}
	if got, want := p.Scope(), "Macron, retraites"; got != want {
		t.Errorf("Scope() = %q, want %q", got, want)
	}
}

func TestFactcheckProducerRunSumsStats(t *testing.T) {
	t.Parallel()
	arch := &fakeArchive{results: map[string]struct {
		stats factcheckarchive.Stats
		err   error
	}{
		"Macron":    {stats: factcheckarchive.Stats{Published: 5, Skipped: 2}},
		"retraites": {stats: factcheckarchive.Stats{Published: 3, Skipped: 1}},
	}}
	p := factcheckProducer{client: arch, queries: []string{"Macron", "retraites"}, maxPages: 4}

	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (crawlnotify.Stats{New: 8, Updated: 0, Skipped: 3}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	// Every configured query is walked, in order: pagination per query is unchanged.
	if want := []string{"Macron", "retraites"}; !slices.Equal(arch.ranQueries, want) {
		t.Errorf("ran queries = %v, want %v", arch.ranQueries, want)
	}
}

func TestFactcheckProducerRunStopsAndReportsOnError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("api down")
	arch := &fakeArchive{results: map[string]struct {
		stats factcheckarchive.Stats
		err   error
	}{
		"Macron":    {stats: factcheckarchive.Stats{Published: 5, Skipped: 2}},
		"retraites": {stats: factcheckarchive.Stats{Published: 1}, err: wantErr},
		"never":     {stats: factcheckarchive.Stats{Published: 99}},
	}}
	p := factcheckProducer{client: arch, queries: []string{"Macron", "retraites", "never"}}

	stats, err := p.Run(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	// Counts gathered before the failure are reported; the query after the failure never runs.
	if want := (crawlnotify.Stats{New: 6, Skipped: 2}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if got := arch.ranQueries; len(got) != 2 {
		t.Errorf("ran %v queries, want to stop after the failing one", got)
	}
}

// TestFactcheckProducerThroughRunWithAlerts is the end-to-end exercise: the
// producer runs through the seam exactly as the command does, and a fake notifier
// observes the start + finish alerts carrying the real scope and counts.
func TestFactcheckProducerThroughRunWithAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	arch := &fakeArchive{results: map[string]struct {
		stats factcheckarchive.Stats
		err   error
	}{
		"Macron": {stats: factcheckarchive.Stats{Published: 7, Skipped: 2}},
	}}
	p := factcheckProducer{client: arch, queries: []string{"Macron"}}

	stats, err := crawlnotify.RunWithAlerts(t.Context(), rec, p)
	if err != nil {
		t.Fatalf("RunWithAlerts: %v", err)
	}
	if want := (crawlnotify.Stats{New: 7, Skipped: 2}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + finish", len(rec.events))
	}
	start, ok := rec.events[0].(crawlnotify.RunStarted)
	if !ok || start.Source != "factcheck" || start.Scope != "Macron" {
		t.Errorf("start event = %#v", rec.events[0])
	}
	fin, ok := rec.events[1].(crawlnotify.RunFinished)
	if !ok || fin.Source != "factcheck" || fin.Scope != "Macron" || fin.New != 7 || fin.Skipped != 2 {
		t.Errorf("finish event = %#v", rec.events[1])
	}
}

func TestFactcheckProducerFailureAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	wantErr := errors.New("boom")
	arch := &fakeArchive{results: map[string]struct {
		stats factcheckarchive.Stats
		err   error
	}{
		"Macron": {err: wantErr},
	}}
	p := factcheckProducer{client: arch, queries: []string{"Macron"}}

	_, err := crawlnotify.RunWithAlerts(t.Context(), rec, p)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + fail", len(rec.events))
	}
	fail, ok := rec.events[1].(crawlnotify.RunFailed)
	if !ok || fail.Source != "factcheck" || fail.Scope != "Macron" || !errors.Is(fail.Err, wantErr) {
		t.Errorf("fail event = %#v", rec.events[1])
	}
}
