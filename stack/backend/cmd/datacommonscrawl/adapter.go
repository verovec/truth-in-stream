package main

import (
	"context"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/datacommons"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
)

// qPublisher adapts the broker client to datacommons.Publisher, so the
// DataCommons producer never imports the transport.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}

// newFeedClient builds the DataCommons client from the loaded config, threading
// every field (including Format, so DATACOMMONS_FEED_FORMAT=ndjson actually selects
// the dump decoder). It is the single construction point main and the entrypoint
// test share, so the wiring is covered without a live broker.
func newFeedClient(cfg config.DataCommonsArchive, maxPriority uint8) (*datacommons.Client, error) {
	return datacommons.New(datacommons.Config{
		FeedURL:         cfg.FeedURL,
		OutletAllowlist: cfg.OutletAllowlist,
		MaxItems:        cfg.MaxItems,
		Format:          cfg.Format,
		MaxPriority:     maxPriority,
	})
}
