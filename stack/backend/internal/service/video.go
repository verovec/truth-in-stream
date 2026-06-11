package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Video service errors. They classify a bad request so the handler maps each to
// its own status code; ErrVideoNotFound lives in the domain package because the
// store raises it.
var (
	// ErrInvalidTitle is returned when an upload request carries no title.
	ErrInvalidTitle = errors.New("video: title is required")
	// ErrInvalidContentType is returned for a content type outside the allowed set.
	ErrInvalidContentType = errors.New("video: unsupported content type")
	// ErrInvalidSize is returned when the declared upload size is non-positive
	// or exceeds the configured maximum.
	ErrInvalidSize = errors.New("video: declared size out of range")
	// ErrObjectNotUploaded is returned by Confirm when the object is not yet
	// present in storage, so the record stays pending and the client can retry.
	ErrObjectNotUploaded = errors.New("video: object not found in storage")
)

// defaultAllowedContentTypes is the web video set the upload API accepts when no
// allow-list is configured. Each is a container the transcription pipeline can
// extract audio from.
var defaultAllowedContentTypes = []string{
	"video/mp4",
	"video/webm",
	"video/ogg",
	"video/quicktime",
}

// contentTypeExtensions maps an allowed content type to the object-key
// extension used for readability; an unmapped type yields no extension. The
// extension is cosmetic: the object's real content type is set by the uploader.
var contentTypeExtensions = map[string]string{
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"video/ogg":       ".ogv",
	"video/quicktime": ".mov",
}

// defaultSampleVideos is the curated sample set seeded by EnsureSamples. Sample
// objects live under the samples/ prefix in the bucket; SizeBytes is unknown at
// seed time (the object is populated by the data-seeding tooling) and is left 0.
var defaultSampleVideos = []domain.Video{
	{
		Title:       "Common Myths",
		ObjectKey:   "samples/common-myths.mp4",
		ContentType: "video/mp4",
		SizeBytes:   0,
		Status:      domain.VideoStatusReady,
		Kind:        domain.VideoKindSample,
	},
}

// UploadRequest is the input to RequestUpload: the operator-supplied title, the
// declared content type, and the declared size in bytes.
type UploadRequest struct {
	Title       string
	ContentType string
	SizeBytes   int64
}

// UploadTicket pairs the created (pending) video record with the presigned PUT
// the browser uses to upload its bytes directly to storage.
type UploadTicket struct {
	Video  domain.Video
	Upload domain.PresignedRequest
}

// PlayableVideo pairs a video record with a presigned GET for direct,
// range-capable playback from storage.
type PlayableVideo struct {
	Video    domain.Video
	Playback domain.PresignedRequest
}

// VideoConfig configures a VideoService. MaxUploadBytes bounds a declared
// upload size.
type VideoConfig struct {
	MaxUploadBytes int64
}

// VideoService owns the video-record lifecycle: it mints presigned uploads,
// confirms completed uploads against storage, lists and resolves records, and
// seeds curated samples. It holds no HTTP types. newObjectKey is a field rather
// than a direct call so tests can inject a deterministic key; it is unexported
// and set only by the constructor, so no caller can bypass validation.
type VideoService struct {
	store          domain.VideoStore
	media          domain.MediaStore
	maxUploadBytes int64
	allowed        map[string]bool
	newObjectKey   func(contentType string) string
}

// NewVideoService builds a VideoService. It fails fast on a nil dependency or a
// non-positive upload bound.
func NewVideoService(store domain.VideoStore, media domain.MediaStore, cfg VideoConfig) (*VideoService, error) {
	if store == nil {
		return nil, errors.New("video: store is required")
	}
	if media == nil {
		return nil, errors.New("video: media store is required")
	}
	if cfg.MaxUploadBytes <= 0 {
		return nil, fmt.Errorf("video: max upload bytes must be positive, got %d", cfg.MaxUploadBytes)
	}
	allowed := make(map[string]bool, len(defaultAllowedContentTypes))
	for _, ct := range defaultAllowedContentTypes {
		allowed[ct] = true
	}
	return &VideoService{
		store:          store,
		media:          media,
		maxUploadBytes: cfg.MaxUploadBytes,
		allowed:        allowed,
		newObjectKey:   uploadObjectKey,
	}, nil
}

