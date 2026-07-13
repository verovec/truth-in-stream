package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func newTestTVRecordingService(t *testing.T, videos tvRecordingVideoStore, channels TVRecordingChannelStore, media domain.MediaStore) *TVRecordingService {
	t.Helper()
	svc, err := NewTVRecordingService(videos, channels, media, 0)
	if err != nil {
		t.Fatalf("NewTVRecordingService: %v", err)
	}
	return svc
}

func seedChannel(t *testing.T, store *fakeTVChannelStore, slug, name string) domain.TVChannel {
	t.Helper()
	ch, err := store.CreateTVChannel(context.Background(), domain.TVChannel{
		Slug: slug, Name: name, SourceKind: domain.TVSourceYouTube, SourceRef: "https://youtube.com/x",
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return ch
}

func TestTVRecordingRequestUpload(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	channels := newFakeTVChannelStore()
	media := &fakeMediaStore{}
	svc := newTestTVRecordingService(t, videos, channels, media)
	ch := seedChannel(t, channels, "franceinfo", "franceinfo")

	recordedAt := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)
	ticket, err := svc.RequestUpload(context.Background(), TVRecordingRequest{
		ChannelID:   ch.ID,
		RecordedAt:  recordedAt,
		ContentType: "video/mp4",
		SizeBytes:   1_000_000,
	})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	const wantKey = "recordings/franceinfo/2026/07/10/200000.mp4"
	if ticket.Video.ObjectKey != wantKey {
		t.Errorf("object key = %q, want %q", ticket.Video.ObjectKey, wantKey)
	}
	if ticket.Video.Kind != domain.VideoKindTV {
		t.Errorf("kind = %q, want tv", ticket.Video.Kind)
	}
	if ticket.Video.Status != domain.VideoStatusPending {
		t.Errorf("status = %q, want pending", ticket.Video.Status)
	}
	if ticket.Video.ChannelID != ch.ID {
		t.Errorf("channel id = %q, want %q", ticket.Video.ChannelID, ch.ID)
	}
	if !ticket.Video.RecordedAt.Equal(recordedAt) {
		t.Errorf("recorded at = %v, want %v", ticket.Video.RecordedAt, recordedAt)
	}
	if want := "franceinfo - 2026-07-10 20:00"; ticket.Video.Title != want {
		t.Errorf("title = %q, want %q", ticket.Video.Title, want)
	}
	// The presign binds the exact key, type, and size (write-once).
	if media.uploadOnceKey != wantKey || media.uploadOnceType != "video/mp4" || media.uploadOnceSize != 1_000_000 {
		t.Errorf("presign once = (%q,%q,%d), want (%q,video/mp4,1000000)", media.uploadOnceKey, media.uploadOnceType, media.uploadOnceSize, wantKey)
	}
}

func TestTVRecordingRequestUploadIsIdempotent(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	channels := newFakeTVChannelStore()
	media := &fakeMediaStore{}
	svc := newTestTVRecordingService(t, videos, channels, media)
	ch := seedChannel(t, channels, "franceinfo", "franceinfo")

	req := TVRecordingRequest{
		ChannelID:   ch.ID,
		RecordedAt:  time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC),
		ContentType: "video/mp4",
		SizeBytes:   1_000_000,
	}

	first, err := svc.RequestUpload(context.Background(), req)
	if err != nil {
		t.Fatalf("first RequestUpload: %v", err)
	}
	// A re-archive of the same segment (worker retry/salvage) hits the same
	// deterministic key; it must resolve the existing row, not collide on it.
	second, err := svc.RequestUpload(context.Background(), req)
	if err != nil {
		t.Fatalf("second RequestUpload: %v", err)
	}
	if second.Video.ID != first.Video.ID {
		t.Fatalf("second video id = %q, want same as first %q", second.Video.ID, first.Video.ID)
	}
	if len(videos.created) != 1 {
		t.Fatalf("CreateVideo called %d times, want 1 (idempotent)", len(videos.created))
	}
	if second.Video.ObjectKey != first.Video.ObjectKey {
		t.Fatalf("object key drifted: %q vs %q", second.Video.ObjectKey, first.Video.ObjectKey)
	}
	// The second request still re-presigns so the worker can re-upload.
	if second.Upload.URL == "" {
		t.Fatal("idempotent request returned no presigned upload")
	}
}

