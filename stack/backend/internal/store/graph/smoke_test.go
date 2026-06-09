package graph

import (
	"context"
	"testing"
)

func TestOpenPingClose(t *testing.T) {
	repo, err := Open(t.TempDir() + "/smoke.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer repo.Close()

	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
