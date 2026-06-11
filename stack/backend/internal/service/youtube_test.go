package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeDownloader writes canned bytes to a file in destDir and returns the result
// it is configured with. err, when set, fails the download. writeBytes are the
// file contents the upload path then reads back.
type fakeDownloader struct {
	result     domain.DownloadResult
	writeBytes []byte
	err        error

	mu      sync.Mutex
	calls   int
	gotURL  string
	gotDest string
}

func (d *fakeDownloader) Download(_ context.Context, videoURL, destDir string) (domain.DownloadResult, error) {
	d.mu.Lock()
	d.calls++
	d.gotURL = videoURL
	d.gotDest = destDir
	d.mu.Unlock()
	if d.err != nil {
		return domain.DownloadResult{}, d.err
	}
	path := filepath.Join(destDir, "video.mp4")
	if err := os.WriteFile(path, d.writeBytes, 0o600); err != nil {
		return domain.DownloadResult{}, err
	}
	res := d.result
	res.FilePath = path
	if res.SizeBytes == 0 {
		res.SizeBytes = int64(len(d.writeBytes))
	}
	return res, nil
}

func (d *fakeDownloader) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// fakeUploader records the last server-side upload.
type fakeUploader struct {
	mu      sync.Mutex
	err     error
	calls   int
	gotKey  string
	gotType string
	gotSize int64
	gotBody []byte
}

