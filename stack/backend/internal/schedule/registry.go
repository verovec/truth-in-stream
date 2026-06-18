// Package schedule runs the ingestion fleet's producers on cron cadences. It owns
// a Registry mapping a source to a parsed cron schedule plus its crawlnotify
// Producer, and a Scheduler that drives those entries on a tick loop: each due
// source runs once (wrapped in crawlnotify.RunWithAlerts for uniform start/finish/
// error alerts), an overlapping tick of a source whose previous run is still in
// flight is skipped rather than stacked, and a small per-source jitter spreads the
// fleet to avoid a thundering herd.
//
// The package is deliberately runtime-agnostic: it depends only on crawlnotify and
// a cron-spec parser, never on docker-compose, a broker, or any cloud runtime, so
// the same Registry a local cmd/scheduler builds can be reused by a future cloud
// runner (EventBridge/Fargate) without a rewrite.
package schedule

import (
	"fmt"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/verovec/truth-in-stream/backend/internal/crawlnotify"
)

// Schedule yields the next fire time at or after t. It is the one method the tick
// loop needs from a cron spec; cron.Schedule (from robfig/cron) satisfies it, and
// a test can supply its own deterministic schedule.
type Schedule interface {
	Next(t time.Time) time.Time
}

// entry binds one source to its schedule, optional jitter, and producer. It is the
// unit the Scheduler ticks; the Producer is run through crawlnotify.RunWithAlerts.
type entry struct {
	source   string
	schedule Schedule
	jitter   time.Duration
	producer crawlnotify.Producer
}

// Registry maps each source to its schedule and producer. Its zero value is an
// empty, ready-to-use registry. Register parses and validates a cron spec (failing
// fast on a bad one); RegisterSchedule takes an already-built Schedule, which is
// how tests inject a deterministic schedule and how a caller can register a
// non-cron cadence. A source may be registered only once.
type Registry struct {
	entries []entry
	seen    map[string]struct{}
}

// Register parses spec as a standard 5-field cron expression and binds it to the
// source and producer with no jitter. An invalid spec returns an error so the
// caller can fail fast at startup. To add jitter, register with RegisterSchedule.
func (r *Registry) Register(source, spec string, producer crawlnotify.Producer) error {
	schedule, err := ParseSpec(spec)
	if err != nil {
		return fmt.Errorf("schedule %q: %w", source, err)
	}
	return r.RegisterSchedule(source, schedule, 0, producer)
}

// RegisterSchedule binds an already-built schedule, jitter, and producer to the
// source. jitter must be non-negative; a positive jitter delays each fire by a
// random duration in [0, jitter) to spread concurrent sources. It errors on a nil
// schedule or producer, a negative jitter, or a duplicate source.
func (r *Registry) RegisterSchedule(source string, schedule Schedule, jitter time.Duration, producer crawlnotify.Producer) error {
	if schedule == nil {
		return fmt.Errorf("schedule %q: nil schedule", source)
	}
	if producer == nil {
		return fmt.Errorf("schedule %q: nil producer", source)
	}
	if jitter < 0 {
		return fmt.Errorf("schedule %q: negative jitter %s", source, jitter)
	}
	if r.seen == nil {
		r.seen = make(map[string]struct{})
	}
	if _, dup := r.seen[source]; dup {
		return fmt.Errorf("schedule %q: source already registered", source)
	}
	r.seen[source] = struct{}{}
	r.entries = append(r.entries, entry{
		source:   source,
		schedule: schedule,
		jitter:   jitter,
		producer: producer,
	})
	return nil
}

// Len reports how many sources are registered.
func (r *Registry) Len() int { return len(r.entries) }

// ParseSpec parses a standard 5-field cron expression (minute hour dom month dow)
// into a Schedule, returning an error for a malformed spec. It is the fail-fast
// validation the scheduler config runs at startup.
func ParseSpec(spec string) (Schedule, error) {
	schedule, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid cron spec %q: %w", spec, err)
	}
	return schedule, nil
}
