package schedule

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// Scheduler runs every registered source on its own cadence until its context is
// canceled. Each source ticks independently, so a slow source never delays the
// others; a tick whose previous run is still in flight is skipped (logged) so runs
// cannot stack; and every run is wrapped in crawlnotify.RunWithAlerts so the
// start/finish/error alerts are uniform across the fleet.
type Scheduler struct {
	registry Registry
	notifier crawlnotify.Notifier
	logger   *slog.Logger
}

// New builds a Scheduler over the registered sources, posting run alerts through
// notifier and logging lifecycle events to logger.
func New(registry Registry, notifier crawlnotify.Notifier, logger *slog.Logger) *Scheduler {
	return &Scheduler{registry: registry, notifier: notifier, logger: logger}
}

// Run drives every registered source until ctx is canceled, then blocks until each
// in-flight run has returned so shutdown never abandons a run mid-publish. With no
// registered source it blocks until ctx is canceled, so an always-on caller idles
// rather than exiting (which would crash-loop a restart-on-exit container).
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, e := range s.registry.entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runSource(ctx, e)
		}()
	}
	<-ctx.Done()
	wg.Wait()
}

// runSource owns one source's tick loop. It computes the next fire from the
// schedule, waits for it (or shutdown), and on each tick runs the producer unless
// a previous run is still in flight, in which case the tick is skipped. The
// in-flight guard is a per-source atomic so a slow run cannot stack ticks.
func (s *Scheduler) runSource(ctx context.Context, e entry) {
	var running atomic.Bool
	// inFlight tracks the current run so shutdown can wait for it to finish.
	var inFlight sync.WaitGroup

	for {
		now := time.Now()
		fire := e.schedule.Next(now)
		delay := fire.Sub(now) + jitterFor(e.jitter)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			inFlight.Wait()
			return
		case <-timer.C:
		}

		if !running.CompareAndSwap(false, true) {
			s.logger.WarnContext(ctx, "scheduled run skipped, previous run still in flight",
				slog.String("source", e.source))
			continue
		}

		inFlight.Add(1)
		go func() {
			defer inFlight.Done()
			defer running.Store(false)
			s.runOnce(ctx, e)
		}()
	}
}

// runOnce executes a single producer run wrapped in the shared alert helper. The
// producer's error is logged here (RunWithAlerts has already alerted on it); a
// failed run never ends the scheduler, so the source keeps its cadence.
func (s *Scheduler) runOnce(ctx context.Context, e entry) {
	s.logger.InfoContext(ctx, "scheduled run starting", slog.String("source", e.source))
	stats, err := crawlnotify.RunWithAlerts(ctx, s.notifier, e.producer)
	if err != nil {
		s.logger.ErrorContext(ctx, "scheduled run failed",
			slog.String("source", e.source), slog.Any("err", err))
		return
	}
	s.logger.InfoContext(ctx, "scheduled run finished",
		slog.String("source", e.source),
		slog.Int("new", stats.New),
		slog.Int("updated", stats.Updated),
		slog.Int("skipped", stats.Skipped))
}

// jitterFor returns a random delay in [0, jitter); a non-positive jitter adds
// nothing. The spread keeps simultaneously-scheduled sources from hitting their
// backends at the same instant.
func jitterFor(jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return 0
	}
	return rand.N(jitter)
}
