package queue

import (
	"context"
	"time"
)

// WithIdleTimeout wraps a delivery stream so it closes cleanly once idle elapses
// with no new delivery arriving from src, letting a consumer drain a queue to
// empty and then exit for cost control (the hands-off drain-to-idle mode). A
// non-positive idle disables the behavior and returns src unchanged, so a worker
// with the mode off lives until ctx is canceled or the client is closed, exactly
// as before.
//
// The returned stream also closes when src closes or ctx is canceled, so it never
// outlives the underlying consumer. Idle is measured only while the wrapper waits
// for a delivery with none pending: a ready delivery is always forwarded in
// preference to firing the idle timer, so a backlog the worker is still draining -
// or one arriving faster than it is consumed - never idle-exits. Only a genuinely
// empty queue does, which is the condition the cloud consumer host keys its
// self-stop on.
func WithIdleTimeout(ctx context.Context, src <-chan Delivery, idle time.Duration) <-chan Delivery {
	if idle <= 0 {
		return src
	}
	out := make(chan Delivery)
	go func() {
		defer close(out)
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			// Prefer draining a ready delivery over firing the idle timer, so a
			// non-empty queue never idle-exits even when the worker is slower than the
			// idle window; the timer's branch is reachable only once nothing is ready.
			select {
			case d, ok := <-src:
				if !ok {
					return
				}
				if !forwardIdle(ctx, out, d) {
					return
				}
				resetIdle(timer, idle)
				continue
			default:
			}
			select {
			case d, ok := <-src:
				if !ok {
					return
				}
				if !forwardIdle(ctx, out, d) {
					return
				}
				resetIdle(timer, idle)
			case <-timer.C:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// forwardIdle hands d downstream, reporting false if ctx is canceled while the
// hand-off is pending (the consumer is gone) so the wrapper stops. While a
// hand-off blocks, the idle timer is not consulted, so a busy worker holding the
// stream is never mistaken for an empty queue.
func forwardIdle(ctx context.Context, out chan<- Delivery, d Delivery) bool {
	select {
	case out <- d:
		return true
	case <-ctx.Done():
		return false
	}
}

// resetIdle restarts the idle timer for the next wait, draining a pending fire so
// the reset window starts clean. It runs only on the wrapper's single goroutine.
func resetIdle(t *time.Timer, idle time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(idle)
}