func TestTVRecordingRequestUploadValidation(t *testing.T) {
	t.Parallel()
	channels := newFakeTVChannelStore()
	ch := seedChannel(t, channels, "bfmtv", "BFMTV")
	valid := TVRecordingRequest{ChannelID: ch.ID, RecordedAt: time.Now().UTC(), ContentType: "video/mp4", SizeBytes: 100}

	tests := []struct {
		name    string
		mutate  func(r *TVRecordingRequest)
		wantErr error
	}{
		{"missing recorded-at", func(r *TVRecordingRequest) { r.RecordedAt = time.Time{} }, ErrTVRecordingNoRecordedAt},
		{"bad content type", func(r *TVRecordingRequest) { r.ContentType = "video/webm" }, ErrTVRecordingInvalidContentType},
		{"zero size", func(r *TVRecordingRequest) { r.SizeBytes = 0 }, ErrTVRecordingInvalidSize},
		{"oversized", func(r *TVRecordingRequest) { r.SizeBytes = (16 << 30) + 1 }, ErrTVRecordingInvalidSize},
		{"unknown channel", func(r *TVRecordingRequest) { r.ChannelID = "nope" }, domain.ErrTVChannelNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newTestTVRecordingService(t, newFakeVideoStore(), channels, &fakeMediaStore{})
			req := valid
			tc.mutate(&req)
			if _, err := svc.RequestUpload(context.Background(), req); !errors.Is(err, tc.wantErr) {
				t.Fatalf("RequestUpload error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestTVRecordingRegister(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	channels := newFakeTVChannelStore()
	media := &fakeMediaStore{}
	svc := newTestTVRecordingService(t, videos, channels, media)
	ch := seedChannel(t, channels, "france24", "France 24")

	ticket, err := svc.RequestUpload(context.Background(), TVRecordingRequest{
		ChannelID: ch.ID, RecordedAt: time.Now().UTC(), ContentType: "video/mp4", SizeBytes: 500,
	})
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}

	// Object not yet present -> still pending, retriable.
	if _, err := svc.Register(context.Background(), ticket.Video.ID); !errors.Is(err, ErrObjectNotUploaded) {
		t.Fatalf("Register before upload = %v, want ErrObjectNotUploaded", err)
	}

	// Object present -> ready.
	media.exists = true
	got, err := svc.Register(context.Background(), ticket.Video.ID)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.Status != domain.VideoStatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}

	// Idempotent: already ready, no further storage round-trip.
	before := media.existsCalls
	if _, err := svc.Register(context.Background(), ticket.Video.ID); err != nil {
		t.Fatalf("Register idempotent: %v", err)
	}
	if media.existsCalls != before {
		t.Errorf("existsCalls = %d, want unchanged %d", media.existsCalls, before)
	}
}

func TestTVRecordingRegisterRejectsNonTV(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	upload, err := videos.CreateVideo(context.Background(), domain.Video{Kind: domain.VideoKindUpload, ObjectKey: "uploads/x.mp4"})
	if err != nil {
		t.Fatalf("seed upload: %v", err)
	}
	svc := newTestTVRecordingService(t, videos, newFakeTVChannelStore(), &fakeMediaStore{exists: true})
	if _, err := svc.Register(context.Background(), upload.ID); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Fatalf("Register non-tv = %v, want ErrVideoNotFound", err)
	}
}

func TestTVRecordingPrune(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	media := &fakeMediaStore{}
	svc := newTestTVRecordingService(t, videos, newFakeTVChannelStore(), media)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	mk := func(kind domain.VideoKind, recordedAt time.Time, key string) domain.Video {
		v, err := videos.CreateVideo(context.Background(), domain.Video{Kind: kind, RecordedAt: recordedAt, ObjectKey: key})
		if err != nil {
			t.Fatalf("seed video: %v", err)
		}
		return v
	}
	old := mk(domain.VideoKindTV, now.Add(-31*24*time.Hour), "recordings/a/old.mp4")
	recent := mk(domain.VideoKindTV, now.Add(-1*24*time.Hour), "recordings/a/recent.mp4")
	upload := mk(domain.VideoKindUpload, now.Add(-90*24*time.Hour), "uploads/keep.mp4") // never tv, never pruned

	deleted, err := svc.Prune(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := videos.GetVideo(context.Background(), old.ID); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("old recording still present")
	}
	if _, err := videos.GetVideo(context.Background(), recent.ID); err != nil {
		t.Errorf("recent recording pruned: %v", err)
	}
	if _, err := videos.GetVideo(context.Background(), upload.ID); err != nil {
		t.Errorf("upload pruned: %v", err)
	}
	if len(media.deletedKeys) != 1 || media.deletedKeys[0] != "recordings/a/old.mp4" {
		t.Errorf("deleted keys = %v, want [recordings/a/old.mp4]", media.deletedKeys)
	}
}

// failDeleteMedia is a media store whose Delete fails for one key and delegates
// the rest, so a prune test can prove one un-deletable object does not wedge the
// deletion of the others.
type failDeleteMedia struct {
	*fakeMediaStore
	failKey string
}

func (m *failDeleteMedia) Delete(ctx context.Context, key string) error {
	if key == m.failKey {
		return errors.New("storage delete boom")
	}
	return m.fakeMediaStore.Delete(ctx, key)
}

func TestTVRecordingPruneContinuesPastDeleteError(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	base := &fakeMediaStore{}
	media := &failDeleteMedia{fakeMediaStore: base, failKey: "recordings/a/stuck.mp4"}
	svc := newTestTVRecordingService(t, videos, newFakeTVChannelStore(), media)
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	mk := func(recordedAt time.Time, key string) domain.Video {
		v, err := videos.CreateVideo(context.Background(), domain.Video{Kind: domain.VideoKindTV, RecordedAt: recordedAt, ObjectKey: key})
		if err != nil {
			t.Fatalf("seed video: %v", err)
		}
		return v
	}
	stuck := mk(now.Add(-40*24*time.Hour), "recordings/a/stuck.mp4")
	ok1 := mk(now.Add(-39*24*time.Hour), "recordings/a/ok1.mp4")
	ok2 := mk(now.Add(-38*24*time.Hour), "recordings/a/ok2.mp4")

	deleted, err := svc.Prune(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Prune returned error, want nil (skip-and-continue): %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (the two deletable recordings)", deleted)
	}
	// The un-deletable recording's row stays for a future retry.
	if _, err := videos.GetVideo(context.Background(), stuck.ID); err != nil {
		t.Errorf("stuck recording row removed despite failed object delete: %v", err)
	}
	// The other two rows are gone.
	if _, err := videos.GetVideo(context.Background(), ok1.ID); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("ok1 not pruned")
	}
	if _, err := videos.GetVideo(context.Background(), ok2.ID); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("ok2 not pruned")
	}
}

