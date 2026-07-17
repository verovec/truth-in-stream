package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// createVideoForAnalysis inserts a video in the given upload status and
// returns it.
func createVideoForAnalysis(ctx context.Context, t *testing.T, store *Store, key string, status domain.VideoStatus) domain.Video {
	t.Helper()
	created, err := store.CreateVideo(ctx, domain.Video{
		Title:       "Analysable",
		ObjectKey:   key,
		ContentType: "video/mp4",
		SizeBytes:   1,
		Status:      status,
		Kind:        domain.VideoKindUpload,
	})
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	return created
}

func analysisFor(videoID string, events string) domain.VideoAnalysis {
	return domain.VideoAnalysis{
		VideoID:            videoID,
		SnapshotVersion:    1,
		Events:             []byte(events),
		Engine:             []byte(`{"transcriber":"u3-rt-pro"}`),
		ClaimsTotal:        3,
		ClaimsCredible:     1,
		ClaimsDisputed:     1,
		ClaimsUnverifiable: 1,
	}
}

func TestVideoAnalysisLifecycleDefaults(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	created := createVideoForAnalysis(ctx, t, store, "uploads/defaults.mp4", domain.VideoStatusReady)
	if created.AnalysisStatus != domain.VideoAnalysisNone {
		t.Errorf("new video analysis status = %q, want none", created.AnalysisStatus)
	}
	if created.AnalysisRuns != 0 || created.AnalysisProgressMS != 0 || !created.AnalyzedAt.IsZero() || created.AnalysisError != "" {
		t.Errorf("new video analysis fields not zero: %+v", created)
	}

	listed, err := store.ListVideos(ctx)
	if err != nil {
		t.Fatalf("ListVideos: %v", err)
	}
	if len(listed) != 1 || listed[0].AnalysisStatus != domain.VideoAnalysisNone {
		t.Errorf("list should carry the analysis flag, got %+v", listed)
	}
}

func TestStartVideoAnalysisTransitions(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	t.Run("ready video is claimed and progress reset", func(t *testing.T) {
		v := createVideoForAnalysis(ctx, t, store, "uploads/claim.mp4", domain.VideoStatusReady)
		if err := store.SetVideoAnalysisProgress(ctx, v.ID, 5000); err != nil {
			t.Fatalf("SetVideoAnalysisProgress: %v", err)
		}
		locked, err := store.StartVideoAnalysis(ctx, v.ID)
		if err != nil {
			t.Fatalf("StartVideoAnalysis: %v", err)
		}
		if locked.AnalysisStatus != domain.VideoAnalysisAnalysing {
			t.Errorf("status = %q, want analysing", locked.AnalysisStatus)
		}
		if locked.AnalysisProgressMS != 0 {
			t.Errorf("progress = %d, want reset to 0", locked.AnalysisProgressMS)
		}
	})

	t.Run("analysing video rejects a concurrent claim", func(t *testing.T) {
		v := createVideoForAnalysis(ctx, t, store, "uploads/locked.mp4", domain.VideoStatusReady)
		if _, err := store.StartVideoAnalysis(ctx, v.ID); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if _, err := store.StartVideoAnalysis(ctx, v.ID); !errors.Is(err, domain.ErrVideoAnalysisInProgress) {
			t.Errorf("second claim err = %v, want ErrVideoAnalysisInProgress", err)
		}
	})

	t.Run("pending upload is not analysable", func(t *testing.T) {
		v := createVideoForAnalysis(ctx, t, store, "uploads/pending.mp4", domain.VideoStatusPending)
		if _, err := store.StartVideoAnalysis(ctx, v.ID); !errors.Is(err, domain.ErrVideoNotReady) {
			t.Errorf("pending err = %v, want ErrVideoNotReady", err)
		}
	})

	t.Run("unknown and malformed ids are not found", func(t *testing.T) {
		if _, err := store.StartVideoAnalysis(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, domain.ErrVideoNotFound) {
			t.Errorf("absent id err = %v, want ErrVideoNotFound", err)
		}
		if _, err := store.StartVideoAnalysis(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrVideoNotFound) {
			t.Errorf("malformed id err = %v, want ErrVideoNotFound", err)
		}
	})

	t.Run("failed analysis re-runs and clears its error", func(t *testing.T) {
		v := createVideoForAnalysis(ctx, t, store, "uploads/failed.mp4", domain.VideoStatusReady)
		if _, err := store.StartVideoAnalysis(ctx, v.ID); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := store.FailVideoAnalysis(ctx, v.ID, "provider unreachable"); err != nil {
			t.Fatalf("FailVideoAnalysis: %v", err)
		}
		failed, err := store.GetVideo(ctx, v.ID)
		if err != nil {
			t.Fatalf("GetVideo: %v", err)
		}
		if failed.AnalysisStatus != domain.VideoAnalysisFailed || failed.AnalysisError != "provider unreachable" {
			t.Errorf("failed record = %q/%q, want failed/provider unreachable", failed.AnalysisStatus, failed.AnalysisError)
		}
		reclaimed, err := store.StartVideoAnalysis(ctx, v.ID)
		if err != nil {
			t.Fatalf("re-claim after failure: %v", err)
		}
		if reclaimed.AnalysisStatus != domain.VideoAnalysisAnalysing || reclaimed.AnalysisError != "" {
			t.Errorf("re-claimed record = %q/%q, want analysing with a cleared error", reclaimed.AnalysisStatus, reclaimed.AnalysisError)
		}
	})
}

