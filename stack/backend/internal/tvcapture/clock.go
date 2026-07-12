package tvcapture

import "time"

// clock is the time seam the supervisor uses for the watchdog, segment poll,
// and restart backoff, so timing is injectable in tests.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) tickerHandle
}

// tickerHandle is a startable/stoppable periodic tick, mirroring time.Ticker.
type tickerHandle interface {
	C() <-chan time.Time
	Stop()
}

// realClock is the production clock backed by the time package.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTicker(d time.Duration) tickerHandle { return realTicker{time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }
