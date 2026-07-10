package postgres

import (
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestCreateAndGetVideo(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateVideo(ctx, domain.Video{
		Title:       "My Upload",
		ObjectKey:   "uploads/one.mp4",
		ContentType: "video/mp4",
		SizeBytes:   4096,
		Status:      domain.VideoStatusPending,
		Kind:        domain.VideoKindUpload,
	})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateVideo returned an empty id")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: %+v", created)
	}

	got, err := store.GetVideo(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if got.ID != created.ID || got.Title != "My Upload" || got.ObjectKey != "uploads/one.mp4" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Status != domain.VideoStatusPending || got.Kind != domain.VideoKindUpload {
		t.Errorf("status/kind mismatch: %q/%q", got.Status, got.Kind)
	}
	if got.SizeBytes != 4096 {
		t.Errorf("size = %d, want 4096", got.SizeBytes)
	}
}

func TestGetVideoNotFound(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// A well-formed but absent UUID.
	if _, err := store.GetVideo(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("absent id err = %v, want ErrVideoNotFound", err)
	}
	// A malformed id is also not found, never a 500.
	if _, err := store.GetVideo(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("malformed id err = %v, want ErrVideoNotFound", err)
	}
}

func TestSetVideoStatus(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateVideo(ctx, domain.Video{
		Title: "Pending", ObjectKey: "uploads/p.mp4", ContentType: "video/mp4",
		SizeBytes: 1, Status: domain.VideoStatusPending, Kind: domain.VideoKindUpload,
	})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}

	updated, err := store.SetVideoStatus(ctx, created.ID, domain.VideoStatusReady)
	if err != nil {
		t.Fatalf("SetVideoStatus: %v", err)
	}
	if updated.Status != domain.VideoStatusReady {
		t.Errorf("status = %q, want ready", updated.Status)
	}

	if _, err := store.SetVideoStatus(ctx, "11111111-1111-1111-1111-111111111111", domain.VideoStatusReady); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("set on absent id err = %v, want ErrVideoNotFound", err)
	}
}

func TestSetVideoStatusRejectsInvalid(t *testing.T) {
	store := setupStore(t)
	created, err := store.CreateVideo(t.Context(), domain.Video{
		Title: "v", ObjectKey: "uploads/v.mp4", ContentType: "video/mp4",
		SizeBytes: 1, Status: domain.VideoStatusPending, Kind: domain.VideoKindUpload,
	})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	if _, err := store.SetVideoStatus(t.Context(), created.ID, "bogus"); err == nil {
		t.Fatal("SetVideoStatus with invalid status: want error, got nil")
	}
}

func TestCreateVideoRejectsInvalidEnum(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	tests := []struct {
		name  string
		video domain.Video
	}{
		{name: "bad kind", video: domain.Video{Title: "v", ObjectKey: "k1", ContentType: "video/mp4", Status: domain.VideoStatusPending, Kind: "bogus"}},
		{name: "bad status", video: domain.Video{Title: "v", ObjectKey: "k2", ContentType: "video/mp4", Status: "bogus", Kind: domain.VideoKindUpload}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.CreateVideo(ctx, tc.video); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestListVideosNewestFirst(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	for _, k := range []string{"uploads/a.mp4", "uploads/b.mp4", "uploads/c.mp4"} {
		if _, err := store.CreateVideo(ctx, domain.Video{
			Title: k, ObjectKey: k, ContentType: "video/mp4",
			SizeBytes: 1, Status: domain.VideoStatusPending, Kind: domain.VideoKindUpload,
		}); err != nil {
			t.Fatalf("CreateVideo %s: %v", k, err)
		}
	}

	got, err := store.ListVideos(ctx)
	if err != nil {
		t.Fatalf("ListVideos: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("listed %d videos, want 3", len(got))
	}
	// created_at DESC: the last inserted appears first (ties broken by id, but
	// the inserts here are sequential so timestamps differ).
	for i := 1; i < len(got); i++ {
		if got[i-1].CreatedAt.Before(got[i].CreatedAt) {
			t.Errorf("not ordered newest first: %v before %v", got[i-1].CreatedAt, got[i].CreatedAt)
		}
	}
}

func TestUpsertSampleVideoIsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	sample := domain.Video{
		Title: "Common Myths", ObjectKey: "samples/common-myths.mp4", ContentType: "video/mp4",
		SizeBytes: 0, Status: domain.VideoStatusReady, Kind: domain.VideoKindSample,
	}

	first, err := store.UpsertSampleVideo(ctx, sample)
	if err != nil {
		t.Fatalf("UpsertSampleVideo first: %v", err)
	}
	sample.Title = "Common Myths (updated)"
	second, err := store.UpsertSampleVideo(ctx, sample)
	if err != nil {
		t.Fatalf("UpsertSampleVideo second: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("upsert changed the id: %q -> %q", first.ID, second.ID)
	}
	if second.Title != "Common Myths (updated)" {
		t.Errorf("title not updated: %q", second.Title)
	}

	all, err := store.ListVideos(ctx)
	if err != nil {
		t.Fatalf("ListVideos: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("seeded the same sample twice produced %d rows, want 1", len(all))
	}
}

func TestUpsertSampleVideoKeepsKnownSizeAgainstZero(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// First seed records a real media size (the bytes were uploaded).
	sample := domain.Video{
		Title: "Common Myths", ObjectKey: "samples/common-myths.mp4", ContentType: "video/mp4",
		SizeBytes: 4096, Status: domain.VideoStatusReady, Kind: domain.VideoKindSample,
	}
	if _, err := store.UpsertSampleVideo(ctx, sample); err != nil {
		t.Fatalf("UpsertSampleVideo first: %v", err)
	}

	// An offline reseed with no cached media reseeds the record with size 0; the
	// object still exists in storage, so the known size must survive.
	sample.SizeBytes = 0
	second, err := store.UpsertSampleVideo(ctx, sample)
	if err != nil {
		t.Fatalf("UpsertSampleVideo second: %v", err)
	}
	if second.SizeBytes != 4096 {
		t.Errorf("SizeBytes = %d, want 4096 (a zero reseed must not clobber a known size)", second.SizeBytes)
	}

	// A later reseed with a real size still updates it.
	sample.SizeBytes = 8192
	third, err := store.UpsertSampleVideo(ctx, sample)
	if err != nil {
		t.Fatalf("UpsertSampleVideo third: %v", err)
	}
	if third.SizeBytes != 8192 {
		t.Errorf("SizeBytes = %d, want 8192 (a real size must still update)", third.SizeBytes)
	}
}

func TestDeleteVideo(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateVideo(ctx, domain.Video{
		Title:       "Doomed",
		ObjectKey:   "uploads/doomed.mp4",
		ContentType: "video/mp4",
		SizeBytes:   1024,
		Status:      domain.VideoStatusPending,
		Kind:        domain.VideoKindUpload,
	})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}

	if err := store.DeleteVideo(ctx, created.ID); err != nil {
		t.Fatalf("DeleteVideo: %v", err)
	}
	if _, err := store.GetVideo(ctx, created.ID); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("video survives delete: err = %v", err)
	}

	// A second delete, an unknown id, and a malformed id all report not-found:
	// none names a live record.
	if err := store.DeleteVideo(ctx, created.ID); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("second delete err = %v, want ErrVideoNotFound", err)
	}
	if err := store.DeleteVideo(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("unknown id err = %v, want ErrVideoNotFound", err)
	}
	if err := store.DeleteVideo(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("malformed id err = %v, want ErrVideoNotFound", err)
	}
}
