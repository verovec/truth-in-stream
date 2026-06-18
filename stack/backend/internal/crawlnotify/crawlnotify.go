// Package crawlnotify announces ingestion runs to Slack. It defines the common
// Producer seam every crawler in the ingestion fleet runs through and a tiny
// Notifier that posts run summaries and failures to an incoming webhook, so
// alerting lives in one place rather than being re-implemented per crawler.
//
// Alert granularity is deliberately coarse - one message when a run starts, one
// when it finishes (with new/updated/skipped counts and duration), and one when
// it fails - never per ingested unit, so a large fleet stays legible instead of
// flooding the channel. With no webhook configured the notifier is a silent
// no-op, so local runs are unaffected.
package crawlnotify

import (
	"context"
	"fmt"
	"time"
)

// Stats is the outcome of one producer run: how many units were newly ingested,
// updated in place, or skipped as unchanged. Its zero value is a valid empty run.
type Stats struct {
	New     int
	Updated int
	Skipped int
}

// Producer is the uniform run-once seam every ingestion crawler implements. Name
// identifies the source (e.g. "wikipedia", "scrutins"); Scope is a human-readable
// description of what this run ingests (e.g. a category, query, or legislature);
// Run executes the ingestion once and reports its Stats. Run must honor the
// context's cancellation.
type Producer interface {
	Name() string
	Scope() string
	Run(ctx context.Context) (Stats, error)
}

// CrawlEvent is a run lifecycle event a Notifier can announce. The variants are
// RunStarted, RunFinished, and RunFailed; message renders the one-line summary
// shared by every transport.
type CrawlEvent interface {
	message() string
}

// RunStarted announces that a producer is about to ingest a scope.
type RunStarted struct {
	Source string
	Scope  string
}

func (e RunStarted) message() string {
	return fmt.Sprintf("Ingestion started: %s (%s)", e.Source, e.Scope)
}

// RunFinished announces a completed run with its counts and elapsed time.
type RunFinished struct {
	Source   string
	Scope    string
	New      int
	Updated  int
	Skipped  int
	Duration time.Duration
}

func (e RunFinished) message() string {
	return fmt.Sprintf("Ingestion finished: %s (%s) - %d new, %d updated, %d skipped in %s",
		e.Source, e.Scope, e.New, e.Updated, e.Skipped, e.Duration.Round(time.Second))
}

// RunFailed announces a run that aborted, with a short reason.
type RunFailed struct {
	Source string
	Scope  string
	Err    error
}

func (e RunFailed) message() string {
	reason := "unknown error"
	if e.Err != nil {
		reason = e.Err.Error()
	}
	return fmt.Sprintf("Ingestion FAILED: %s (%s) - %s", e.Source, e.Scope, reason)
}

// Notifier announces a crawl event. Implementations are best-effort: a transport
// failure is the caller's to log, never a reason to abort the run.
type Notifier interface {
	Notify(ctx context.Context, event CrawlEvent) error
}

// NoopNotifier discards every event. It is the active notifier when no webhook
// is configured, so local runs without Slack behave exactly as before.
type NoopNotifier struct{}

// Notify discards the event and never errors.
func (NoopNotifier) Notify(context.Context, CrawlEvent) error { return nil }

// NewNotifier returns a Slack notifier when webhookURL is set, and the silent
// NoopNotifier when it is empty. This is the single wiring decision the fleet
// keys off SLACK_WEBHOOK_URL.
func NewNotifier(webhookURL string) Notifier {
	if webhookURL == "" {
		return NoopNotifier{}
	}
	return NewSlackNotifier(webhookURL)
}

// alertNotifyTimeout bounds each post-run alert. The finish/fail alert runs on a
// context derived from the background, not the producer's, so a run that aborts
// because its own context was canceled or timed out still delivers its failure
// alert - the moment the operator most needs one.
const alertNotifyTimeout = slackHTTPTimeout

// RunWithAlerts runs a producer once, wrapping it with start and finish/fail
// alerts: it emits RunStarted, runs the producer, then emits RunFinished on
// success or RunFailed on error, and returns the producer's Stats and error
// unchanged. Notifier failures are swallowed - alerting must never decide a
// run's outcome - so the producer's error is the only one that propagates. The
// outcome alert is decoupled from the producer's context so a canceled or
// timed-out run is still announced.
func RunWithAlerts(ctx context.Context, n Notifier, p Producer) (Stats, error) {
	source, scope := p.Name(), p.Scope()
	_ = n.Notify(ctx, RunStarted{Source: source, Scope: scope})

	start := time.Now()
	stats, err := p.Run(ctx)
	elapsed := time.Since(start)

	alertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertNotifyTimeout)
	defer cancel()

	if err != nil {
		_ = n.Notify(alertCtx, RunFailed{Source: source, Scope: scope, Err: err})
		return stats, err
	}
	_ = n.Notify(alertCtx, RunFinished{
		Source:   source,
		Scope:    scope,
		New:      stats.New,
		Updated:  stats.Updated,
		Skipped:  stats.Skipped,
		Duration: elapsed,
	})
	return stats, nil
}
