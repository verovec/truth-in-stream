package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestRunRejectsUnknownMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// An unsupported mode is rejected before any config load or DB connection,
	// so this needs no environment.
	if err := run(logger, "frobnicate", t.TempDir(), false, false, 0); err == nil {
		t.Fatal("run accepted an unsupported mode, want error")
	}
}

func TestRunRejectsPublishOnlyOutsideBulk(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// -publish-only only makes sense for bulk; delta and reset reject it before
	// any config load, so this needs no environment.
	for _, mode := range []string{"delta", "reset"} {
		err := run(logger, mode, t.TempDir(), false, true, 0)
		if err == nil || !strings.Contains(err.Error(), "publish-only") {
			t.Errorf("run(%q, publishOnly=true) error = %v, want a publish-only rejection", mode, err)
		}
	}
}

func TestRunRejectsPublishOnlyWithDryRun(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := run(logger, "bulk", t.TempDir(), true, true, 0)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("run(bulk, dryRun+publishOnly) error = %v, want mutually-exclusive rejection", err)
	}
}

func TestRunAcceptsResetMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// reset is a recognized mode: it passes mode validation and only fails later
	// (here, on config load without DATABASE_URL), never with the unsupported-mode
	// error a typo would trigger.
	err := run(logger, "reset", t.TempDir(), false, false, 0)
	if err == nil {
		t.Skip("reset succeeded (DATABASE_URL is set in this env); mode-acceptance still holds")
	}
	if strings.Contains(err.Error(), "unsupported mode") {
		t.Errorf("reset rejected as an unsupported mode: %v", err)
	}
}

// TestClassifyStop pins the rule that decides whether a stop is clean: it is
// keyed on the owned context (a -max-duration budget or an interrupt), never on
// the shape of the work error. A provider timeout satisfies
// errors.Is(err, context.DeadlineExceeded) too, so error-sniffing would mistake
// it for a budget stop and silently swallow a real failure.
func TestClassifyStop(t *testing.T) {
	t.Parallel()
	providerTimeout := fmt.Errorf("wiki: embed chunks [0:128]: %w",
		fmt.Errorf("embed: giving up after 6 attempts: %w", context.DeadlineExceeded))
	tests := []struct {
		name          string
		workErr       error
		ctxErr        error
		wantStopped   bool
		wantNilResult bool
	}{
		{name: "success", workErr: nil, ctxErr: nil, wantNilResult: true},
		{
			name:          "finished as the budget expired is a success, not a stop",
			workErr:       nil,
			ctxErr:        context.DeadlineExceeded,
			wantNilResult: true,
		},
		{
			name:        "budget expired mid-run",
			workErr:     fmt.Errorf("wiki: read pending chunks: %w", context.DeadlineExceeded),
			ctxErr:      context.DeadlineExceeded,
			wantStopped: true,
		},
		{
			name:        "interrupt mid-run",
			workErr:     fmt.Errorf("wiki: load embedded chunks: %w", context.Canceled),
			ctxErr:      context.Canceled,
			wantStopped: true,
		},
		{
			name:        "provider timeout with a live context is a failure, not a clean stop",
			workErr:     providerTimeout,
			ctxErr:      nil,
			wantStopped: false,
		},
		{
			name:        "genuine failure",
			workErr:     errors.New("wiki: finalize staging: relation does not exist"),
			ctxErr:      nil,
			wantStopped: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyStop(tc.workErr, tc.ctxErr)
			if tc.wantNilResult {
				if got != nil {
					t.Fatalf("classifyStop = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("classifyStop = nil, want an error")
			}
			if stoppedEarly(got) != tc.wantStopped {
				t.Errorf("stoppedEarly(%v) = %v, want %v", got, stoppedEarly(got), tc.wantStopped)
			}
		})
	}
}
