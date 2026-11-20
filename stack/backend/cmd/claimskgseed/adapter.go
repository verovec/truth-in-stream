package main

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

// qPublisher adapts the broker client to claimskg.Publisher, so the seeder never
// imports the transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}
