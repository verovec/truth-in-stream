package audioextract

import (
	"context"
	"time"
)

// pacer meters frame submission to a multiple of realtime. Each frame's
// release time is computed from the stream start and the audio already sent,
// not by sleeping a fixed interval per frame: wall-clock targets absorb
// processing time between frames instead of adding to it, the drift
// AssemblyAI's pre-recorded streaming guide warns turns fixed sleeps into
// progressively late audio.
type pacer struct {
	clk    clock
	factor float64
	start  time.Time
	sentMs int64
}

func newPacer(clk clock, factor float64) *pacer {
	return &pacer{clk: clk, factor: factor}
}

// wait blocks until the wall clock reaches the release target for the next
// frame - the point where all previously sent audio has "played out" at the
// configured factor - then accounts frameMs as sent. The first call anchors
// the stream start and never sleeps.
func (p *pacer) wait(ctx context.Context, frameMs int) error {
	now := p.clk.Now()
	if p.start.IsZero() {
		p.start = now
	}
	target := p.start.Add(time.Duration(float64(p.sentMs) / p.factor * float64(time.Millisecond)))
	if d := target.Sub(now); d > 0 {
		if err := p.clk.Sleep(ctx, d); err != nil {
			return err
		}
	}
	p.sentMs += int64(frameMs)
	return nil
}
