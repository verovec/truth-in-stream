package main

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/config"
)

// TestNewFeedClientThreadsFormat proves the entrypoint wiring passes
// DATACOMMONS_FEED_FORMAT through to the client, so the ndjson dump decoder is
// reachable via the real command (not only a package-level unit test).
func TestNewFeedClientThreadsFormat(t *testing.T) {
	t.Setenv("DATACOMMONS_FEED_FORMAT", "ndjson")
	cfg, err := config.LoadDataCommonsArchive()
	if err != nil {
		t.Fatalf("LoadDataCommonsArchive: %v", err)
	}
	client, err := newFeedClient(cfg, 9)
	if err != nil {
		t.Fatalf("newFeedClient: %v", err)
	}
	if client.Format() != "ndjson" {
		t.Fatalf("entrypoint client format = %q, want ndjson", client.Format())
	}
}

func TestNewFeedClientDefaultsDatafeed(t *testing.T) {
	cfg, err := config.LoadDataCommonsArchive()
	if err != nil {
		t.Fatalf("LoadDataCommonsArchive: %v", err)
	}
	client, err := newFeedClient(cfg, 9)
	if err != nil {
		t.Fatalf("newFeedClient: %v", err)
	}
	if client.Format() != "datafeed" {
		t.Fatalf("default entrypoint format = %q, want datafeed", client.Format())
	}
}
