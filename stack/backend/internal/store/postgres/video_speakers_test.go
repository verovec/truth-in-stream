package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

var ignoreSpeakerTimestamps = cmpopts.IgnoreFields(domain.VideoSpeaker{}, "UpdatedAt")

func proposedSpeaker(videoID, label, name string, role domain.SpeakerRole) domain.VideoSpeaker {
	speaker := domain.VideoSpeaker{
		VideoID: videoID, Label: label, Name: name, Role: role,
		Method: domain.SpeakerMethodNone, Evidence: []domain.SpeakerEvidence{},
	}
	if name != "" {
		speaker.Confidence = 0.9
		speaker.Method = domain.SpeakerMethodSignals
		speaker.Evidence = []domain.SpeakerEvidence{
			{Kind: domain.SpeakerEvidenceHandoff, Quote: "Madame Durand, vous avez deux minutes.", At: 90500 * time.Millisecond},
		}
	}
	return speaker
}

func TestVideoSpeakersReplaceAndList(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	video := createVideoForAnalysis(ctx, t, store, "videos/speakers-1.mp4", domain.VideoStatusReady)

	empty, err := store.ListVideoSpeakers(ctx, video.ID)
	if err != nil || len(empty) != 0 {
		t.Fatalf("ListVideoSpeakers before naming = %v, %v; want empty", empty, err)
	}

	first := []domain.VideoSpeaker{
		proposedSpeaker(video.ID, "B", "", domain.SpeakerRoleModerator),
		proposedSpeaker(video.ID, "A", "Marine Durand", domain.SpeakerRolePrincipal),
		proposedSpeaker(video.ID, "C", "Paul Lefèvre", domain.SpeakerRolePrincipal),
	}
	if err := store.ReplaceProposedVideoSpeakers(ctx, video.ID, first); err != nil {
		t.Fatalf("ReplaceProposedVideoSpeakers: %v", err)
	}
	got, err := store.ListVideoSpeakers(ctx, video.ID)
	if err != nil {
		t.Fatalf("ListVideoSpeakers: %v", err)
	}
	want := []domain.VideoSpeaker{first[1], first[0], first[2]}
	if diff := cmp.Diff(want, got, ignoreSpeakerTimestamps); diff != "" {
		t.Errorf("stored proposals mismatch (-want +got):\n%s", diff)
	}

	// A re-run replaces the step's own rows, including labels it no longer
	// produces.
	second := []domain.VideoSpeaker{proposedSpeaker(video.ID, "A", "", domain.SpeakerRolePrincipal)}
	if err := store.ReplaceProposedVideoSpeakers(ctx, video.ID, second); err != nil {
		t.Fatalf("ReplaceProposedVideoSpeakers (re-run): %v", err)
	}
	got, err = store.ListVideoSpeakers(ctx, video.ID)
	if err != nil {
		t.Fatalf("ListVideoSpeakers: %v", err)
	}
	if diff := cmp.Diff(second, got, ignoreSpeakerTimestamps); diff != "" {
		t.Errorf("re-run mismatch (-want +got):\n%s", diff)
	}
}

func TestVideoSpeakersOperatorOverrideSurvivesReRun(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	video := createVideoForAnalysis(ctx, t, store, "videos/speakers-2.mp4", domain.VideoStatusReady)

	proposals := []domain.VideoSpeaker{
		proposedSpeaker(video.ID, "A", "Marine Durand", domain.SpeakerRolePrincipal),
		proposedSpeaker(video.ID, "B", "Paul Lefèvre", domain.SpeakerRolePrincipal),
	}
	if err := store.ReplaceProposedVideoSpeakers(ctx, video.ID, proposals); err != nil {
		t.Fatalf("ReplaceProposedVideoSpeakers: %v", err)
	}

	set, err := store.SetOperatorVideoSpeaker(ctx, video.ID, "A", "Sophie Garnier", domain.SpeakerRoleModerator)
	if err != nil {
		t.Fatalf("SetOperatorVideoSpeaker: %v", err)
	}
	wantSet := domain.VideoSpeaker{
		VideoID: video.ID, Label: "A", Name: "Sophie Garnier", Role: domain.SpeakerRoleModerator,
		Confidence: 1, Method: domain.SpeakerMethodOperator, Evidence: proposals[0].Evidence, OperatorSet: true,
	}
	if diff := cmp.Diff(wantSet, set, ignoreSpeakerTimestamps); diff != "" {
		t.Errorf("operator row mismatch (-want +got):\n%s", diff)
	}

	// The operator may also decide a label the step never stored.
	if _, err := store.SetOperatorVideoSpeaker(ctx, video.ID, "Z", "Thomas", domain.SpeakerRoleGuest); err != nil {
		t.Fatalf("SetOperatorVideoSpeaker (new label): %v", err)
	}

	rerun := []domain.VideoSpeaker{
		proposedSpeaker(video.ID, "A", "Marine Durand", domain.SpeakerRolePrincipal),
		proposedSpeaker(video.ID, "B", "", domain.SpeakerRolePrincipal),
	}
	if err := store.ReplaceProposedVideoSpeakers(ctx, video.ID, rerun); err != nil {
		t.Fatalf("ReplaceProposedVideoSpeakers (re-run): %v", err)
	}
	got, err := store.ListVideoSpeakers(ctx, video.ID)
	if err != nil {
		t.Fatalf("ListVideoSpeakers: %v", err)
	}
	names := make(map[string]string, len(got))
	for _, s := range got {
		names[s.Label] = s.Name
		if (s.Label == "A" || s.Label == "Z") != s.OperatorSet {
			t.Errorf("label %s: operator_set = %v", s.Label, s.OperatorSet)
		}
	}
	if diff := cmp.Diff(map[string]string{"A": "Sophie Garnier", "B": "", "Z": "Thomas"}, names); diff != "" {
		t.Errorf("names after re-run mismatch (-want +got):\n%s", diff)
	}
}

