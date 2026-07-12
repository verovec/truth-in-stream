package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// TV recording service errors. They classify a bad registration request so the
// handler maps each to a status code; ErrObjectNotUploaded is shared with the
// upload path and ErrVideoNotFound / ErrTVChannelNotFound come from the domain.
var (
	// ErrTVRecordingNoRecordedAt is returned when a recording request omits the
	// segment's start time, which the storage key and title are derived from.
	ErrTVRecordingNoRecordedAt = errors.New("tv recording: recorded-at time is required")
	// ErrTVRecordingInvalidContentType is returned for a content type other than
	// the archive container the capture worker produces.
	ErrTVRecordingInvalidContentType = errors.New("tv recording: unsupported content type")
	// ErrTVRecordingInvalidSize is returned when the declared segment size is
	// non-positive or exceeds the configured maximum.
	ErrTVRecordingInvalidSize = errors.New("tv recording: declared size out of range")
)

// tvRecordingContentType is the only archive container the capture worker
// produces: hourly MPEG-TS segments remuxed to MP4 with a stream copy. Binding
// it here keeps the presigned PUT's content-type signature exact.
const tvRecordingContentType = "video/mp4"

// defaultTVRecordingMaxBytes bounds a declared segment size. An hour of a news
// simulcast stream-copied to MP4 is a few GB; the bound is generous so a long,
// high-bitrate hour is not rejected, while still capping an absurd declaration.
const defaultTVRecordingMaxBytes = 16 << 30

// TVRecordingChannelStore is the slice of the channel store the recording
// service needs: it resolves the source channel to build the storage key and
// the human-readable recording title. Satisfied by *store/postgres and the
// channel store fakes.
type TVRecordingChannelStore interface {
	GetTVChannel(ctx context.Context, id string) (domain.TVChannel, error)
}

// TVRecordingRequest is the input to RequestUpload: the source channel, the
// captured segment's start time, and the declared object content type and size.
type TVRecordingRequest struct {
	ChannelID   string
	RecordedAt  time.Time
	ContentType string
	SizeBytes   int64
}

// TVRecordingService owns the archived-recording lifecycle for the capture
// worker: it mints a presigned PUT under the channel's recordings prefix and a
// pending kind `tv` video row, flips that row to ready once the worker confirms
// the object landed, and prunes expired recordings (object and row together).
// It reuses the ordinary video store and media port, so a recording is an
// ordinary video that replays through the existing watch flow. It holds no HTTP
// types. now is a field so retention tests pin the clock.
type TVRecordingService struct {
	videos   domain.VideoStore
	channels TVRecordingChannelStore
	media    domain.MediaStore
	maxBytes int64
	now      func() time.Time
}

// NewTVRecordingService builds a TVRecordingService. It fails fast on a nil
// dependency; a non-positive maxBytes falls back to the default bound.
func NewTVRecordingService(videos domain.VideoStore, channels TVRecordingChannelStore, media domain.MediaStore, maxBytes int64) (*TVRecordingService, error) {
	if videos == nil {
		return nil, errors.New("tv recording: video store is required")
	}
	if channels == nil {
		return nil, errors.New("tv recording: channel store is required")
	}
	if media == nil {
		return nil, errors.New("tv recording: media store is required")
	}
	if maxBytes <= 0 {
		maxBytes = defaultTVRecordingMaxBytes
	}
	return &TVRecordingService{
		videos:   videos,
		channels: channels,
		media:    media,
		maxBytes: maxBytes,
		now:      time.Now,
	}, nil
}

