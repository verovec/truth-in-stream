package main

import (
	"errors"
	"io"
	"log/slog"
	"testing"
)

// TestRunRequiresDir proves the command fails fast with errMissingDir when no
// -dir is given, before it ever tries to load config or open the store, so a
// missing flag is a clear operator error rather than a connection failure.
func TestRunRequiresDir(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(logger, ""); !errors.Is(err, errMissingDir) {
		t.Fatalf("run(\"\") error = %v, want errMissingDir", err)
	}
}
