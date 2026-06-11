package seed

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeSampleStore struct {
	mu        sync.Mutex
	records   map[string]domain.Video
	upsertErr error
}

func newFakeSampleStore() *fakeSampleStore {
	return &fakeSampleStore{records: make(map[string]domain.Video)}
}

func (f *fakeSampleStore) UpsertSampleVideo(_ context.Context, v domain.Video) (domain.Video, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return domain.Video{}, f.upsertErr
	}
	if existing, ok := f.records[v.ObjectKey]; ok {
		v.ID = existing.ID
	} else {
		v.ID = "id-" + v.ObjectKey
	}
	f.records[v.ObjectKey] = v
	return v, nil
}

type fakeSampleMedia struct {
	mu        sync.Mutex
	objects   map[string][]byte
	uploads   int
	existsErr error
	uploadErr error
}

func newFakeSampleMedia() *fakeSampleMedia {
	return &fakeSampleMedia{objects: make(map[string][]byte)}
}

func (f *fakeSampleMedia) Exists(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.existsErr != nil {
		return false, f.existsErr
	}
	_, ok := f.objects[key]
	return ok, nil
}

func (f *fakeSampleMedia) Upload(_ context.Context, key string, body io.Reader, _ string, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return f.uploadErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return errors.New("upload size mismatch")
	}
	f.objects[key] = b
	f.uploads++
	return nil
}

type fakeFetcher struct {
	mu    sync.Mutex
	data  []byte
	err   error
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, _ string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

func oneSample() []Sample {
	return []Sample{{
		Video: domain.Video{
			Title:       "Common Myths",
			ObjectKey:   "samples/common-myths.mp4",
			ContentType: "video/mp4",
			Status:      domain.VideoStatusReady,
			Kind:        domain.VideoKindSample,
		},
		MediaURL: "https://example.test/clip.mp4",
	}}
}

func TestInsertSampleVideosFetchesAndUploads(t *testing.T) {
	t.Parallel()
	store := newFakeSampleStore()
	media := newFakeSampleMedia()
	fetcher := &fakeFetcher{data: []byte("hello world")}
	cacheDir := t.TempDir()
	samples := oneSample()

	if err := InsertSampleVideos(t.Context(), store, media, fetcher, cacheDir, samples, discardLogger()); err != nil {
		t.Fatalf("InsertSampleVideos: %v", err)
	}

	rec, ok := store.records["samples/common-myths.mp4"]
	if !ok {
		t.Fatal("sample record not upserted")
	}
	if rec.SizeBytes != int64(len("hello world")) {
		t.Errorf("SizeBytes = %d, want %d (real fetched bytes)", rec.SizeBytes, len("hello world"))
	}
	if got := media.objects["samples/common-myths.mp4"]; string(got) != "hello world" {
		t.Errorf("uploaded media = %q, want %q", got, "hello world")
	}
	if media.uploads != 1 {
		t.Errorf("uploads = %d, want 1", media.uploads)
	}
	// The fetched bytes are cached on disk for later reseeds.
	cached := filepath.Join(cacheDir, "samples_common-myths.mp4")
	if b, err := os.ReadFile(cached); err != nil || string(b) != "hello world" {
		t.Errorf("cache file = %q, err = %v; want %q", b, err, "hello world")
	}
}

func TestInsertSampleVideosReusesCache(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, "samples_common-myths.mp4"), []byte("cached bytes"), 0o644); err != nil {
		t.Fatalf("seed cache file: %v", err)
	}
	store := newFakeSampleStore()
	media := newFakeSampleMedia()
	fetcher := &fakeFetcher{err: errors.New("network must not be touched")}

	if err := InsertSampleVideos(t.Context(), store, media, fetcher, cacheDir, oneSample(), discardLogger()); err != nil {
		t.Fatalf("InsertSampleVideos: %v", err)
	}

	if fetcher.calls != 0 {
		t.Errorf("fetcher called %d times, want 0 (cache hit)", fetcher.calls)
	}
	rec := store.records["samples/common-myths.mp4"]
	if rec.SizeBytes != int64(len("cached bytes")) {
		t.Errorf("SizeBytes = %d, want %d (from cache)", rec.SizeBytes, len("cached bytes"))
	}
	if string(media.objects["samples/common-myths.mp4"]) != "cached bytes" {
		t.Error("cached media not uploaded to storage")
	}
}

func TestInsertSampleVideosGracefulSkipOffline(t *testing.T) {
	t.Parallel()
	store := newFakeSampleStore()
	media := newFakeSampleMedia()
	fetcher := &fakeFetcher{err: errors.New("dial tcp: no route to host")}
	cacheDir := t.TempDir()

	// No cache, host unreachable: the record is still seeded, media is skipped,
	// and the run does not hard-fail.
	if err := InsertSampleVideos(t.Context(), store, media, fetcher, cacheDir, oneSample(), discardLogger()); err != nil {
		t.Fatalf("InsertSampleVideos must not fail offline: %v", err)
	}

	rec, ok := store.records["samples/common-myths.mp4"]
	if !ok {
		t.Fatal("sample record not seeded on offline skip")
	}
	if rec.SizeBytes != 0 {
		t.Errorf("SizeBytes = %d, want 0 when media is skipped", rec.SizeBytes)
	}
	if media.uploads != 0 {
		t.Errorf("uploads = %d, want 0 when media is skipped", media.uploads)
	}
}