// RequestUpload validates the request, resolves the channel, records a pending
// kind `tv` video under the channel's recordings prefix, and returns it with a
// write-once presigned PUT the worker uploads the remuxed segment to. An unknown
// channel surfaces domain.ErrTVChannelNotFound unwrapped so the handler answers
// 404.
func (s *TVRecordingService) RequestUpload(ctx context.Context, req TVRecordingRequest) (UploadTicket, error) {
	if req.RecordedAt.IsZero() {
		return UploadTicket{}, ErrTVRecordingNoRecordedAt
	}
	if req.ContentType != tvRecordingContentType {
		return UploadTicket{}, ErrTVRecordingInvalidContentType
	}
	if req.SizeBytes <= 0 || req.SizeBytes > s.maxBytes {
		return UploadTicket{}, ErrTVRecordingInvalidSize
	}
	channel, err := s.channels.GetTVChannel(ctx, req.ChannelID)
	if err != nil {
		return UploadTicket{}, err
	}
	recordedAt := req.RecordedAt.UTC()
	key := tvRecordingObjectKey(channel.Slug, recordedAt)
	video, err := s.videos.CreateVideo(ctx, domain.Video{
		Title:       tvRecordingTitle(channel.Name, recordedAt),
		ObjectKey:   key,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
		Status:      domain.VideoStatusPending,
		Kind:        domain.VideoKindTV,
		ChannelID:   channel.ID,
		RecordedAt:  recordedAt,
	})
	if err != nil {
		return UploadTicket{}, fmt.Errorf("tv recording: create record: %w", err)
	}
	presigned, err := s.media.PresignUploadOnce(ctx, key, req.ContentType, req.SizeBytes)
	if err != nil {
		return UploadTicket{}, fmt.Errorf("tv recording: presign upload: %w", err)
	}
	return UploadTicket{Video: video, Upload: presigned}, nil
}

// Register verifies the recording's object is present in storage and flips the
// record to ready. It is idempotent: an already-ready record is returned
// unchanged without a storage round-trip. A still-missing object leaves the
// record pending and returns ErrObjectNotUploaded so the worker can retry. A
// record that is not a kind `tv` recording is reported as ErrVideoNotFound, so
// this path cannot be used to confirm an arbitrary video.
func (s *TVRecordingService) Register(ctx context.Context, videoID string) (domain.Video, error) {
	video, err := s.videos.GetVideo(ctx, videoID)
	if err != nil {
		return domain.Video{}, err
	}
	if video.Kind != domain.VideoKindTV {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	if video.Status == domain.VideoStatusReady {
		return video, nil
	}
	exists, err := s.media.Exists(ctx, video.ObjectKey)
	if err != nil {
		return domain.Video{}, fmt.Errorf("tv recording: register %s: %w", videoID, err)
	}
	if !exists {
		return domain.Video{}, ErrObjectNotUploaded
	}
	updated, err := s.videos.SetVideoStatus(ctx, videoID, domain.VideoStatusReady)
	if err != nil {
		return domain.Video{}, fmt.Errorf("tv recording: register %s: %w", videoID, err)
	}
	return updated, nil
}

// Prune deletes every kind `tv` recording whose RecordedAt is older than the
// retention window, storage object first then row, so a failed row delete leaves
// the record visible for a retry rather than stranding an object. It returns the
// number of recordings removed. A non-positive retention is rejected so an
// accidental zero window cannot wipe the archive.
func (s *TVRecordingService) Prune(ctx context.Context, retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, errors.New("tv recording: retention must be positive")
	}
	cutoff := s.now().Add(-retention)
	videos, err := s.videos.ListVideos(ctx)
	if err != nil {
		return 0, fmt.Errorf("tv recording: list for prune: %w", err)
	}
	deleted := 0
	for _, v := range videos {
		if v.Kind != domain.VideoKindTV || v.RecordedAt.IsZero() {
			continue
		}
		if !v.RecordedAt.Before(cutoff) {
			continue
		}
		if err := s.media.Delete(ctx, v.ObjectKey); err != nil {
			return deleted, fmt.Errorf("tv recording: prune delete object %s: %w", v.ID, err)
		}
		if err := s.videos.DeleteVideo(ctx, v.ID); err != nil {
			return deleted, fmt.Errorf("tv recording: prune delete record %s: %w", v.ID, err)
		}
		deleted++
	}
	return deleted, nil
}

// tvRecordingObjectKey mints the storage key for a recording:
// recordings/{slug}/{YYYY}/{MM}/{DD}/{HHMMSS}.mp4 in UTC, so the archive is
// date-partitioned per channel and keys sort chronologically.
func tvRecordingObjectKey(slug string, at time.Time) string {
	u := at.UTC()
	return fmt.Sprintf("recordings/%s/%04d/%02d/%02d/%02d%02d%02d.mp4",
		slug, u.Year(), int(u.Month()), u.Day(), u.Hour(), u.Minute(), u.Second())
}

// tvRecordingTitle generates a recording's library title from the channel name
// and the segment start, e.g. "franceinfo - 2026-07-10 20:00".
func tvRecordingTitle(name string, at time.Time) string {
	return fmt.Sprintf("%s - %s", name, at.UTC().Format("2006-01-02 15:04"))
}
