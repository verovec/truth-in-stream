package main

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/factcheckjob"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

// The adapters bridge the broker's transport types to the worker's
// transport-free interfaces, so internal/factcheckjob never imports
// internal/queue.

// qDelivery adapts a queue.Delivery to factcheckjob.Delivery.
type qDelivery struct{ d queue.Delivery }

func (q qDelivery) Body() []byte            { return q.d.Body }
func (q qDelivery) Priority() uint8         { return q.d.Priority }
func (q qDelivery) Version() string         { return q.d.Version }
func (q qDelivery) Ack() error              { return q.d.Ack() }
func (q qDelivery) Nack(requeue bool) error { return q.d.Nack(requeue) }

// qStream adapts a queue.Client's consume stream to factcheckjob.Stream, wrapping
// each broker delivery as it arrives. The forwarding goroutine ends when the
// broker closes the underlying stream (ctx canceled) or ctx is canceled while a
// hand-off is pending, so it never leaks past shutdown.
type qStream struct{ client *queue.Client }

func (s qStream) Consume(ctx context.Context) (<-chan factcheckjob.Delivery, error) {
	raw, err := s.client.Consume(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan factcheckjob.Delivery)
	go func() {
		defer close(out)
		for d := range raw {
			select {
			case out <- qDelivery{d: d}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// qEnqueuer adapts a queue.Client to factcheckjob.Enqueuer for bounded retries.
type qEnqueuer struct{ client *queue.Client }

func (e qEnqueuer) Enqueue(ctx context.Context, body []byte, priority uint8) error {
	return e.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
