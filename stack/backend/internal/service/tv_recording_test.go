package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func newTestTVRecordingService(t *testing.T, videos domain.VideoStore, channels TVRecordingChannelStore, media domain.MediaStore) *TVRecordingService {
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

func TestTVRecordingPruneRejectsNonPositiveRetention(t *testing.T) {
	t.Parallel()
	svc := newTestTVRecordingService(t, newFakeVideoStore(), newFakeTVChannelStore(), &fakeMediaStore{})
	if _, err := svc.Prune(context.Background(), 0); err == nil {
		t.Fatal("Prune accepted a zero retention window")
	}
}
