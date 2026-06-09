// Package transcribe turns audio or video sources into ordered, timestamped
// transcript segments. The Transcriber interface is the stable contract shared
// by the v1 batch path and the future real-time path: live mode emits the same
// Segment shape incrementally, so callers never change.
package transcribe

import (
	"context"
	"io"
	"time"
)

// Segment is a contiguous span of speech with its position in the source.
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Transcript is the complete result of transcribing one source.
type Transcript struct {
	Language string
	Segments []Segment
}

// TranscriptEvent is one incremental result from a streaming transcription.
// Final marks a committed segment; non-final segments may still be revised.
type TranscriptEvent struct {
	Segment Segment
	Final   bool
}

// Options tunes a single transcription request. The zero value auto-detects
// the language.
type Options struct {
	// Language pins the spoken language as an ISO-639 code; empty means
	// provider-side auto-detection.
	Language string
	// Filename labels the uploaded source for providers that sniff the
	// container format from the name; empty falls back to a generic name.
	Filename string
}

// Transcriber converts an audio or video source into timestamped segments.
// Implementations wrap one speech-to-text provider; callers must not depend
// on which one.
type Transcriber interface {
	// TranscribeFile transcribes a complete pre-recorded source in one call.
	TranscribeFile(ctx context.Context, audio io.Reader, opts Options) (Transcript, error)
	// TranscribeStream transcribes audio chunks as they arrive, emitting
	// segments incrementally until chunks closes or ctx is done. Unimplemented
	// in v1; the signature is fixed now so live mode needs no caller change.
	TranscribeStream(ctx context.Context, chunks <-chan []byte, opts Options) (<-chan TranscriptEvent, error)
}