func TestInsertSampleVideosIdempotent(t *testing.T) {
	t.Parallel()
	store := newFakeSampleStore()
	media := newFakeSampleMedia()
	fetcher := &fakeFetcher{data: []byte("hello world")}
	cacheDir := t.TempDir()
	samples := oneSample()

	for range 2 {
		if err := InsertSampleVideos(t.Context(), store, media, fetcher, cacheDir, samples, discardLogger()); err != nil {
			t.Fatalf("InsertSampleVideos: %v", err)
		}
	}

	if len(store.records) != 1 {
		t.Errorf("stored %d records after two runs, want 1 (idempotent)", len(store.records))
	}
	// Second run: cache hit and object already present, so no re-fetch, no re-upload.
	if fetcher.calls != 1 {
		t.Errorf("fetcher called %d times, want 1 (second run reuses cache)", fetcher.calls)
	}
	if media.uploads != 1 {
		t.Errorf("uploads = %d, want 1 (second run sees object present)", media.uploads)
	}
}

func TestInsertSampleVideosSkipsUploadWhenPresent(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, "samples_common-myths.mp4"), []byte("cached bytes"), 0o644); err != nil {
		t.Fatalf("seed cache file: %v", err)
	}
	store := newFakeSampleStore()
	media := newFakeSampleMedia()
	media.objects["samples/common-myths.mp4"] = []byte("already there")
	fetcher := &fakeFetcher{err: errors.New("network must not be touched")}

	if err := InsertSampleVideos(t.Context(), store, media, fetcher, cacheDir, oneSample(), discardLogger()); err != nil {
		t.Fatalf("InsertSampleVideos: %v", err)
	}

	if media.uploads != 0 {
		t.Errorf("uploads = %d, want 0 when object already present", media.uploads)
	}
	rec := store.records["samples/common-myths.mp4"]
	if rec.SizeBytes != int64(len("cached bytes")) {
		t.Errorf("SizeBytes = %d, want %d (from cache even when upload skipped)", rec.SizeBytes, len("cached bytes"))
	}
}

func TestInsertSampleVideosPropagatesUpsertError(t *testing.T) {
	t.Parallel()
	store := newFakeSampleStore()
	store.upsertErr = errors.New("boom")
	media := newFakeSampleMedia()
	fetcher := &fakeFetcher{data: []byte("hello world")}

	if err := InsertSampleVideos(t.Context(), store, media, fetcher, t.TempDir(), oneSample(), discardLogger()); err == nil {
		t.Fatal("want upsert error, got nil")
	}
}

func TestInsertSampleVideosPropagatesStorageError(t *testing.T) {
	t.Parallel()
	store := newFakeSampleStore()
	media := newFakeSampleMedia()
	media.existsErr = errors.New("storage down")
	fetcher := &fakeFetcher{data: []byte("hello world")}

	// A storage failure is a real misconfiguration, not the offline-clip case:
	// it must surface, not be silently skipped.
	if err := InsertSampleVideos(t.Context(), store, media, fetcher, t.TempDir(), oneSample(), discardLogger()); err == nil {
		t.Fatal("want storage error, got nil")
	}
}

func TestSamplesDefaultURL(t *testing.T) {
	t.Parallel()
	withDefault := Samples("")
	if len(withDefault) == 0 {
		t.Fatal("no samples defined")
	}
	for _, s := range withDefault {
		if s.MediaURL == "" {
			t.Errorf("sample %q has no default media URL", s.Video.ObjectKey)
		}
	}

	override := Samples("https://example.test/custom.mp4")
	if override[0].MediaURL != "https://example.test/custom.mp4" {
		t.Errorf("MediaURL = %q, want the override", override[0].MediaURL)
	}
}

func TestSamplesAreValidRecords(t *testing.T) {
	t.Parallel()
	for _, s := range Samples("") {
		if !s.Video.Kind.Valid() || s.Video.Kind != domain.VideoKindSample {
			t.Errorf("sample %q kind = %q, want sample", s.Video.ObjectKey, s.Video.Kind)
		}
		if !s.Video.Status.Valid() || s.Video.Status != domain.VideoStatusReady {
			t.Errorf("sample %q status = %q, want ready", s.Video.ObjectKey, s.Video.Status)
		}
		if s.Video.Title == "" || s.Video.ObjectKey == "" || s.Video.ContentType == "" {
			t.Errorf("sample %q missing required fields: %+v", s.Video.ObjectKey, s.Video)
		}
	}
}
