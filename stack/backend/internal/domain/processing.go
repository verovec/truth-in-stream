package domain

import (
	"context"
	"time"
)

// Segment is one ordered, timestamped span of transcribed speech. Timestamps
// are millisecond precision: transcription APIs emit milliseconds and the
// store persists milliseconds, so finer durations would not round-trip.
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// SegmentMatch is one ranked claim hit for a spoken segment. Similarity is a
// numeric score where higher means more similar. The json tags are both the
// segment_results.matches jsonb wire format and the API response shape; they
// are identical by design so the stored result is served verbatim.
type SegmentMatch struct {
	Claim      string   `json:"claim"`
	Verdict    Verdict  `json:"verdict"`
	Sources    []Source `json:"sources"`
	Similarity float64  `json:"similarity"`
}

// SegmentResult is the fact-check outcome for one transcript segment. It is
// the unit a batch pipeline persists per segment and the exact shape an
// incremental live source will emit, so clients handle both modes identically.
type SegmentResult struct {
	Segment
	Matches []SegmentMatch
}

// SegmentResultWriter is the write side of the processing results port: the
// pipeline persists each segment as it completes and marks the video processed
// only after the last one, so a completion marker always means a full result
// set.
type SegmentResultWriter interface {
	// SaveSegmentResult inserts or replaces the result keyed by
	// (videoID, result.Start).
	SaveSegmentResult(ctx context.Context, videoID string, result SegmentResult) error
	// DeleteSegmentResults removes every persisted result for videoID. A
	// fresh run clears leftovers of an earlier failed run first, so a later
	// completion never serves stale rows from a different segmentation.
	DeleteSegmentResults(ctx context.Context, videoID string) error
	// MarkVideoProcessed records that all segmentCount segments of videoID
	// have been persisted.
	MarkVideoProcessed(ctx context.Context, videoID string, segmentCount int) error
}

// SegmentResultReader is the read side of the processing results port.
type SegmentResultReader interface {
	// ProcessedSegmentCount returns the persisted segment count for videoID
	// and whether the video has been fully processed.
	ProcessedSegmentCount(ctx context.Context, videoID string) (count int, processed bool, err error)
	// ListSegmentResults returns every persisted result for videoID ordered
	// by segment start time.
	ListSegmentResults(ctx context.Context, videoID string) ([]SegmentResult, error)
}

// SegmentResultStore is the full processing results port, implemented by
// store/postgres.
type SegmentResultStore interface {
	SegmentResultWriter
	SegmentResultReader
}
