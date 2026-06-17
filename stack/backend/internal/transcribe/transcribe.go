// Package transcribe turns streaming audio into ordered, timestamped transcript
// segments. AssemblyAI Universal-3 Pro streaming is the single speech-to-text
// provider; its realtime WebSocket emits committed and partial Turn events that
// the live pipeline surfaces as captions and fact-checks, for live streams and
// imported videos alike.
package transcribe

import "time"

// Segment is a contiguous span of speech with its position in the source.
// Speaker is the diarized speaker label emitted on each committed turn; it lets
// the live aggregator keep one speaker's words out of another's analysis unit.
type Segment struct {
	Start   time.Duration
	End     time.Duration
	Text    string
	Speaker string
}

// TranscriptEvent is one incremental result from a streaming transcription.
// Final marks a committed segment; non-final segments may still be revised.
type TranscriptEvent struct {
	Segment Segment
	Final   bool
}

// Options tunes a single transcription request. The zero value auto-detects the
// language.
type Options struct {
	// Language biases the spoken language as an ISO-639 code (e.g. "fr"); it is
	// sent as the provider's language_code parameter. Empty means provider-side
	// auto-detection.
	Language string
}
