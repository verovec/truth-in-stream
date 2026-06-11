package seed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// defaultSampleMediaURL is the media bytes the curated sample carries when no
// override is given: a stable, widely-mirrored public test clip. It does not
// match the demo transcript; an operator wanting matching fact-checks points
// SAMPLE_VIDEO_URL at their own clip.
const defaultSampleMediaURL = "https://storage.googleapis.com/gtv-videos-bucket/sample/BigBuckBunny.mp4"

// Sample is a curated sample video: the record to upsert and the URL its media
// bytes are fetched from when seeding online. It is the single source of truth
// for the sample gallery, owned by the seed command rather than the HTTP-facing
// service layer.
type Sample struct {
	Video    domain.Video
	MediaURL string
}

// Samples returns the curated samples to seed. A non-empty mediaURL (from
// SAMPLE_VIDEO_URL) overrides the default clip for the primary sample so an
// operator can supply a clip that matches the demo transcript.
func Samples(mediaURL string) []Sample {
	url := mediaURL
	if url == "" {
		url = defaultSampleMediaURL
	}
	return []Sample{
		{
			Video: domain.Video{
				Title:       "Common Myths",
				ObjectKey:   "samples/common-myths.mp4",
				ContentType: "video/mp4",
				Status:      domain.VideoStatusReady,
				Kind:        domain.VideoKindSample,
			},
			MediaURL: url,
		},
	}
}

// SampleVideoStore upserts curated sample records idempotently, keyed by object
// key so reseeding keeps a stable id. The postgres video store satisfies it.
type SampleVideoStore interface {
	UpsertSampleVideo(ctx context.Context, v domain.Video) (domain.Video, error)
}

// SampleMediaStore is the slice of object storage the sample seed writes to: it
// checks whether the object is already present and uploads the bytes when not.
// The S3/MinIO store satisfies it.
type SampleMediaStore interface {
	Exists(ctx context.Context, key string) (bool, error)
	Upload(ctx context.Context, key string, body io.Reader, contentType string, size int64) error
}

// MediaFetcher fetches sample media bytes from a URL. The HTTP fetcher wired in
// cmd/seed satisfies it; tests inject a fake with no network. The caller owns
// the returned reader and MUST close it.
type MediaFetcher interface {
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

// InsertSampleVideos upserts each curated sample record and, best-effort, places
// its media bytes in object storage. The record is always seeded; the media is
// fetched once, cached under cacheDir for later reseeds, and uploaded only when
// not already present. When the clip cannot be fetched and is not cached, the
// record is seeded without media and a warning is logged rather than failing,
// so an offline reseed never hard-fails. A storage failure, by contrast, is a
// real misconfiguration and is returned.
func InsertSampleVideos(ctx context.Context, store SampleVideoStore, media SampleMediaStore, fetcher MediaFetcher, cacheDir string, samples []Sample, logger *slog.Logger) error {
	for _, s := range samples {
		if err := seedSampleVideo(ctx, store, media, fetcher, cacheDir, s, logger); err != nil {
			return err
		}
	}
	return nil
}

func seedSampleVideo(ctx context.Context, store SampleVideoStore, media SampleMediaStore, fetcher MediaFetcher, cacheDir string, s Sample, logger *slog.Logger) error {
	v := s.Video
	// size is 0 when the media is skipped (offline, uncached); the store keeps a
	// previously recorded real size against that zero, so a skip never reports a
	// 0-byte sample once the object exists.
	size, err := ensureSampleMedia(ctx, media, fetcher, cacheDir, s, logger)
	if err != nil {
		return err
	}
	v.SizeBytes = size
	if _, err := store.UpsertSampleVideo(ctx, v); err != nil {
		return fmt.Errorf("seed: upsert sample %q: %w", v.ObjectKey, err)
	}
	return nil
}

// ensureSampleMedia resolves the sample's media bytes (from cache or a fetch),
// uploads them to storage if absent, and returns the byte size. It returns size
// 0 with a nil error when the clip is unavailable and uncached, so the caller
// seeds the record without media instead of failing; writeCache rejects empty
// bodies, so a non-zero size always means real bytes are present.
func ensureSampleMedia(ctx context.Context, media SampleMediaStore, fetcher MediaFetcher, cacheDir string, s Sample, logger *slog.Logger) (int64, error) {
	cachePath, size, err := cachedOrFetch(ctx, fetcher, cacheDir, s)
	if err != nil {
		logger.WarnContext(ctx, "sample media unavailable; seeding record without media bytes",
			slog.String("object_key", s.Video.ObjectKey),
			slog.String("url", s.MediaURL),
			slog.Any("err", err))
		return 0, nil
	}

	exists, err := media.Exists(ctx, s.Video.ObjectKey)
	if err != nil {
		return 0, fmt.Errorf("seed: check sample media %q: %w", s.Video.ObjectKey, err)
	}
	if !exists {
		if err := uploadSampleMedia(ctx, media, s.Video.ObjectKey, cachePath, s.Video.ContentType, size); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// cachedOrFetch returns the path to the sample's cached media and its size,
// fetching and caching it first when the cache is cold. A missing media URL or
// a failed fetch with no cache is an error the caller treats as a graceful skip.
func cachedOrFetch(ctx context.Context, fetcher MediaFetcher, cacheDir string, s Sample) (string, int64, error) {
	cachePath := filepath.Join(cacheDir, cacheFileName(s.Video.ObjectKey))
	if fi, err := os.Stat(cachePath); err == nil && fi.Size() > 0 {
		return cachePath, fi.Size(), nil
	}
	if s.MediaURL == "" {
		return "", 0, errors.New("no media url and no cached media")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("create media cache dir %q: %w", cacheDir, err)
	}
	rc, err := fetcher.Fetch(ctx, s.MediaURL)
	if err != nil {
		return "", 0, fmt.Errorf("fetch sample media: %w", err)
	}
	defer func() { _ = rc.Close() }()

	size, err := writeCache(cachePath, rc)
	if err != nil {
		return "", 0, err
	}
	return cachePath, size, nil
}

// cacheFileName derives a flat, collision-free cache filename from an object
// key by replacing path separators, so two samples under different prefixes
// (samples/a/clip.mp4 vs samples/b/clip.mp4) never share one cache file.
func cacheFileName(objectKey string) string {
	return strings.ReplaceAll(objectKey, "/", "_")
}

// writeCache streams r to a temp file in the cache directory and atomically
// renames it into place, returning the number of bytes written. An empty body
// is rejected so a truncated fetch never masquerades as cached media.
func writeCache(cachePath string, r io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), "media-*.part")
	if err != nil {
		return 0, fmt.Errorf("create media cache temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	n, err := io.Copy(tmp, r)
	if err != nil {
		return 0, fmt.Errorf("write sample media to cache: %w", err)
	}
	if n == 0 {
		return 0, errors.New("fetched sample media is empty")
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("flush sample media cache: %w", err)
	}
	if err := os.Rename(tmpName, cachePath); err != nil {
		return 0, fmt.Errorf("commit sample media cache: %w", err)
	}
	return n, nil
}

func uploadSampleMedia(ctx context.Context, media SampleMediaStore, key, cachePath, contentType string, size int64) error {
	f, err := os.Open(cachePath)
	if err != nil {
		return fmt.Errorf("seed: open cached sample media %q: %w", cachePath, err)
	}
	defer func() { _ = f.Close() }()
	if err := media.Upload(ctx, key, f, contentType, size); err != nil {
		return fmt.Errorf("seed: upload sample media %q: %w", key, err)
	}
	return nil
}