func (u *fakeUploader) Upload(_ context.Context, key string, body io.Reader, contentType string, size int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.err != nil {
		return u.err
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	u.gotKey = key
	u.gotType = contentType
	u.gotSize = size
	u.gotBody = b
	return nil
}

func (u *fakeUploader) snapshot() (int, string, string, int64, []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls, u.gotKey, u.gotType, u.gotSize, append([]byte(nil), u.gotBody...)
}

// newTestIngest builds an IngestService over fakes with an inline spawn so the
// background worker runs synchronously and assertions see the terminal state.
func newTestIngest(t *testing.T, store ingestStore, media mediaUploader, dl Downloader) *IngestService {
	t.Helper()
	svc, err := NewIngestService(store, media, dl, IngestConfig{
		MaxDownloadBytes: 1 << 20,
		DownloadTimeout:  5 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("NewIngestService: %v", err)
	}
	svc.spawn = func(f func()) { f() }
	return svc
}

func TestParseYouTubeID(t *testing.T) {
	t.Parallel()
	const id = "dQw4w9WgXcQ"
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "watch url", raw: "https://www.youtube.com/watch?v=" + id, want: id},
		{name: "watch url extra params", raw: "https://youtube.com/watch?v=" + id + "&t=42s", want: id},
		{name: "short youtu.be", raw: "https://youtu.be/" + id, want: id},
		{name: "shorts", raw: "https://www.youtube.com/shorts/" + id, want: id},
		{name: "embed", raw: "https://www.youtube.com/embed/" + id, want: id},
		{name: "live", raw: "https://www.youtube.com/live/" + id, want: id},
		{name: "mobile host", raw: "https://m.youtube.com/watch?v=" + id, want: id},
		{name: "http scheme", raw: "http://youtube.com/watch?v=" + id, want: id},
		{name: "non youtube host", raw: "https://vimeo.com/watch?v=" + id, wantErr: true},
		{name: "missing scheme", raw: "youtube.com/watch?v=" + id, wantErr: true},
		{name: "no video id", raw: "https://www.youtube.com/watch", wantErr: true},
		{name: "short id", raw: "https://youtu.be/abc", wantErr: true},
		{name: "playlist not video", raw: "https://www.youtube.com/playlist?list=PL123", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseYouTubeID(tc.raw)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidYouTubeURL) {
					t.Fatalf("err = %v, want ErrInvalidYouTubeURL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("id = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIngestSubmitRejectsBadURL(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	dl := &fakeDownloader{}
	svc := newTestIngest(t, store, &fakeUploader{}, dl)

	_, err := svc.Submit(t.Context(), "https://example.com/not-youtube")
	if !errors.Is(err, ErrInvalidYouTubeURL) {
		t.Fatalf("err = %v, want ErrInvalidYouTubeURL", err)
	}
	if len(store.created) != 0 {
		t.Errorf("a record was created for an invalid url: %+v", store.created)
	}
	if dl.callCount() != 0 {
		t.Errorf("downloader ran for an invalid url")
	}
}

func TestIngestSubmitDownloadsAndStores(t *testing.T) {
	t.Parallel()
	const id = "dQw4w9WgXcQ"
	store := newFakeVideoStore()
	up := &fakeUploader{}
	dl := &fakeDownloader{
		writeBytes: []byte("the video bytes"),
		result:     domain.DownloadResult{Title: "Never Gonna Give You Up", DurationMS: 213000},
	}
	svc := newTestIngest(t, store, up, dl)

	video, err := svc.Submit(t.Context(), "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// Submit returns the pending record regardless of when the worker finishes.
	if video.Status != domain.VideoStatusPending {
		t.Errorf("submitted status = %q, want pending", video.Status)
	}
	if video.Kind != domain.VideoKindYouTube {
		t.Errorf("kind = %q, want youtube", video.Kind)
	}
	if video.SourceID != id {
		t.Errorf("source id = %q, want %q", video.SourceID, id)
	}
	if video.ObjectKey != "youtube/"+id+".mp4" {
		t.Errorf("object key = %q, want youtube/%s.mp4", video.ObjectKey, id)
	}

	// The inline worker has run: the upload happened and the record is ready.
	calls, key, ctype, size, body := up.snapshot()
	if calls != 1 {
		t.Fatalf("upload calls = %d, want 1", calls)
	}
	if key != "youtube/"+id+".mp4" {
		t.Errorf("upload key = %q", key)
	}
	if ctype != "video/mp4" {
		t.Errorf("upload content type = %q, want video/mp4", ctype)
	}
	if want := int64(len("the video bytes")); size != want {
		t.Errorf("upload size = %d, want %d", size, want)
	}
	if !bytes.Equal(body, []byte("the video bytes")) {
		t.Errorf("upload body = %q", body)
	}

	final, err := store.GetVideoBySourceID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetVideoBySourceID: %v", err)
	}
	if final.Status != domain.VideoStatusReady {
		t.Errorf("final status = %q, want ready", final.Status)
	}
	if final.Title != "Never Gonna Give You Up" {
		t.Errorf("title = %q, want probed title", final.Title)
	}
	if final.DurationMS != 213000 {
		t.Errorf("duration = %d, want 213000", final.DurationMS)
	}
}

func TestIngestSubmitDedupes(t *testing.T) {
	t.Parallel()
	const id = "dQw4w9WgXcQ"
	store := newFakeVideoStore()
	up := &fakeUploader{}
	dl := &fakeDownloader{writeBytes: []byte("bytes"), result: domain.DownloadResult{Title: "Clip"}}
	svc := newTestIngest(t, store, up, dl)

	first, err := svc.Submit(t.Context(), "https://www.youtube.com/watch?v="+id)
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	// A different link form for the same video must resolve to the same record.
	second, err := svc.Submit(t.Context(), "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("dedupe failed: second id %q != first id %q", second.ID, first.ID)
	}
	if dl.callCount() != 1 {
		t.Errorf("downloader ran %d times, want 1 (no re-download on dedupe)", dl.callCount())
	}
	if len(store.created) != 1 {
		t.Errorf("created %d records, want 1", len(store.created))
	}
}

func TestIngestResubmitRetriesFailed(t *testing.T) {
	t.Parallel()
	const id = "dQw4w9WgXcQ"
	store := newFakeVideoStore()
	up := &fakeUploader{}
	dl := &fakeDownloader{err: errors.New("transient network error")}
	svc := newTestIngest(t, store, up, dl)

	// First submit fails the download, leaving the record failed.
	video, err := svc.Submit(t.Context(), "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if got, _ := store.GetVideo(t.Context(), video.ID); got.Status != domain.VideoStatusFailed {
		t.Fatalf("status after first submit = %q, want failed", got.Status)
	}

	// The downloader now works; re-submitting the same link retries the ingest.
	dl.err = nil
	dl.writeBytes = []byte("recovered bytes")
	dl.result = domain.DownloadResult{Title: "Recovered"}

	retried, err := svc.Submit(t.Context(), "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if retried.ID != video.ID {
		t.Errorf("retry created a new record: %q != %q", retried.ID, video.ID)
	}
	if calls, _, _, _, _ := up.snapshot(); calls != 1 {
		t.Errorf("upload calls = %d, want 1 after retry", calls)
	}
	final, err := store.GetVideo(t.Context(), video.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if final.Status != domain.VideoStatusReady {
		t.Errorf("status after retry = %q, want ready", final.Status)
	}
}

func TestIngestDownloadFailureMarksFailed(t *testing.T) {
	t.Parallel()
	const id = "dQw4w9WgXcQ"
	store := newFakeVideoStore()
	up := &fakeUploader{}
	dl := &fakeDownloader{err: errors.New("yt-dlp exited 1")}
	svc := newTestIngest(t, store, up, dl)

	video, err := svc.Submit(t.Context(), "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if calls, _, _, _, _ := up.snapshot(); calls != 0 {
		t.Errorf("upload ran despite download failure")
	}
	final, err := store.GetVideo(t.Context(), video.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if final.Status != domain.VideoStatusFailed {
		t.Errorf("status = %q, want failed", final.Status)
	}
	if final.Error == "" {
		t.Error("failed record carries no reason")
	}
}

func TestIngestOversizeMarksFailed(t *testing.T) {
	t.Parallel()
	const id = "dQw4w9WgXcQ"
	store := newFakeVideoStore()
	up := &fakeUploader{}
	// Report a size beyond the 1 MiB test bound; the file itself is small.
	dl := &fakeDownloader{writeBytes: []byte("small"), result: domain.DownloadResult{SizeBytes: 2 << 20}}
	svc := newTestIngest(t, store, up, dl)

	video, err := svc.Submit(t.Context(), "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if calls, _, _, _, _ := up.snapshot(); calls != 0 {
		t.Errorf("oversize video was uploaded")
	}
	final, err := store.GetVideo(t.Context(), video.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if final.Status != domain.VideoStatusFailed {
		t.Errorf("status = %q, want failed", final.Status)
	}
}

func TestNewIngestServiceValidation(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	up := &fakeUploader{}
	dl := &fakeDownloader{}
	tests := []struct {
		name string
		cfg  IngestConfig
	}{
		{name: "zero max bytes", cfg: IngestConfig{MaxDownloadBytes: 0, DownloadTimeout: time.Second}},
		{name: "negative max bytes", cfg: IngestConfig{MaxDownloadBytes: -1, DownloadTimeout: time.Second}},
		{name: "zero timeout", cfg: IngestConfig{MaxDownloadBytes: 1, DownloadTimeout: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewIngestService(store, up, dl, tc.cfg, nil); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
