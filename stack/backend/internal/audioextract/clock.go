package audioextract

import (
	"context"
	"time"
)

// clock is the time seam for the pacer, so pacing is asserted in tests
// through recorded sleeps instead of real delays (the same seam pattern as
// tvcapture's supervisor clock).
type clock interface {
	Now() time.Time
	// Sleep blocks for d or until ctx ends, returning ctx's error in that
	// case.
	Sleep(ctx context.Context, d time.Duration) error
}

// realClock is the production clock backed by the time package.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
