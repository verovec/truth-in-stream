package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// fakeVideoStore is an in-memory domain.VideoStore for service tests. Each
// *Err field, when set, makes the matching method fail.
type fakeVideoStore struct {
	videos    map[string]domain.Video
	nextID    int
	createErr error
	getErr    error
	listErr   error
	setErr    error
	upsertErr error

	created  []domain.Video
	upserts  []domain.Video
	setCalls []setCall
}

type setCall struct {
	id     string
	status domain.VideoStatus
}

func newFakeVideoStore() *fakeVideoStore {
	return &fakeVideoStore{videos: map[string]domain.Video{}}
}

func (f *fakeVideoStore) CreateVideo(_ context.Context, v domain.Video) (domain.Video, error) {
	if f.createErr != nil {
		return domain.Video{}, f.createErr
	}
	f.nextID++
	v.ID = fmt.Sprintf("video-%d", f.nextID)
	f.videos[v.ID] = v
	f.created = append(f.created, v)
	return v, nil
}

func (f *fakeVideoStore) GetVideo(_ context.Context, id string) (domain.Video, error) {
	if f.getErr != nil {
		return domain.Video{}, f.getErr
	}
	v, ok := f.videos[id]
	if !ok {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	return v, nil
}

func (f *fakeVideoStore) ListVideos(_ context.Context) ([]domain.Video, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.Video, 0, len(f.videos))
	for _, v := range f.videos {
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeVideoStore) SetVideoStatus(_ context.Context, id string, status domain.VideoStatus) (domain.Video, error) {
	f.setCalls = append(f.setCalls, setCall{id: id, status: status})
	if f.setErr != nil {
		return domain.Video{}, f.setErr
	}
	v, ok := f.videos[id]
	if !ok {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	v.Status = status
	f.videos[id] = v
	return v, nil
}

func (f *fakeVideoStore) UpsertSampleVideo(_ context.Context, v domain.Video) (domain.Video, error) {
	if f.upsertErr != nil {
		return domain.Video{}, f.upsertErr
	}
	// Idempotent by object key: reuse the existing id, otherwise assign one.
	for id, existing := range f.videos {
		if existing.ObjectKey == v.ObjectKey {
			v.ID = id
			f.videos[id] = v
			f.upserts = append(f.upserts, v)
			return v, nil
		}
	}
	f.nextID++
	v.ID = fmt.Sprintf("sample-%d", f.nextID)
	f.videos[v.ID] = v
	f.upserts = append(f.upserts, v)
	return v, nil
}

func (f *fakeVideoStore) CreateYouTubeVideo(_ context.Context, v domain.Video) (domain.Video, error) {
	if f.createErr != nil {
		return domain.Video{}, f.createErr
	}
	for _, existing := range f.videos {
		if existing.SourceID != "" && existing.SourceID == v.SourceID {
			return domain.Video{}, domain.ErrDuplicateSource
		}
	}
	f.nextID++
	v.ID = fmt.Sprintf("video-%d", f.nextID)
	f.videos[v.ID] = v
	f.created = append(f.created, v)
	return v, nil
}

func (f *fakeVideoStore) GetVideoBySourceID(_ context.Context, sourceID string) (domain.Video, error) {
	if f.getErr != nil {
		return domain.Video{}, f.getErr
	}
	for _, v := range f.videos {
		if v.SourceID == sourceID {
			return v, nil
		}
	}
	return domain.Video{}, domain.ErrVideoNotFound
}

func (f *fakeVideoStore) RetryFailedVideo(_ context.Context, id string) (domain.Video, error) {
	if f.setErr != nil {
		return domain.Video{}, f.setErr
	}
	v, ok := f.videos[id]
	if !ok || v.Status != domain.VideoStatusFailed {
		return domain.Video{}, domain.ErrIngestNotRetriable
	}
	v.Status = domain.VideoStatusPending
	v.Error = ""
	f.videos[id] = v
	return v, nil
}

func (f *fakeVideoStore) SetVideoReady(_ context.Context, id, title string, sizeBytes, durationMS int64) (domain.Video, error) {
	if f.setErr != nil {
		return domain.Video{}, f.setErr
	}
	v, ok := f.videos[id]
	if !ok {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	v.Status = domain.VideoStatusReady
	v.Title = title
	v.SizeBytes = sizeBytes
	v.DurationMS = durationMS
	v.Error = ""
	f.videos[id] = v
	return v, nil
}

func (f *fakeVideoStore) SetVideoFailed(_ context.Context, id, reason string) (domain.Video, error) {
	if f.setErr != nil {
		return domain.Video{}, f.setErr
	}
	v, ok := f.videos[id]
	if !ok {
		return domain.Video{}, domain.ErrVideoNotFound
	}
	v.Status = domain.VideoStatusFailed
	v.Error = reason
	f.videos[id] = v
	return v, nil
}

// fakeMediaStore is a domain.MediaStore that records keys and returns canned
// presigned requests.
type fakeMediaStore struct {
	exists               bool
	existsErr            error
	presignUploadErr     error
	presignUploadOnceErr error
	presignDownloadErr   error
	deleteErr            error

	uploadKey      string
	uploadOnceKey  string
	uploadOnceType string
	uploadOnceSize int64
	downloadKey    string
	existsKey      string
	existsCalls    int
	deletedKeys    []string
}

func (f *fakeMediaStore) PresignUpload(_ context.Context, key string) (domain.PresignedRequest, error) {
	if f.presignUploadErr != nil {
		return domain.PresignedRequest{}, f.presignUploadErr
	}
	f.uploadKey = key
	return domain.PresignedRequest{URL: "https://put/" + key, Method: "PUT"}, nil
}

func (f *fakeMediaStore) PresignUploadOnce(_ context.Context, key, contentType string, sizeBytes int64) (domain.PresignedRequest, error) {
	if f.presignUploadOnceErr != nil {
		return domain.PresignedRequest{}, f.presignUploadOnceErr
	}
	f.uploadOnceKey = key
	f.uploadOnceType = contentType
	f.uploadOnceSize = sizeBytes
	return domain.PresignedRequest{URL: "https://put/" + key, Method: "PUT"}, nil
}

func (f *fakeMediaStore) PresignDownload(_ context.Context, key string) (domain.PresignedRequest, error) {
	if f.presignDownloadErr != nil {
		return domain.PresignedRequest{}, f.presignDownloadErr
	}
	f.downloadKey = key
	return domain.PresignedRequest{URL: "https://get/" + key, Method: "GET"}, nil
}

func (f *fakeMediaStore) Exists(_ context.Context, key string) (bool, error) {
	f.existsCalls++
	f.existsKey = key
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.exists, nil
}

func (f *fakeMediaStore) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedKeys = append(f.deletedKeys, key)
	return nil
}

const testMaxUpload = 1 << 20

// newTestService builds a VideoService over the fakes with a deterministic
// object-key generator so assertions are stable.
func newTestService(t *testing.T, store domain.VideoStore, media domain.MediaStore) *VideoService {
	t.Helper()
	svc, err := NewVideoService(store, media, VideoConfig{MaxUploadBytes: testMaxUpload})
	if err != nil {
		t.Fatalf("NewVideoService: %v", err)
	}
	svc.newObjectKey = func(string) string { return "uploads/fixed-key.mp4" }
	return svc
}

func TestNewVideoServiceValidation(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	media := &fakeMediaStore{}
	tests := []struct {
		name  string
		store domain.VideoStore
		media domain.MediaStore
		max   int64
	}{
		{name: "nil store", store: nil, media: media, max: 1},
		{name: "nil media", store: store, media: nil, max: 1},
		{name: "zero max", store: store, media: media, max: 0},
		{name: "negative max", store: store, media: media, max: -5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewVideoService(tc.store, tc.media, VideoConfig{MaxUploadBytes: tc.max}); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestRequestUploadSuccess(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	media := &fakeMediaStore{}
	svc := newTestService(t, store, media)

	ticket, err := svc.RequestUpload(t.Context(), UploadRequest{
		Title:       "  My Clip  ",
		ContentType: "video/mp4",
		SizeBytes:   2048,
	})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	if ticket.Video.Title != "My Clip" {
		t.Errorf("title = %q, want trimmed %q", ticket.Video.Title, "My Clip")
	}
	if ticket.Video.Status != domain.VideoStatusPending {
		t.Errorf("status = %q, want pending", ticket.Video.Status)
	}
	if ticket.Video.Kind != domain.VideoKindUpload {
		t.Errorf("kind = %q, want upload", ticket.Video.Kind)
	}
	if ticket.Video.ObjectKey != "uploads/fixed-key.mp4" {
		t.Errorf("object key = %q, want generated key", ticket.Video.ObjectKey)
	}
	if ticket.Upload.URL != "https://put/uploads/fixed-key.mp4" {
		t.Errorf("upload URL = %q, want presigned PUT for the key", ticket.Upload.URL)
	}
	if media.uploadKey != ticket.Video.ObjectKey {
		t.Errorf("presigned key = %q, want %q", media.uploadKey, ticket.Video.ObjectKey)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d records, want 1", len(store.created))
	}
}

func TestRequestUploadRejectsBadRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  UploadRequest
		want error
	}{
		{name: "empty title", req: UploadRequest{Title: "  ", ContentType: "video/mp4", SizeBytes: 10}, want: ErrInvalidTitle},
		{name: "unsupported type", req: UploadRequest{Title: "t", ContentType: "application/zip", SizeBytes: 10}, want: ErrInvalidContentType},
		{name: "zero size", req: UploadRequest{Title: "t", ContentType: "video/mp4", SizeBytes: 0}, want: ErrInvalidSize},
		{name: "negative size", req: UploadRequest{Title: "t", ContentType: "video/mp4", SizeBytes: -1}, want: ErrInvalidSize},
		{name: "oversize", req: UploadRequest{Title: "t", ContentType: "video/mp4", SizeBytes: testMaxUpload + 1}, want: ErrInvalidSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeVideoStore()
			media := &fakeMediaStore{}
			svc := newTestService(t, store, media)
			_, err := svc.RequestUpload(t.Context(), tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(store.created) != 0 {
				t.Errorf("created %d records on a rejected request, want 0", len(store.created))
			}
			if media.uploadKey != "" {
				t.Errorf("presigned a key on a rejected request: %q", media.uploadKey)
			}
		})
	}
}

func TestRequestUploadPropagatesStoreError(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	store.createErr = errors.New("boom")
	svc := newTestService(t, store, &fakeMediaStore{})
	if _, err := svc.RequestUpload(t.Context(), UploadRequest{Title: "t", ContentType: "video/mp4", SizeBytes: 1}); err == nil {
		t.Fatal("want store error, got nil")
	}
}

func TestConfirmMarksReadyWhenObjectPresent(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	store.videos["v1"] = domain.Video{ID: "v1", ObjectKey: "uploads/k.mp4", Status: domain.VideoStatusPending, Kind: domain.VideoKindUpload}
	media := &fakeMediaStore{exists: true}
	svc := newTestService(t, store, media)

	got, err := svc.Confirm(t.Context(), "v1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got.Status != domain.VideoStatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if media.existsKey != "uploads/k.mp4" {
		t.Errorf("checked key = %q, want the record's object key", media.existsKey)
	}
	if len(store.setCalls) != 1 || store.setCalls[0].status != domain.VideoStatusReady {
		t.Errorf("set calls = %+v, want one ready transition", store.setCalls)
	}
}

func TestConfirmIsIdempotentWhenAlreadyReady(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	store.videos["v1"] = domain.Video{ID: "v1", ObjectKey: "uploads/k.mp4", Status: domain.VideoStatusReady, Kind: domain.VideoKindUpload}
	media := &fakeMediaStore{}
	svc := newTestService(t, store, media)

	got, err := svc.Confirm(t.Context(), "v1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got.Status != domain.VideoStatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if media.existsCalls != 0 {
		t.Errorf("Exists called %d times for an already-ready record, want 0", media.existsCalls)
	}
	if len(store.setCalls) != 0 {
		t.Errorf("status updated for an already-ready record: %+v", store.setCalls)
	}
}

func TestConfirmObjectNotUploaded(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	store.videos["v1"] = domain.Video{ID: "v1", ObjectKey: "uploads/k.mp4", Status: domain.VideoStatusPending, Kind: domain.VideoKindUpload}
	media := &fakeMediaStore{exists: false}
	svc := newTestService(t, store, media)

	_, err := svc.Confirm(t.Context(), "v1")
	if !errors.Is(err, ErrObjectNotUploaded) {
		t.Fatalf("err = %v, want ErrObjectNotUploaded", err)
	}
	if len(store.setCalls) != 0 {
		t.Errorf("status updated when object was missing: %+v", store.setCalls)
	}
}

func TestConfirmUnknownVideo(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, newFakeVideoStore(), &fakeMediaStore{})
	_, err := svc.Confirm(t.Context(), "missing")
	if !errors.Is(err, domain.ErrVideoNotFound) {
		t.Fatalf("err = %v, want ErrVideoNotFound", err)
	}
}

func TestGetReturnsPlaybackURL(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	store.videos["v1"] = domain.Video{ID: "v1", ObjectKey: "samples/c.mp4", Status: domain.VideoStatusReady, Kind: domain.VideoKindSample}
	media := &fakeMediaStore{}
	svc := newTestService(t, store, media)

	got, err := svc.Get(t.Context(), "v1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Video.ID != "v1" {
		t.Errorf("id = %q, want v1", got.Video.ID)
	}
	if got.Playback.URL != "https://get/samples/c.mp4" {
		t.Errorf("playback URL = %q, want presigned GET for the key", got.Playback.URL)
	}
}

func TestGetUnknownVideo(t *testing.T) {
	t.Parallel()
	svc := newTestService(t, newFakeVideoStore(), &fakeMediaStore{})
	if _, err := svc.Get(t.Context(), "missing"); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Fatalf("err = %v, want ErrVideoNotFound", err)
	}
}

func TestListPassesThrough(t *testing.T) {
	t.Parallel()
	store := newFakeVideoStore()
	store.videos["v1"] = domain.Video{ID: "v1", Kind: domain.VideoKindSample}
	store.videos["v2"] = domain.Video{ID: "v2", Kind: domain.VideoKindUpload}
	svc := newTestService(t, store, &fakeMediaStore{})

	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d videos, want 2", len(got))
	}
}

func TestUploadObjectKeyShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		contentType string
		wantSuffix  string
	}{
		{contentType: "video/mp4", wantSuffix: ".mp4"},
		{contentType: "video/webm", wantSuffix: ".webm"},
		{contentType: "video/unknown", wantSuffix: ""},
	}
	for _, tc := range tests {
		t.Run(tc.contentType, func(t *testing.T) {
			t.Parallel()
			key := uploadObjectKey(tc.contentType)
			if want := "uploads/"; len(key) < len(want) || key[:len(want)] != want {
				t.Errorf("key = %q, want uploads/ prefix", key)
			}
			if tc.wantSuffix != "" && key[len(key)-len(tc.wantSuffix):] != tc.wantSuffix {
				t.Errorf("key = %q, want %q suffix", key, tc.wantSuffix)
			}
			// Two calls must not collide.
			if key == uploadObjectKey(tc.contentType) {
				t.Errorf("object key is not unique across calls: %q", key)
			}
		})
	}
}

// guard ensures the fakes satisfy the ports they stand in for.
var (
	_ domain.VideoStore = (*fakeVideoStore)(nil)
	_ domain.MediaStore = (*fakeMediaStore)(nil)
)
