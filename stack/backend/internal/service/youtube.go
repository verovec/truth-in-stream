package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// YouTube ingest errors. ErrInvalidYouTubeURL classifies a bad request so the
// handler maps it to 400; ErrDownloadTooLarge surfaces inside the background
// worker and is recorded as a failure reason, never returned to a caller.
var (
	// ErrInvalidYouTubeURL is returned when the submitted link is not a YouTube
	// video URL the ingest can resolve to a canonical video id.
	ErrInvalidYouTubeURL = errors.New("youtube: not a valid youtube video url")
	// ErrDownloadTooLarge is returned by the worker when a download exceeds the
	// configured size bound.
	ErrDownloadTooLarge = errors.New("youtube: download exceeds the maximum size")
)

// youtubeContentType is the container the downloader is asked to produce, and
// the content type recorded on the stored object.
const youtubeContentType = "video/mp4"

// statusWriteTimeout bounds the database write that records a terminal ingest
// outcome. It uses a fresh context so a download timeout cannot also abort the
// write that records the failure.
const statusWriteTimeout = 10 * time.Second

// youtubeIDRe matches YouTube's canonical 11-character video id.
var youtubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Downloader fetches a video from a URL into destDir and returns the resulting
// file and its probed metadata. It is the seam the yt-dlp adapter implements and
// a fake replaces in tests, so the ingest orchestration carries no subprocess
// knowledge.
type Downloader interface {
	Download(ctx context.Context, videoURL, destDir string) (domain.DownloadResult, error)
}

// mediaUploader is the slice of the media store the ingest consumes: a single
// server-side object write. Presigning lives on the upload path, not here.
type mediaUploader interface {
	Upload(ctx context.Context, key string, body io.Reader, contentType string, size int64) error
}

// ingestStore is the YouTube ingest lifecycle port: create a pending record
// (deduplicated by canonical source id), resolve an existing one, and record a
// terminal outcome. It is one cohesive use case, satisfied by *postgres.Store.
type ingestStore interface {
	CreateYouTubeVideo(ctx context.Context, v domain.Video) (domain.Video, error)
	GetVideoBySourceID(ctx context.Context, sourceID string) (domain.Video, error)
	SetVideoReady(ctx context.Context, id, title string, sizeBytes, durationMS int64) (domain.Video, error)
	SetVideoFailed(ctx context.Context, id, reason string) (domain.Video, error)
}

// IngestConfig configures an IngestService. MaxDownloadBytes bounds a single
// download; DownloadTimeout bounds the whole download-upload run.
type IngestConfig struct {
	MaxDownloadBytes int64
	DownloadTimeout  time.Duration
}

// IngestService turns a YouTube link into a cataloged, stored video. Submit is
// synchronous only up to creating the pending record and returns 202-worthy
// state immediately; the slow download, upload, and probe run in a background
// worker that flips the record to ready or failed. spawn is a field so a test
// can run the worker inline; it is unexported and set only by the constructor.
type IngestService struct {
	store      ingestStore
	media      mediaUploader
	downloader Downloader
	maxBytes   int64
	timeout    time.Duration
	logger     *slog.Logger
	spawn      func(func())
}

// NewIngestService builds an IngestService. It fails fast on a nil dependency or
// a non-positive bound.
func NewIngestService(store ingestStore, media mediaUploader, downloader Downloader, cfg IngestConfig, logger *slog.Logger) (*IngestService, error) {
	if store == nil {
		return nil, errors.New("youtube: store is required")
	}
	if media == nil {
		return nil, errors.New("youtube: media store is required")
	}
	if downloader == nil {
		return nil, errors.New("youtube: downloader is required")
	}
	if cfg.MaxDownloadBytes <= 0 {
		return nil, fmt.Errorf("youtube: max download bytes must be positive, got %d", cfg.MaxDownloadBytes)
	}
	if cfg.DownloadTimeout <= 0 {
		return nil, fmt.Errorf("youtube: download timeout must be positive, got %s", cfg.DownloadTimeout)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &IngestService{
		store:      store,
		media:      media,
		downloader: downloader,
		maxBytes:   cfg.MaxDownloadBytes,
		timeout:    cfg.DownloadTimeout,
		logger:     logger,
		spawn:      func(f func()) { go f() },
	}, nil
}

// Submit validates the link, records a pending record (deduplicated by canonical
// video id), and starts the background download. Re-submitting the same link
// returns the existing record without re-downloading. The returned record is
// always the catalog entry; its status is pending unless it was already ingested.
func (s *IngestService) Submit(ctx context.Context, rawURL string) (domain.Video, error) {
	id, err := parseYouTubeID(rawURL)
	if err != nil {
		return domain.Video{}, err
	}
	canonical := canonicalYouTubeURL(id)

	video, err := s.store.CreateYouTubeVideo(ctx, domain.Video{
		Title:       canonical,
		ObjectKey:   youtubeObjectKey(id),
		ContentType: youtubeContentType,
		Status:      domain.VideoStatusPending,
		Kind:        domain.VideoKindYouTube,
		SourceURL:   canonical,
		SourceID:    id,
	})
	if errors.Is(err, domain.ErrDuplicateSource) {
		existing, gErr := s.store.GetVideoBySourceID(ctx, id)
		if gErr != nil {
			return domain.Video{}, fmt.Errorf("youtube: resolve duplicate %s: %w", id, gErr)
		}
		// Re-submitting retries a previously failed ingest (which left no
		// object); a pending or ready record is returned unchanged so the same
		// link never re-downloads while one is in flight or already stored.
		if existing.Status == domain.VideoStatusFailed {
			s.spawn(func() { s.process(existing) })
		}
		return existing, nil
	}
	if err != nil {
		return domain.Video{}, fmt.Errorf("youtube: create %s: %w", id, err)
	}

	s.spawn(func() { s.process(video) })
	return video, nil
}

// process runs the background ingest for one pending record on its own bounded
// context, then records the terminal outcome. A request context is never used
// here: the work outlives the HTTP request that triggered it.
func (s *IngestService) process(video domain.Video) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	result, err := s.fetch(ctx, video)
	if err != nil {
		s.markFailed(video, err)
		return
	}

	writeCtx, writeCancel := context.WithTimeout(context.Background(), statusWriteTimeout)
	defer writeCancel()
	if _, err := s.store.SetVideoReady(writeCtx, video.ID, result.Title, result.SizeBytes, result.DurationMS); err != nil {
		s.logger.ErrorContext(writeCtx, "youtube ingest: mark ready",
			slog.String("video_id", video.ID), slog.String("source_id", video.SourceID), slog.Any("err", err))
	}
}

