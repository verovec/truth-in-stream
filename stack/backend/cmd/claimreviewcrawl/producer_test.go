package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/claimreviewsite"
	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

type recordingNotifier struct {
	events []crawlnotify.CrawlEvent
}

func (r *recordingNotifier) Notify(_ context.Context, e crawlnotify.CrawlEvent) error {
	r.events = append(r.events, e)
	return nil
}

type fakeReader struct {
	stats claimreviewsite.Stats
	err   error
	ran   int
}

func (f *fakeReader) Run(_ context.Context, _ *slog.Logger, _ claimreviewsite.Publisher) (claimreviewsite.Stats, error) {
	f.ran++
	return f.stats, f.err
}

func TestClaimreviewProducerMapsStats(t *testing.T) {
	t.Parallel()
	reader := &fakeReader{stats: claimreviewsite.Stats{Published: 5, Skipped: 2}}
	p := claimreviewProducer{client: reader, outlets: 4}

	if p.Name() != "claimreview" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.Scope() != "4 allowlisted outlets" {
		t.Errorf("Scope() = %q", p.Scope())
	}
	stats, err := p.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (crawlnotify.Stats{New: 5, Skipped: 2}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if reader.ran != 1 {
		t.Errorf("ran %d, want 1", reader.ran)
	}
}

func TestClaimreviewProducerThroughRunWithAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	reader := &fakeReader{stats: claimreviewsite.Stats{Published: 3, Skipped: 1}}
	p := claimreviewProducer{client: reader, outlets: 3}

	stats, err := crawlnotify.RunWithAlerts(t.Context(), rec, p)
	if err != nil {
		t.Fatalf("RunWithAlerts: %v", err)
	}
	if stats.New != 3 || stats.Skipped != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if len(rec.events) != 2 {
		t.Fatalf("got %d events, want start + finish", len(rec.events))
	}
	if fin, ok := rec.events[1].(crawlnotify.RunFinished); !ok || fin.Source != "claimreview" {
		t.Errorf("finish event = %#v", rec.events[1])
	}
}

func TestClaimreviewProducerFailureAlerts(t *testing.T) {
	t.Parallel()
	rec := &recordingNotifier{}
	wantErr := errors.New("boom")
	p := claimreviewProducer{client: &fakeReader{err: wantErr}, outlets: 1}

	if _, err := crawlnotify.RunWithAlerts(t.Context(), rec, p); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if fail, ok := rec.events[1].(crawlnotify.RunFailed); !ok || !errors.Is(fail.Err, wantErr) {
		t.Errorf("fail event = %#v", rec.events[1])
	}
}
