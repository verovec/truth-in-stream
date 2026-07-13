package crawlnotify

import (
	"context"
	"fmt"
	"time"
)

// ConsumerStats is the outcome of one long-running consumer drain: how many
// deliveries it acknowledged and how many it parked in the dead-letter queue
// (a poison message or one past its retry budget). Its zero value is a valid
// empty run. It mirrors producer Stats so a consumer run reports symmetrically.
type ConsumerStats struct {
	Processed   int64
	ParkedToDLQ int64
}

// ConsumerStarted announces that a consumer began draining its queue.
type ConsumerStarted struct {
	Consumer string
	Queue    string
}

func (e ConsumerStarted) message() string {
	return fmt.Sprintf("Consumer started: %s (%s)", e.Consumer, e.Queue)
}

// ConsumerStopped announces a consumer that drained cleanly (a graceful
// SIGTERM), with its processed and dead-lettered counts and elapsed time.
type ConsumerStopped struct {
	Consumer    string
	Queue       string
	Processed   int64
	ParkedToDLQ int64
	Duration    time.Duration
}

func (e ConsumerStopped) message() string {
	return fmt.Sprintf("Consumer stopped: %s (%s) - %d processed, %d parked to DLQ in %s",
		e.Consumer, e.Queue, e.Processed, e.ParkedToDLQ, e.Duration.Round(time.Second))
}

// ConsumerFailed announces a consumer that exited with an error, carrying the
// counts it accumulated before the failure so a partial drain is still legible.
type ConsumerFailed struct {
	Consumer    string
	Queue       string
	Processed   int64
	ParkedToDLQ int64
	Err         error
}

func (e ConsumerFailed) message() string {
	reason := "unknown error"
	if e.Err != nil {
		reason = e.Err.Error()
	}
	return fmt.Sprintf("Consumer FAILED: %s (%s) - %d processed, %d parked to DLQ - %s",
		e.Consumer, e.Queue, e.Processed, e.ParkedToDLQ, reason)
}

// RunConsumerWithAlerts runs a consumer drain once, wrapping it with a start and
// a stop/fail alert symmetrical to a producer's RunWithAlerts: it emits
// ConsumerStarted, runs the drain, then emits ConsumerStopped on a clean return
// or ConsumerFailed on error, and returns the drain's ConsumerStats and error
// unchanged. Notifier failures are swallowed - alerting must never decide a
// run's outcome. The outcome alert runs on a context decoupled from the drain's,
// so a consumer stopped by its own canceled context (a SIGTERM) still announces
// its drain summary - the moment the operator most wants one.
func RunConsumerWithAlerts(ctx context.Context, n Notifier, consumer, queue string, run func(context.Context) (ConsumerStats, error)) (ConsumerStats, error) {
	_ = n.Notify(ctx, ConsumerStarted{Consumer: consumer, Queue: queue})

	start := time.Now()
	stats, err := run(ctx)
	elapsed := time.Since(start)

	alertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertNotifyTimeout)
	defer cancel()

	if err != nil {
		_ = n.Notify(alertCtx, ConsumerFailed{
			Consumer:    consumer,
			Queue:       queue,
			Processed:   stats.Processed,
			ParkedToDLQ: stats.ParkedToDLQ,
			Err:         err,
		})
		return stats, err
	}
	_ = n.Notify(alertCtx, ConsumerStopped{
		Consumer:    consumer,
		Queue:       queue,
		Processed:   stats.Processed,
		ParkedToDLQ: stats.ParkedToDLQ,
		Duration:    elapsed,
	})
	return stats, nil
}
