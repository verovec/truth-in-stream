package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestRunRejectsUnknownMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// An unsupported mode is rejected before any config load or DB connection,
	// so this needs no environment.
	if err := run(logger, "frobnicate", t.TempDir(), false); err == nil {
		t.Fatal("run accepted an unsupported mode, want error")
	}
}
