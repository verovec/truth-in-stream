package domain

import (
	"context"
	"errors"
	"time"
)

// ErrVideoAnalysisNotFound is returned when no stored analysis exists for the
// given video. Callers detect it with errors.Is; it never wraps a transport
// type.
var ErrVideoAnalysisNotFound = errors.New("video analysis not found")

// ErrVideoNotReady is returned when an analysis is triggered on a video whose
// upload is not ready (still pending or failed): there is no playable media to
// analyze.
var ErrVideoNotReady = errors.New("video is not ready for analysis")

// ErrVideoAnalysisInProgress is returned when an analysis is triggered on a
// video whose analysis is already running. The analysing status is the job
// lock, so a concurrent trigger is a conflict.
var ErrVideoAnalysisInProgress = errors.New("video analysis is already in progress")

// VideoAnalysisStatus is the durable pre-analysis lifecycle of a video,
// mirroring DocumentAnalysisStatus and orthogonal to the upload lifecycle: a
// ready video may never have been analyzed (None), be mid-run (Analysing), or
// have finished (Complete/Failed). Analysing doubles as the job lock: a
// concurrent trigger on an analysing video is rejected.
type VideoAnalysisStatus string

const (
	// VideoAnalysisNone means no analysis has ever started.
	VideoAnalysisNone VideoAnalysisStatus = "none"
	// VideoAnalysisAnalysing means a run is in progress.
	VideoAnalysisAnalysing VideoAnalysisStatus = "analysing"
	// VideoAnalysisComplete means the latest run finished and its events are
	// stored.
	VideoAnalysisComplete VideoAnalysisStatus = "complete"
	// VideoAnalysisFailed means the latest run ended with an error.
	VideoAnalysisFailed VideoAnalysisStatus = "failed"
)

// Valid reports whether s is a known analysis status.
func (s VideoAnalysisStatus) Valid() bool {
	switch s {
	case VideoAnalysisNone, VideoAnalysisAnalysing, VideoAnalysisComplete, VideoAnalysisFailed:
		return true
	default:
		return false
	}
}

// VideoAnalysis is a video's durably stored completed analysis: the ordered
// live events the pipeline emitted, the engine that produced them, and
// denormalized claim counters for list badges. One row exists per video; a
// re-analysis overwrites it. Events and Engine are opaque JSON at this layer -
// the service layer owns their encoding, exactly as it does for the Redis
// snapshot - so the store stays payload-agnostic.
type VideoAnalysis struct {
	VideoID string
	// SnapshotVersion is the schema version of the Events payload, stamped at
	// write time and checked on read so a build never mis-decodes an older
	// format.
	SnapshotVersion int
	// Events is the ordered live-event stream as JSON, with absolute
	// video-time timestamps.
	Events []byte
	// Engine is a JSON record of the model identifiers and config fingerprint
	// of the run that produced this analysis.
	Engine             []byte
	ClaimsTotal        int
	ClaimsCredible     int
	ClaimsDisputed     int
	ClaimsUnverifiable int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VideoAnalysisStore is the persistence port for durable video analyses,
// implemented by internal/store/postgres. It holds no transport types.
type VideoAnalysisStore interface {
	// CompleteVideoAnalysis atomically stores a completed run (upsert, one row
	// per video) and flips the video's lifecycle to complete, stamping
	// analyzed_at and counting the run. It returns the stored record, or
	// ErrVideoNotFound for an unknown video.
	CompleteVideoAnalysis(ctx context.Context, a VideoAnalysis) (VideoAnalysis, error)
	// GetVideoAnalysis returns the stored analysis for the video, or
	// ErrVideoAnalysisNotFound.
	GetVideoAnalysis(ctx context.Context, videoID string) (VideoAnalysis, error)
}