// fetch downloads the video to a temp directory, enforces the size bound, and
// uploads it to storage. The temp directory is always removed; storage's
// PutObject is atomic, so a failed upload leaves no half-written object.
func (s *IngestService) fetch(ctx context.Context, video domain.Video) (domain.DownloadResult, error) {
	dir, err := os.MkdirTemp("", "yt-ingest-*")
	if err != nil {
		return domain.DownloadResult{}, fmt.Errorf("youtube: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	result, err := s.downloader.Download(ctx, video.SourceURL, dir)
	if err != nil {
		return domain.DownloadResult{}, fmt.Errorf("youtube: download %s: %w", video.SourceID, err)
	}
	if result.SizeBytes > s.maxBytes {
		return domain.DownloadResult{}, fmt.Errorf("youtube: %s is %d bytes: %w", video.SourceID, result.SizeBytes, ErrDownloadTooLarge)
	}

	f, err := os.Open(result.FilePath)
	if err != nil {
		return domain.DownloadResult{}, fmt.Errorf("youtube: open download: %w", err)
	}
	defer func() { _ = f.Close() }()

	contentType := result.ContentType
	if contentType == "" {
		contentType = youtubeContentType
	}
	if err := s.media.Upload(ctx, video.ObjectKey, f, contentType, result.SizeBytes); err != nil {
		return domain.DownloadResult{}, fmt.Errorf("youtube: upload %s: %w", video.ObjectKey, err)
	}
	return result, nil
}

// markFailed records why an ingest will never become playable, on a fresh
// context so a download timeout cannot also abort the status write.
func (s *IngestService) markFailed(video domain.Video, cause error) {
	s.logger.ErrorContext(context.Background(), "youtube ingest failed",
		slog.String("video_id", video.ID), slog.String("source_id", video.SourceID), slog.Any("err", cause))

	ctx, cancel := context.WithTimeout(context.Background(), statusWriteTimeout)
	defer cancel()
	if _, err := s.store.SetVideoFailed(ctx, video.ID, failureReason(cause)); err != nil {
		s.logger.ErrorContext(ctx, "youtube ingest: mark failed",
			slog.String("video_id", video.ID), slog.Any("err", err))
	}
}

// failureReason maps an internal error to a stable, operator-facing reason. The
// raw error is logged, never stored, so storage carries no internal detail.
func failureReason(cause error) string {
	if errors.Is(cause, ErrDownloadTooLarge) {
		return "video exceeds the maximum allowed size"
	}
	return "could not download or store the video"
}

// parseYouTubeID extracts the canonical 11-character video id from a YouTube
// URL, accepting the watch, youtu.be, shorts, embed, and live forms. Anything
// else is ErrInvalidYouTubeURL, so no work begins on a non-YouTube link.
func parseYouTubeID(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", ErrInvalidYouTubeURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidYouTubeURL
	}

	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var candidate string
	switch host {
	case "youtu.be":
		candidate = strings.TrimPrefix(u.Path, "/")
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		switch {
		case u.Path == "/watch":
			candidate = u.Query().Get("v")
		case strings.HasPrefix(u.Path, "/shorts/"):
			candidate = strings.TrimPrefix(u.Path, "/shorts/")
		case strings.HasPrefix(u.Path, "/embed/"):
			candidate = strings.TrimPrefix(u.Path, "/embed/")
		case strings.HasPrefix(u.Path, "/live/"):
			candidate = strings.TrimPrefix(u.Path, "/live/")
		}
	default:
		return "", ErrInvalidYouTubeURL
	}

	// Path forms may carry a trailing segment ("/shorts/<id>/"); keep the first.
	if i := strings.IndexByte(candidate, '/'); i >= 0 {
		candidate = candidate[:i]
	}
	if !youtubeIDRe.MatchString(candidate) {
		return "", ErrInvalidYouTubeURL
	}
	return candidate, nil
}

// canonicalYouTubeURL is the normalized watch URL for a video id; it is what the
// downloader is handed and what is stored as the record's source URL, so any of
// the accepted link forms collapses to one origin.
func canonicalYouTubeURL(id string) string {
	return "https://www.youtube.com/watch?v=" + id
}

// youtubeObjectKey is the storage key for a YouTube video, derived from its
// canonical id so the object is unique and the same link maps to one object.
func youtubeObjectKey(id string) string {
	return "youtube/" + id + ".mp4"
}