// TestCompleteVideoAnalysisClearsProposedSpeakers pins the re-analysis rule:
// diarization labels are assigned afresh on every run, so storing a new
// analysis drops the names proposed for the previous one and keeps only what
// an operator set.
func TestCompleteVideoAnalysisClearsProposedSpeakers(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	video := createVideoForAnalysis(ctx, t, store, "videos/speakers-4.mp4", domain.VideoStatusReady)
	if err := store.ReplaceProposedVideoSpeakers(ctx, video.ID, []domain.VideoSpeaker{
		proposedSpeaker(video.ID, "A", "Marine Durand", domain.SpeakerRolePrincipal),
		proposedSpeaker(video.ID, "B", "Paul Lefèvre", domain.SpeakerRolePrincipal),
	}); err != nil {
		t.Fatalf("ReplaceProposedVideoSpeakers: %v", err)
	}
	if _, err := store.SetOperatorVideoSpeaker(ctx, video.ID, "B", "Sophie Garnier", domain.SpeakerRoleGuest); err != nil {
		t.Fatalf("SetOperatorVideoSpeaker: %v", err)
	}

	if _, err := store.StartVideoAnalysis(ctx, video.ID); err != nil {
		t.Fatalf("StartVideoAnalysis: %v", err)
	}
	if _, err := store.CompleteVideoAnalysis(ctx, analysisFor(video.ID, `[{"Kind":"subtitle"}]`)); err != nil {
		t.Fatalf("CompleteVideoAnalysis: %v", err)
	}

	got, err := store.ListVideoSpeakers(ctx, video.ID)
	if err != nil {
		t.Fatalf("ListVideoSpeakers: %v", err)
	}
	if len(got) != 1 || got[0].Label != "B" || got[0].Name != "Sophie Garnier" || !got[0].OperatorSet {
		t.Errorf("speakers after a new analysis = %+v, want only the operator-set B", got)
	}
}

func TestVideoSpeakersUnknownVideo(t *testing.T) {
	store := setupStore(t)
	err := store.ReplaceProposedVideoSpeakers(t.Context(), "00000000-0000-4000-8000-000000000000", nil)
	if !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("ReplaceProposedVideoSpeakers(unknown video) = %v, want ErrVideoNotFound", err)
	}
}

func TestVideoSpeakersCascadeAndBadIDs(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	video := createVideoForAnalysis(ctx, t, store, "videos/speakers-3.mp4", domain.VideoStatusReady)
	if err := store.ReplaceProposedVideoSpeakers(ctx, video.ID, []domain.VideoSpeaker{proposedSpeaker(video.ID, "A", "", domain.SpeakerRoleUnknown)}); err != nil {
		t.Fatalf("ReplaceProposedVideoSpeakers: %v", err)
	}
	if err := store.DeleteVideo(ctx, video.ID); err != nil {
		t.Fatalf("DeleteVideo: %v", err)
	}
	got, err := store.ListVideoSpeakers(ctx, video.ID)
	if err != nil || len(got) != 0 {
		t.Errorf("speakers after video delete = %v, %v; want none", got, err)
	}

	if got, err := store.ListVideoSpeakers(ctx, "not-a-uuid"); err != nil || len(got) != 0 {
		t.Errorf("ListVideoSpeakers(bad id) = %v, %v", got, err)
	}
	if err := store.ReplaceProposedVideoSpeakers(ctx, "not-a-uuid", nil); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("ReplaceProposedVideoSpeakers(bad id) = %v, want ErrVideoNotFound", err)
	}
	if _, err := store.SetOperatorVideoSpeaker(ctx, "not-a-uuid", "A", "x", domain.SpeakerRoleGuest); !errors.Is(err, domain.ErrVideoNotFound) {
		t.Errorf("SetOperatorVideoSpeaker(bad id) = %v, want ErrVideoNotFound", err)
	}
}