// RequestUpload validates the request, records a pending upload, and returns it
// with a presigned PUT the browser uses to upload bytes directly to storage.
func (s *VideoService) RequestUpload(ctx context.Context, req UploadRequest) (UploadTicket, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return UploadTicket{}, ErrInvalidTitle
	}
	if !s.allowed[req.ContentType] {
		return UploadTicket{}, ErrInvalidContentType
	}
	if req.SizeBytes <= 0 || req.SizeBytes > s.maxUploadBytes {
		return UploadTicket{}, ErrInvalidSize
	}

	key := s.newObjectKey(req.ContentType)
	video, err := s.store.CreateVideo(ctx, domain.Video{
		Title:       title,
		ObjectKey:   key,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
		Status:      domain.VideoStatusPending,
		Kind:        domain.VideoKindUpload,
	})
	if err != nil {
		return UploadTicket{}, fmt.Errorf("video: request upload: %w", err)
	}
	presigned, err := s.media.PresignUpload(ctx, key)
	if err != nil {
		return UploadTicket{}, fmt.Errorf("video: presign upload: %w", err)
	}
	return UploadTicket{Video: video, Upload: presigned}, nil
}

// Confirm verifies the upload's object is present in storage and flips the
// record to ready. It is idempotent: a record already ready is returned
// unchanged. A still-missing object leaves the record pending and returns
// ErrObjectNotUploaded so the client can retry.
func (s *VideoService) Confirm(ctx context.Context, id string) (domain.Video, error) {
	video, err := s.store.GetVideo(ctx, id)
	if err != nil {
		return domain.Video{}, err
	}
	if video.Status == domain.VideoStatusReady {
		return video, nil
	}
	exists, err := s.media.Exists(ctx, video.ObjectKey)
	if err != nil {
		return domain.Video{}, fmt.Errorf("video: confirm %s: %w", id, err)
	}
	if !exists {
		return domain.Video{}, ErrObjectNotUploaded
	}
	updated, err := s.store.SetVideoStatus(ctx, id, domain.VideoStatusReady)
	if err != nil {
		return domain.Video{}, fmt.Errorf("video: confirm %s: %w", id, err)
	}
	return updated, nil
}

// List returns every video record, newest first.
func (s *VideoService) List(ctx context.Context) ([]domain.Video, error) {
	videos, err := s.store.ListVideos(ctx)
	if err != nil {
		return nil, fmt.Errorf("video: list: %w", err)
	}
	return videos, nil
}

// Get returns the record with the given id plus a presigned, range-capable
// playback URL.
func (s *VideoService) Get(ctx context.Context, id string) (PlayableVideo, error) {
	video, err := s.store.GetVideo(ctx, id)
	if err != nil {
		return PlayableVideo{}, err
	}
	presigned, err := s.media.PresignDownload(ctx, video.ObjectKey)
	if err != nil {
		return PlayableVideo{}, fmt.Errorf("video: get %s: %w", id, err)
	}
	return PlayableVideo{Video: video, Playback: presigned}, nil
}

// EnsureSamples seeds the curated sample records idempotently. It is safe to run
// on every startup: samples upsert by object key, keeping a stable id.
func (s *VideoService) EnsureSamples(ctx context.Context) error {
	for _, sample := range defaultSampleVideos {
		if _, err := s.store.UpsertSampleVideo(ctx, sample); err != nil {
			return fmt.Errorf("video: ensure sample %q: %w", sample.ObjectKey, err)
		}
	}
	return nil
}

// uploadObjectKey mints a unique storage key for an upload of contentType under
// the uploads/ prefix. The UUID guarantees uniqueness; the extension is purely
// for readability.
func uploadObjectKey(contentType string) string {
	return "uploads/" + uuid.NewString() + contentTypeExtensions[contentType]
}
