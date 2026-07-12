package tvcapture

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeUploader struct {
	mu         sync.Mutex
	recordedAt time.Time
	sizeBytes  int64
	uploaded   []string
	registered []string
	nextVideo  int
}

func (f *fakeUploader) RequestUpload(_ context.Context, _ string, recordedAt time.Time, size int64) (recordingTicket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordedAt = recordedAt
	f.sizeBytes = size
	f.nextVideo++
	return recordingTicket{VideoID: "v" + string(rune('0'+f.nextVideo)), Upload: presignedRequest{URL: "https://store/put", Method: "PUT"}}, nil
}

func (f *fakeUploader) UploadFile(_ context.Context, _ recordingTicket, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploaded = append(f.uploaded, path)
	return nil
}

func (f *fakeUploader) Register(_ context.Context, videoID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, videoID)
	return nil
}

func testArchiver(up recordingUploader) *archiver {
	a := &archiver{uploader: up, ffmpegPath: "ffmpeg", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.remux = func(_ context.Context, _, mp4Path string) error {
		return os.WriteFile(mp4Path, []byte("remuxed-mp4-content"), 0o600)
	}
	return a
}

func writeSegment(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("ts-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArchiveHappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := writeSegment(t, dir, "20260713_090000.ts")

	up := &fakeUploader{}
	a := testArchiver(up)
	ch := Channel{ID: "c1", Slug: "tf1"}

	if err := a.archive(context.Background(), ch, ts); err != nil {
		t.Fatalf("archive: %v", err)
	}

	if want := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC); !up.recordedAt.Equal(want) {
		t.Fatalf("recordedAt = %v, want %v", up.recordedAt, want)
	}
	if up.sizeBytes != int64(len("remuxed-mp4-content")) {
		t.Fatalf("sizeBytes = %d", up.sizeBytes)
	}
	if len(up.uploaded) != 1 || len(up.registered) != 1 {
		t.Fatalf("uploaded=%v registered=%v", up.uploaded, up.registered)
	}
	if up.registered[0] != "v1" {
		t.Fatalf("registered video = %q", up.registered[0])
	}

	// Local files must be cleaned up.
	if _, err := os.Stat(ts); !os.IsNotExist(err) {
		t.Fatalf("ts not removed: %v", err)
	}
	mp4 := filepath.Join(dir, "20260713_090000.mp4")
	if _, err := os.Stat(mp4); !os.IsNotExist(err) {
		t.Fatalf("mp4 not removed: %v", err)
	}
}

func TestSalvageArchivesAllSegments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSegment(t, dir, "20260713_090000.ts")
	writeSegment(t, dir, "20260713_100000.ts")

	up := &fakeUploader{}
	a := testArchiver(up)
	if err := a.salvage(context.Background(), Channel{ID: "c1", Slug: "tf1"}, dir); err != nil {
		t.Fatalf("salvage: %v", err)
	}
	if len(up.registered) != 2 {
		t.Fatalf("registered %d segments, want 2", len(up.registered))
	}
	remaining, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
	if len(remaining) != 0 {
		t.Fatalf("segments left after salvage: %v", remaining)
	}
}
