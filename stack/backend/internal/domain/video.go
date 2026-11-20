package domain

import (
	"context"
	"errors"
	"time"
)

// ErrVideoNotFound is returned by a VideoStore when no record matches the given
// id. Callers detect it with errors.Is and map it to a 404; it never wraps a
// transport type.
var ErrVideoNotFound = errors.New("video not found")

// ErrDuplicateSource is returned when creating a video whose canonical source id
// already exists. The YouTube ingest path uses it to make re-submitting the same
// link a no-op: the caller resolves and returns the existing record instead.
var ErrDuplicateSource = errors.New("video source already ingested")

// ErrIngestNotRetriable is returned when a retry is attempted on a record that is
// not in the failed state. It lets the ingest path claim a failed record for
// retry atomically: only the caller that flips failed->pending re-runs the work.
var ErrIngestNotRetriable = errors.New("video ingest is not in a retriable state")

// VideoKind distinguishes operator uploads from curated sample clips. Both
// surface through one library listing so the frontend renders samples and
// uploads in a single grid.
type VideoKind string

const (
	// VideoKindUpload is a video the operator uploaded to storage.
	VideoKindUpload VideoKind = "upload"
	// VideoKindSample is a curated clip seeded with the application.
	VideoKindSample VideoKind = "sample"
	// VideoKindYouTube is a video the backend downloaded from a YouTube link and
	// wrote to storage server-side.
	VideoKindYouTube VideoKind = "youtube"
	// VideoKindTV is an archived hour captured from a live TV channel. Its
	// ChannelID links it to the source channel and RecordedAt is the segment's
	// start; it replays through the existing watch flow like any other video.
	VideoKindTV VideoKind = "tv"
)

// Valid reports whether k is a known video kind.
func (k VideoKind) Valid() bool {
	switch k {
	case VideoKindUpload, VideoKindSample, VideoKindYouTube, VideoKindTV:
		return true
	default:
		return false
	}
}

// VideoStatus is the lifecycle of a video record. An upload starts Pending,
// becomes Ready once its object is confirmed present in storage, and is Failed
// when an upload is abandoned or rejected. Samples are seeded Ready.
type VideoStatus string

const (
	// VideoStatusPending is an upload whose object has not yet been confirmed.
	VideoStatusPending VideoStatus = "pending"
	// VideoStatusReady is a video whose object is present and playable.
	VideoStatusReady VideoStatus = "ready"
	// VideoStatusFailed is an upload that will never become playable.
	VideoStatusFailed VideoStatus = "failed"
)

// Valid reports whether s is a known video status.
func (s VideoStatus) Valid() bool {
	switch s {
	case VideoStatusPending, VideoStatusReady, VideoStatusFailed:
		return true
	default:
		return false
	}
}

// Video is a first-class media record: a durable identity, the storage object
// it maps to, and its processing lifecycle status. ID is the canonical string
// form of the row's UUID; the storage object lives at ObjectKey.
type Video struct {
	ID          string
	Title       string
	ObjectKey   string
	ContentType string
	SizeBytes   int64
	Status      VideoStatus
	Kind        VideoKind
	// SourceURL is the operator-submitted origin (the YouTube watch URL) for an
	// ingested video; empty for uploads and samples.
	SourceURL string
	// SourceID is the canonical id of the source (the YouTube video id), unique
	// across the catalog so the same link is never ingested twice; empty for
	// uploads and samples.
	SourceID string
	// DurationMS is the probed media length in milliseconds; 0 when unknown.
	DurationMS int64
	// Error is the reason a failed ingest will never become playable; empty
	// otherwise.
	Error string
	// ChannelID is the source TV channel of a kind `tv` recording; empty for
	// uploads, samples, and YouTube imports. It nulls out if the channel is
	// deleted, so a recording outlives its channel.
	ChannelID string
	// RecordedAt is the wall-clock start of a kind `tv` recording's captured
	// segment; the zero time for every other kind.
	RecordedAt time.Time
	// AnalysisStatus tracks the durable pre-analysis lifecycle, orthogonal to
	// the upload lifecycle; playback and live analysis work regardless of it.
	AnalysisStatus VideoAnalysisStatus
	// AnalysisError is the reason the latest run failed; empty otherwise.
	AnalysisError string
	// AnalyzedAt is when the latest run completed; zero when never completed.
	AnalyzedAt time.Time
	// AnalysisRuns counts completed runs, so a re-analysis is observable.
	AnalysisRuns int
	// AnalysisProgressMS is the audio position the current run has reached, in
	// milliseconds; it drives the progress indicator while analysing.
	AnalysisProgressMS int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// DownloadResult is the outcome of fetching a video from a source: the path to
// the single downloaded file plus the metadata probed alongside it. It is a
// plain data carrier shared by the downloader adapter (which produces it) and
// the ingest service (which consumes it), so neither imports the other.
type DownloadResult struct {
	// FilePath is the absolute path to the downloaded file inside the
	// caller-provided destination directory.
	FilePath string
	// Title is the source's title.
	Title string
	// DurationMS is the source's length in milliseconds; 0 when unknown.
	DurationMS int64
	// SizeBytes is the size of the downloaded file.
	SizeBytes int64
	// ContentType is the MIME type of the downloaded file; consumers fall back
	// to video/mp4 when empty.
	ContentType string
}

// VideoStore is the persistence port for video records, implemented by
// internal/store/postgres. It holds no transport types.
type VideoStore interface {
	// CreateVideo inserts v (its ID, CreatedAt, and UpdatedAt are assigned by
	// the store) and returns the stored record.
	CreateVideo(ctx context.Context, v Video) (Video, error)
	// GetVideo returns the record with the given id, or ErrVideoNotFound.
	GetVideo(ctx context.Context, id string) (Video, error)
	// DeleteVideo removes the record with the given id, or returns
	// ErrVideoNotFound when no record matches.
	DeleteVideo(ctx context.Context, id string) error
	// ListVideos returns every record, newest first.
	ListVideos(ctx context.Context) ([]Video, error)
	// SetVideoStatus updates the status of the record with the given id and
	// returns the updated record, or ErrVideoNotFound.
	SetVideoStatus(ctx context.Context, id string, status VideoStatus) (Video, error)
	// UpsertSampleVideo inserts or updates a curated sample keyed by its
	// ObjectKey, so seeding the same sample repeatedly is idempotent and keeps
	// a stable id. It returns the stored record.
	UpsertSampleVideo(ctx context.Context, v Video) (Video, error)
}
