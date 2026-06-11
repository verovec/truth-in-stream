package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
)

func TestRunRejectsUnknownMode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// An unsupported mode is rejected before any config load or DB connection,
	// so this needs no environment.
	if err := run(logger, "frobnicate", t.TempDir(), false, 0); err == nil {
		t.Fatal("run accepted an unsupported mode, want error")
	}
}

func TestStoppedEarly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "deadline budget", err: context.DeadlineExceeded, want: true},
		{name: "interrupt signal", err: context.Canceled, want: true},
		{
			name: "wrapped deadline from a store call",
			err:  fmt.Errorf("wiki: read pending chunks: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "doubly wrapped cancellation through the embed retry",
			err:  fmt.Errorf("wiki: embed chunks [0:64]: %w", fmt.Errorf("embed request: %w", context.Canceled)),
			want: true,
		},
		{name: "genuine failure", err: errors.New("wiki: finalize staging: relation does not exist"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := stoppedEarly(tc.err); got != tc.want {
				t.Errorf("stoppedEarly(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
