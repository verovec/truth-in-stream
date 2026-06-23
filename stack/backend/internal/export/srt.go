// Package export renders a completed video's cached analysis snapshot into
// operator-facing artifacts: an SRT subtitle track of the transcript and a CSV
// of the fact-check decision trace. It is pure - it operates on the decoded
// []service.LiveEvent and returns bytes, with no HTTP or I/O - so it is fully
// unit-testable and the handler layer owns transport.
package export

import (
	"fmt"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// SRT renders the finalized subtitle segments of a snapshot as an SRT subtitle
// track: one block per LiveEventSubtitle event, in the order they were emitted,
// with a 1-based index, an HH:MM:SS,mmm cue range and the segment text. Interim
// captions and every non-subtitle event are skipped, so only committed
// transcript reaches the file. A speaker label, when present, prefixes the text.
// The result is empty when the snapshot holds no finalized segments.
func SRT(events []service.LiveEvent) []byte {
	var b strings.Builder
	index := 0
	for _, ev := range events {
		if ev.Kind != service.LiveEventSubtitle {
			continue
		}
		index++
		text := ev.Segment.Text
		if ev.Segment.Speaker != "" {
			text = ev.Segment.Speaker + ": " + text
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			index,
			srtTimestamp(ev.Segment.Start),
			srtTimestamp(ev.Segment.End),
			text,
		)
	}
	return []byte(b.String())
}

// srtTimestamp formats a duration as the SRT cue stamp HH:MM:SS,mmm, with a
// comma decimal separator and zero-padded fields. A negative duration is clamped
// to zero so a malformed segment can never emit a stray minus sign into a cue.
func srtTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := d.Milliseconds()
	ms := total % 1000
	totalSeconds := total / 1000
	seconds := totalSeconds % 60
	totalMinutes := totalSeconds / 60
	minutes := totalMinutes % 60
	hours := totalMinutes / 60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, ms)
}