func TestCompleteVideoAnalysisUpsertAndCounters(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	v := createVideoForAnalysis(ctx, t, store, "uploads/complete.mp4", domain.VideoStatusReady)
	if _, err := store.StartVideoAnalysis(ctx, v.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	stored, err := store.CompleteVideoAnalysis(ctx, analysisFor(v.ID, `[{"Kind":"subtitle"}]`))
	if err != nil {
		t.Fatalf("CompleteVideoAnalysis: %v", err)
	}
	if stored.ClaimsTotal != 3 || stored.ClaimsCredible != 1 || stored.ClaimsDisputed != 1 || stored.ClaimsUnverifiable != 1 {
		t.Errorf("stored counters = %+v, want 3/1/1/1", stored)
	}
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: %+v", stored)
	}

	completed, err := store.GetVideo(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if completed.AnalysisStatus != domain.VideoAnalysisComplete || completed.AnalysisRuns != 1 || completed.AnalyzedAt.IsZero() {
		t.Errorf("completed lifecycle = %q/%d/%s, want complete/1/stamped", completed.AnalysisStatus, completed.AnalysisRuns, completed.AnalyzedAt)
	}

	// A re-analysis overwrites the single row and counts the run.
	second := analysisFor(v.ID, `[{"Kind":"subtitle"},{"Kind":"result"}]`)
	second.ClaimsTotal = 5
	if _, err := store.StartVideoAnalysis(ctx, v.ID); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if _, err := store.CompleteVideoAnalysis(ctx, second); err != nil {
		t.Fatalf("re-complete: %v", err)
	}
	got, err := store.GetVideoAnalysis(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetVideoAnalysis: %v", err)
	}
	if got.ClaimsTotal != 5 || string(got.Events) != `[{"Kind": "subtitle"}, {"Kind": "result"}]` {
		t.Errorf("overwritten analysis = total %d events %s", got.ClaimsTotal, got.Events)
	}
	rerun, err := store.GetVideo(ctx, v.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if rerun.AnalysisRuns != 2 {
		t.Errorf("runs = %d, want 2", rerun.AnalysisRuns)
	}

	t.Run("unknown video is not found and stores nothing", func(t *testing.T) {
		if _, err := store.CompleteVideoAnalysis(ctx, analysisFor("11111111-1111-1111-1111-111111111111", `[{}]`)); !errors.Is(err, domain.ErrVideoNotFound) {
			t.Errorf("err = %v, want ErrVideoNotFound", err)
		}
	})

	t.Run("empty events are rejected", func(t *testing.T) {
		if _, err := store.CompleteVideoAnalysis(ctx, analysisFor(v.ID, "")); err == nil {
			t.Error("empty events should be rejected")
		}
	})
}

func TestGetVideoAnalysisNotFound(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if _, err := store.GetVideoAnalysis(ctx, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, domain.ErrVideoAnalysisNotFound) {
		t.Errorf("absent err = %v, want ErrVideoAnalysisNotFound", err)
	}
	if _, err := store.GetVideoAnalysis(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrVideoAnalysisNotFound) {
		t.Errorf("malformed err = %v, want ErrVideoAnalysisNotFound", err)
	}
}

func TestRecoverInterruptedVideoAnalyses(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	orphaned := createVideoForAnalysis(ctx, t, store, "uploads/orphan.mp4", domain.VideoStatusReady)
	if _, err := store.StartVideoAnalysis(ctx, orphaned.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	untouched := createVideoForAnalysis(ctx, t, store, "uploads/untouched.mp4", domain.VideoStatusReady)

	recovered, err := store.RecoverInterruptedVideoAnalyses(ctx)
	if err != nil {
		t.Fatalf("RecoverInterruptedVideoAnalyses: %v", err)
	}
	if !slices.Contains(recovered, orphaned.ID) || len(recovered) != 1 {
		t.Errorf("recovered = %v, want exactly [%s]", recovered, orphaned.ID)
	}

	got, err := store.GetVideo(ctx, orphaned.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if got.AnalysisStatus != domain.VideoAnalysisFailed || got.AnalysisError == "" {
		t.Errorf("orphan = %q/%q, want failed with a reason", got.AnalysisStatus, got.AnalysisError)
	}
	other, err := store.GetVideo(ctx, untouched.ID)
	if err != nil {
		t.Fatalf("GetVideo: %v", err)
	}
	if other.AnalysisStatus != domain.VideoAnalysisNone {
		t.Errorf("untouched video flipped to %q", other.AnalysisStatus)
	}
}

func TestDeleteVideoCascadesAnalysis(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	v := createVideoForAnalysis(ctx, t, store, "uploads/cascade.mp4", domain.VideoStatusReady)
	if _, err := store.CompleteVideoAnalysis(ctx, analysisFor(v.ID, `[{}]`)); err != nil {
		t.Fatalf("CompleteVideoAnalysis: %v", err)
	}
	if err := store.DeleteVideo(ctx, v.ID); err != nil {
		t.Fatalf("DeleteVideo: %v", err)
	}
	if _, err := store.GetVideoAnalysis(ctx, v.ID); !errors.Is(err, domain.ErrVideoAnalysisNotFound) {
		t.Errorf("analysis survived the video delete: %v", err)
	}
}