func TestTVRecordingPruneRejectsNonPositiveRetention(t *testing.T) {
	t.Parallel()
	svc := newTestTVRecordingService(t, newFakeVideoStore(), newFakeTVChannelStore(), &fakeMediaStore{})
	if _, err := svc.Prune(context.Background(), 0); err == nil {
		t.Fatal("Prune accepted a zero retention window")
	}
}

func TestTVRecordingListRecordings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	videos := newFakeVideoStore()

	base := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC)
	// Two ready recordings for the target channel, one pending (excluded), and
	// one ready recording for another channel (excluded).
	if _, err := videos.CreateVideo(ctx, domain.Video{Kind: domain.VideoKindTV, ChannelID: "chan-1", Status: domain.VideoStatusReady, RecordedAt: base}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := videos.CreateVideo(ctx, domain.Video{Kind: domain.VideoKindTV, ChannelID: "chan-1", Status: domain.VideoStatusReady, RecordedAt: base.Add(time.Hour)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := videos.CreateVideo(ctx, domain.Video{Kind: domain.VideoKindTV, ChannelID: "chan-1", Status: domain.VideoStatusPending, RecordedAt: base.Add(2 * time.Hour)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := videos.CreateVideo(ctx, domain.Video{Kind: domain.VideoKindTV, ChannelID: "chan-2", Status: domain.VideoStatusReady, RecordedAt: base}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := newTestTVRecordingService(t, videos, newFakeTVChannelStore(), &fakeMediaStore{})
	got, err := svc.ListRecordings(ctx, "chan-1")
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recordings = %d, want 2 (only ready, only chan-1)", len(got))
	}
	// Newest first.
	if !got[0].RecordedAt.After(got[1].RecordedAt) {
		t.Fatalf("order = %v,%v, want newest first", got[0].RecordedAt, got[1].RecordedAt)
	}

	empty, err := svc.ListRecordings(ctx, "chan-unknown")
	if err != nil {
		t.Fatalf("ListRecordings unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("unknown channel recordings = %d, want 0", len(empty))
	}
}

func TestTVRecordingListRecordingsWrapsStoreError(t *testing.T) {
	t.Parallel()
	videos := newFakeVideoStore()
	videos.listErr = errors.New("boom")
	svc := newTestTVRecordingService(t, videos, newFakeTVChannelStore(), &fakeMediaStore{})
	if _, err := svc.ListRecordings(context.Background(), "chan-1"); err == nil {
		t.Fatal("ListRecordings swallowed a store error")
	}
}
