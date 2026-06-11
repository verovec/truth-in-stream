package postgres

import (
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func pendingYouTube(sourceID string) domain.Video {
	return domain.Video{
		Title:       "https://www.youtube.com/watch?v=" + sourceID,
		ObjectKey:   "youtube/" + sourceID + ".mp4",
		ContentType: "video/mp4",
		Status:      domain.VideoStatusPending,
		Kind:        domain.VideoKindYouTube,
		SourceURL:   "https://www.youtube.com/watch?v=" + sourceID,
		SourceID:    sourceID,
	}
}

func TestCreateYouTubeVideoDedupesBySourceID(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateYouTubeVideo(ctx, pendingYouTube("dQw4w9WgXcQ"))
	if err != nil {
		t.Fatalf("CreateYouTubeVideo: %v", err)
	}
	if created.SourceID != "dQw4w9WgXcQ" || created.Kind != domain.VideoKindYouTube {
		t.Fatalf("unexpected record: %+v", created)
	}

	// A second insert for the same canonical id is a no-op surfaced as the
	// duplicate sentinel.
	if _, err := store.CreateYouTubeVideo(ctx, pendingYouTube("dQw4w9WgXcQ")); !errors.Is(err, domain.ErrDuplicateSource) {
		t.Fatalf("second create err = %v, want ErrDuplicateSource", err)
	}

	got, err := store.GetVideoBySourceID(ctx, "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("GetVideoBySourceID: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("resolved id %q != created id %q", got.ID, created.ID)
	}
}

func TestGetVideoBySourceIDNotFound(t *testing.T) {
	store := setupStore(t)
	if _, err := store.GetVideoBySourceID(t.Context(), "missing12345"); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Fatalf("err = %v, want ErrVideoNotFound", err)
	}
}

func TestSetVideoReadyRecordsMetadata(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateYouTubeVideo(ctx, pendingYouTube("readyVideo01"))
	if err != nil {
		t.Fatalf("CreateYouTubeVideo: %v", err)
	}

	ready, err := store.SetVideoReady(ctx, created.ID, "Probed Title", 4096, 213000)
	if err != nil {
		t.Fatalf("SetVideoReady: %v", err)
	}
	if ready.Status != domain.VideoStatusReady {
		t.Errorf("status = %q, want ready", ready.Status)
	}
	if ready.Title != "Probed Title" || ready.SizeBytes != 4096 || ready.DurationMS != 213000 {
		t.Errorf("metadata not recorded: %+v", ready)
	}
}

func TestSetVideoFailedRecordsReason(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateYouTubeVideo(ctx, pendingYouTube("failVideo001"))
	if err != nil {
		t.Fatalf("CreateYouTubeVideo: %v", err)
	}

	failed, err := store.SetVideoFailed(ctx, created.ID, "could not download")
	if err != nil {
		t.Fatalf("SetVideoFailed: %v", err)
	}
	if failed.Status != domain.VideoStatusFailed {
		t.Errorf("status = %q, want failed", failed.Status)
	}
	if failed.Error != "could not download" {
		t.Errorf("error = %q, want reason recorded", failed.Error)
	}
}

func TestRetryFailedVideoClaimsOnlyFailed(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created, err := store.CreateYouTubeVideo(ctx, pendingYouTube("retryVideo01"))
	if err != nil {
		t.Fatalf("CreateYouTubeVideo: %v", err)
	}

	// A pending record cannot be retried.
	if _, err := store.RetryFailedVideo(ctx, created.ID); !errors.Is(err, domain.ErrIngestNotRetriable) {
		t.Fatalf("retry of pending err = %v, want ErrIngestNotRetriable", err)
	}

	if _, err := store.SetVideoFailed(ctx, created.ID, "boom"); err != nil {
		t.Fatalf("SetVideoFailed: %v", err)
	}

	// The first retry claims it (failed -> pending, error cleared).
	claimed, err := store.RetryFailedVideo(ctx, created.ID)
	if err != nil {
		t.Fatalf("RetryFailedVideo: %v", err)
	}
	if claimed.Status != domain.VideoStatusPending || claimed.Error != "" {
		t.Errorf("claimed record = %+v, want pending with no error", claimed)
	}

	// A concurrent second retry finds it already pending and does not claim it.
	if _, err := store.RetryFailedVideo(ctx, created.ID); !errors.Is(err, domain.ErrIngestNotRetriable) {
		t.Fatalf("second retry err = %v, want ErrIngestNotRetriable", err)
	}
}

func TestListVideosCarriesYouTubeFields(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if _, err := store.CreateYouTubeVideo(ctx, pendingYouTube("listVideo001")); err != nil {
		t.Fatalf("CreateYouTubeVideo: %v", err)
	}
	videos, err := store.ListVideos(ctx)
	if err != nil {
		t.Fatalf("ListVideos: %v", err)
	}
	var found bool
	for _, v := range videos {
		if v.SourceID == "listVideo001" {
			found = true
			if v.SourceURL == "" || v.Kind != domain.VideoKindYouTube {
				t.Errorf("youtube fields not listed: %+v", v)
			}
		}
	}
	if !found {
		t.Error("created youtube video missing from listing")
	}
}
