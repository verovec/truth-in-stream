package main

import (
	"context"
	"errors"
	"log/slog"
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

// fakeArchive returns a fixed result and records how many streams it was asked to run.
type fakeArchive struct {
	stats      factcheckarchive.Stats
	err        error
	ranStreams int
}

func (f *fakeArchive) RunStreams(_ context.Context, _ *slog.Logger, _ factcheckarchive.Publisher, streams []factcheckarchive.RunConfig, _ factcheckarchive.StreamCheckpoint) (factcheckarchive.Stats, error) {
	f.ranStreams = len(streams)
	return f.stats, f.err
}

// spyCheckpoint records whether Clear was called, so a test can assert the producer
// clears the checkpoint only on full success.
type spyCheckpoint struct {
	factcheckarchive.NoStreamCheckpoint
	cleared bool
}

func (c *spyCheckpoint) Clear() error {
	c.cleared = true
	return nil
}

func nStreams(n int) []factcheckarchive.RunConfig {
	out := make([]factcheckarchive.RunConfig, n)
	for i := range out {
		out[i] = factcheckarchive.RunConfig{Query: "topic"}
	}
	return out
}

func TestFactcheckProducerScope(t *testing.T) {
	t.Parallel()
	p := factcheckProducer{streams: []factcheckarchive.RunConfig{
		{Query: "retraites"}, {Query: "immigration"}, {PublisherSite: "lemonde.fr"},
	}}
	if got := p.Name(); got != "factcheck" {
		t.Errorf("Name() = %q, want factcheck", got)
	}
	if got, want := p.Scope(), "2 topics + 1 publisher streams"; got != want {
		t.Errorf("Scope() = %q, want %q", got, want)
	}
}

func TestFactcheckProducerRunClearsCheckpointOnSuccess(t *testing.T) {
	t.Parallel()
	arch := &fakeArchive{stats: factcheckarchive.Stats{Published: 8, Skipped: 3}}
	cp := &spyCheckpoint{}
	p := factcheckProducer{client: arch, streams: nStreams(4), checkpoint: cp}

	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (crawlnotify.Stats{New: 8, Skipped: 3}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if arch.ranStreams != 4 {
		t.Errorf("ran %d streams, want 4", arch.ranStreams)
	}
	if !cp.cleared {
		t.Error("checkpoint not cleared after a full successful run")
	}
}

func TestFactcheckProducerRunKeepsCheckpointOnError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("api down")
	arch := &fakeArchive{stats: factcheckarchive.Stats{Published: 2}, err: wantErr}
	cp := &spyCheckpoint{}
	p := factcheckProducer{client: arch, streams: nStreams(4), checkpoint: cp}

	stats, err := p.Run(t.Context())
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if want := (crawlnotify.Stats{New: 2}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if cp.cleared {
		t.Error("checkpoint cleared despite a stream error; a rerun must resume")
	}
}

// TestFactcheckProducerThroughRunWithAlerts is the end-to-end exercise: the producer
// runs through the seam exactly as the command does, and a fake notifier observes
// the start + finish alerts carrying the real scope and counts.
func TestFactcheckProducerThroughRunWithAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	arch := &fakeArchive{stats: factcheckarchive.Stats{Published: 7, Skipped: 2}}
	p := factcheckProducer{client: arch, streams: []factcheckarchive.RunConfig{{Query: "Macron"}}, checkpoint: factcheckarchive.NoStreamCheckpoint{}}

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
	if !ok || start.Source != "factcheck" {
		t.Errorf("start event = %#v", rec.events[0])
	}
	fin, ok := rec.events[1].(crawlnotify.RunFinished)
	if !ok || fin.Source != "factcheck" || fin.New != 7 || fin.Skipped != 2 {
		t.Errorf("finish event = %#v", rec.events[1])
	}
}

func TestFactcheckProducerFailureAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	wantErr := errors.New("boom")
	arch := &fakeArchive{err: wantErr}
	p := factcheckProducer{client: arch, streams: nStreams(1), checkpoint: factcheckarchive.NoStreamCheckpoint{}}

	_, err := crawlnotify.RunWithAlerts(t.Context(), rec, p)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + fail", len(rec.events))
	}
	fail, ok := rec.events[1].(crawlnotify.RunFailed)
	if !ok || fail.Source != "factcheck" || !errors.Is(fail.Err, wantErr) {
		t.Errorf("fail event = %#v", rec.events[1])
	}
}
