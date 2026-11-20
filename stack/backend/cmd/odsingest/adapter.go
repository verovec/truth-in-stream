package main

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

// qPublisher adapts the broker client to stats.Publisher, so the odsingest producer
// publishes embedding jobs without importing the transport. It is a thin
// delegation; the broker round-trip is covered by the queue package's tests.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
