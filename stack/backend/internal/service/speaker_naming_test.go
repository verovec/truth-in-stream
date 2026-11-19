package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/speakername"
)

type fakeSpeakerVideos struct {
	video domain.Video
	err   error
}

func (f fakeSpeakerVideos) GetVideo(context.Context, string) (domain.Video, error) {
	return f.video, f.err
}

type fakeSnapshots struct {
	events []LiveEvent
	found  bool
	err    error
}

func (f fakeSnapshots) Snapshot(context.Context, string) ([]LiveEvent, bool, error) {
	return f.events, f.found, f.err
}

// fakeSpeakerStore keeps the mapping in memory with the store's contract: a
// replace drops the step's rows and never touches operator-set ones.
type fakeSpeakerStore struct {
	rows       map[string]domain.VideoSpeaker
	replaces   int
	replaceErr error
}

func newFakeSpeakerStore() *fakeSpeakerStore {
	return &fakeSpeakerStore{rows: make(map[string]domain.VideoSpeaker)}
}

func (f *fakeSpeakerStore) ListVideoSpeakers(context.Context, string) ([]domain.VideoSpeaker, error) {
	out := make([]domain.VideoSpeaker, 0, len(f.rows))
	for _, row := range f.rows {
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b domain.VideoSpeaker) int { return strings.Compare(a.Label, b.Label) })
	return out, nil
}

func (f *fakeSpeakerStore) ReplaceProposedVideoSpeakers(_ context.Context, _ string, speakers []domain.VideoSpeaker) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.replaces++
	for label, row := range f.rows {
		if !row.OperatorSet {
			delete(f.rows, label)
		}
	}
	for _, s := range speakers {
		if _, held := f.rows[s.Label]; !held {
			f.rows[s.Label] = s
		}
	}
	return nil
}

func (f *fakeSpeakerStore) SetOperatorVideoSpeaker(_ context.Context, videoID, label, name string, role domain.SpeakerRole) (domain.VideoSpeaker, error) {
	row := f.rows[label]
	row.VideoID, row.Label, row.Name, row.Role = videoID, label, name, role
	row.Confidence, row.Method, row.OperatorSet = 1, domain.SpeakerMethodOperator, true
	f.rows[label] = row
	return row, nil
}

type stubSpeakerModel struct {
	participants    []speakername.Participant
	participantsErr error
	tieName         string
	tieQuote        string
	tieErr          error
	tieCalls        []string
}

func (s *stubSpeakerModel) Participants(context.Context, string, []speakername.Turn) ([]speakername.Participant, error) {
	return s.participants, s.participantsErr
}

func (s *stubSpeakerModel) BreakTie(_ context.Context, label string, _ []string, _ []speakername.Turn) (string, string, error) {
	s.tieCalls = append(s.tieCalls, label)
	return s.tieName, s.tieQuote, s.tieErr
}

// interviewEvents is a two-voice program: a moderator F who hands the floor
// to Marine Durand (label A) twice, with a claim event in between that naming
// must ignore.
func interviewEvents() []LiveEvent {
	sub := func(speaker string, start, end int, text string) LiveEvent {
		return LiveEvent{Kind: LiveEventSubtitle, Segment: domain.Segment{
			Speaker: speaker, Start: time.Duration(start) * time.Second, End: time.Duration(end) * time.Second, Text: text,
		}}
	}
	return []LiveEvent{
		sub("F", 0, 6, "Bonsoir, nous recevons ce soir Marine Durand."),
		sub("F", 6, 10, "Madame Durand, vous avez deux minutes."),
		sub("A", 10, 70, "Merci. Le chômage a baissé de deux points en un an."),
		{Kind: LiveEventResult, ClaimID: "c1", Segment: domain.Segment{Speaker: "A", Text: "Madame Durand, à vous."}},
		sub("F", 70, 74, "Une précision, Madame Durand, à vous."),
		sub("A", 74, 130, "Je maintiens ce chiffre, il est public."),
	}
}

var durandParticipant = []speakername.Participant{{Name: "Marine Durand", Quote: "nous recevons ce soir Marine Durand"}}

func newTestNamer(t *testing.T, snapshots fakeSnapshots, store *fakeSpeakerStore, model SpeakerNameModel) *SpeakerNamer {
	t.Helper()
	namer, err := NewSpeakerNamer(fakeSpeakerVideos{video: domain.Video{ID: "v1", Title: "Le grand entretien"}}, snapshots, store, model, discardLogger())
	if err != nil {
		t.Fatalf("NewSpeakerNamer: %v", err)
	}
	return namer
}

func namesByLabel(speakers []domain.VideoSpeaker) map[string]string {
	out := make(map[string]string, len(speakers))
	for _, s := range speakers {
		out[s.Label] = s.Name
	}
	return out
}

func TestNewSpeakerNamerValidation(t *testing.T) {
	t.Parallel()
	videos, snapshots, store := fakeSpeakerVideos{}, fakeSnapshots{}, newFakeSpeakerStore()
	if _, err := NewSpeakerNamer(nil, snapshots, store, nil, nil); err == nil {
		t.Error("expected an error without a video store")
	}
	if _, err := NewSpeakerNamer(videos, nil, store, nil, nil); err == nil {
		t.Error("expected an error without a snapshot source")
	}
	if _, err := NewSpeakerNamer(videos, snapshots, nil, nil, nil); err == nil {
		t.Error("expected an error without a speaker store")
	}
	if _, err := NewSpeakerNamer(videos, snapshots, store, nil, nil); err != nil {
		t.Errorf("a nil model and logger must be accepted: %v", err)
	}
}

func TestSpeakerNamerNameStoresAProposalPerLabel(t *testing.T) {
	t.Parallel()
	store := newFakeSpeakerStore()
	namer := newTestNamer(t, fakeSnapshots{events: interviewEvents(), found: true}, store, &stubSpeakerModel{participants: durandParticipant})

	got, err := namer.Name(t.Context(), "v1")
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d speakers, want one per label (2): %+v", len(got), got)
	}
	a, f := got[0], got[1]
	if a.Label != "A" || a.Name != "Marine Durand" || a.Role != domain.SpeakerRolePrincipal || a.Method != domain.SpeakerMethodSignals {
		t.Errorf("label A = %+v", a)
	}
	if a.Confidence < speakername.ConfidenceFloor || len(a.Evidence) == 0 {
		t.Errorf("label A lacks confidence or evidence: %+v", a)
	}
	if f.Label != "F" || f.Name != "" || f.Role != domain.SpeakerRoleModerator || f.Method != domain.SpeakerMethodNone {
		t.Errorf("label F = %+v", f)
	}
	for _, s := range got {
		if s.VideoID != "v1" || s.OperatorSet {
			t.Errorf("label %s: video %q, operator_set %v", s.Label, s.VideoID, s.OperatorSet)
		}
	}
}

func TestSpeakerNamerDiscardsUngroundedNames(t *testing.T) {
	t.Parallel()
	model := &stubSpeakerModel{participants: []speakername.Participant{
		{Name: "Marine Durand", Quote: "Marine Durand est la candidate bien connue"},
	}}
	namer := newTestNamer(t, fakeSnapshots{events: interviewEvents(), found: true}, newFakeSpeakerStore(), model)

	got, err := namer.Name(t.Context(), "v1")
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"A": "", "F": ""}, namesByLabel(got)); diff != "" {
		t.Errorf("a name quoted from outside the transcript was used (-want +got):\n%s", diff)
	}
}

func TestSpeakerNamerWithoutModelLeavesLabelsUnnamed(t *testing.T) {
	t.Parallel()
	namer := newTestNamer(t, fakeSnapshots{events: interviewEvents(), found: true}, newFakeSpeakerStore(), nil)

	got, err := namer.Name(t.Context(), "v1")
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"A": "", "F": ""}, namesByLabel(got)); diff != "" {
		t.Errorf("names mismatch (-want +got):\n%s", diff)
	}
	if got[0].Role != domain.SpeakerRolePrincipal {
		t.Errorf("roles must still be inferred without a model: %+v", got[0])
	}
}

func TestSpeakerNamerFailuresWriteNothing(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name      string
		videos    fakeSpeakerVideos
		snapshots fakeSnapshots
		model     *stubSpeakerModel
		storeErr  error
		wantIs    error
	}{
		{name: "unknown video", videos: fakeSpeakerVideos{err: domain.ErrVideoNotFound}, wantIs: domain.ErrVideoNotFound},
		{name: "no stored analysis", snapshots: fakeSnapshots{found: false}, wantIs: domain.ErrVideoAnalysisNotFound},
		{name: "snapshot read fails", snapshots: fakeSnapshots{err: boom}, wantIs: boom},
		{
			name: "participant extraction fails", snapshots: fakeSnapshots{events: interviewEvents(), found: true},
			model: &stubSpeakerModel{participantsErr: boom}, wantIs: boom,
		},
		{
			name: "store write fails", snapshots: fakeSnapshots{events: interviewEvents(), found: true},
			model: &stubSpeakerModel{participants: durandParticipant}, storeErr: boom, wantIs: boom,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeSpeakerStore()
			store.rows["A"] = domain.VideoSpeaker{Label: "A", Name: "Earlier Proposal"}
			store.replaceErr = tc.storeErr
			var model SpeakerNameModel
			if tc.model != nil {
				model = tc.model
			}
			namer, err := NewSpeakerNamer(tc.videos, tc.snapshots, store, model, discardLogger())
			if err != nil {
				t.Fatalf("NewSpeakerNamer: %v", err)
			}
			if err := namer.Run(t.Context(), "v1"); !errors.Is(err, tc.wantIs) {
				t.Fatalf("Run error = %v, want %v", err, tc.wantIs)
			}
			if store.replaces != 0 || store.rows["A"].Name != "Earlier Proposal" {
				t.Errorf("a failed run changed the stored mapping: %+v", store.rows)
			}
		})
	}
}

func TestSpeakerNamerOperatorOverrideSurvivesReRun(t *testing.T) {
	t.Parallel()
	store := newFakeSpeakerStore()
	namer := newTestNamer(t, fakeSnapshots{events: interviewEvents(), found: true}, store, &stubSpeakerModel{participants: durandParticipant})
	ctx := t.Context()

	if _, err := namer.Name(ctx, "v1"); err != nil {
		t.Fatalf("Name: %v", err)
	}
	updated, err := namer.Update(ctx, "v1", "A", SpeakerUpdate{Name: "  Sophie Garnier ", Role: domain.SpeakerRoleGuest})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Sophie Garnier" || updated.Role != domain.SpeakerRoleGuest || !updated.OperatorSet {
		t.Errorf("updated speaker = %+v", updated)
	}

	got, err := namer.Name(ctx, "v1")
	if err != nil {
		t.Fatalf("Name (re-run): %v", err)
	}
	if got[0].Name != "Sophie Garnier" || got[0].Role != domain.SpeakerRoleGuest || !got[0].OperatorSet {
		t.Errorf("the operator's decision did not survive the re-run: %+v", got[0])
	}
}

func TestSpeakerNamerUpdateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		videos    fakeSpeakerVideos
		snapshots fakeSnapshots
		label     string
		update    SpeakerUpdate
		wantIs    error
	}{
		{
			name: "unknown role", snapshots: fakeSnapshots{events: interviewEvents(), found: true},
			label: "A", update: SpeakerUpdate{Name: "x", Role: "host"}, wantIs: domain.ErrInvalidSpeakerRole,
		},
		{
			name: "name too long", snapshots: fakeSnapshots{events: interviewEvents(), found: true},
			label: "A", update: SpeakerUpdate{Name: strings.Repeat("é", maxSpeakerNameRunes+1), Role: domain.SpeakerRoleGuest}, wantIs: domain.ErrInvalidSpeakerName,
		},
		{
			name: "unknown video", videos: fakeSpeakerVideos{err: domain.ErrVideoNotFound},
			label: "A", update: SpeakerUpdate{Role: domain.SpeakerRoleGuest}, wantIs: domain.ErrVideoNotFound,
		},
		{
			name: "no stored analysis", snapshots: fakeSnapshots{found: false},
			label: "A", update: SpeakerUpdate{Role: domain.SpeakerRoleGuest}, wantIs: domain.ErrVideoAnalysisNotFound,
		},
		{
			name: "label absent from the analysis", snapshots: fakeSnapshots{events: interviewEvents(), found: true},
			label: "Z", update: SpeakerUpdate{Name: "x", Role: domain.SpeakerRoleGuest}, wantIs: domain.ErrVideoSpeakerNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeSpeakerStore()
			namer, err := NewSpeakerNamer(tc.videos, tc.snapshots, store, nil, discardLogger())
			if err != nil {
				t.Fatalf("NewSpeakerNamer: %v", err)
			}
			if _, err := namer.Update(t.Context(), "v1", tc.label, tc.update); !errors.Is(err, tc.wantIs) {
				t.Fatalf("Update error = %v, want %v", err, tc.wantIs)
			}
			if len(store.rows) != 0 {
				t.Errorf("a rejected update wrote a row: %+v", store.rows)
			}
		})
	}
}

func TestSpeakerNamerUpdateAcceptsAnEmptyName(t *testing.T) {
	t.Parallel()
	namer := newTestNamer(t, fakeSnapshots{events: interviewEvents(), found: true}, newFakeSpeakerStore(), nil)
	got, err := namer.Update(t.Context(), "v1", "F", SpeakerUpdate{Role: domain.SpeakerRoleModerator})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "" || !got.OperatorSet {
		t.Errorf("confirmed-unnamed speaker = %+v", got)
	}
}

// tiedEvents leaves label A open between two names: the moderator hands it the
// floor under each name twice.
func tiedEvents() []LiveEvent {
	var events []LiveEvent
	clock := 0
	say := func(speaker string, seconds int, text string) {
		events = append(events, LiveEvent{Kind: LiveEventSubtitle, Segment: domain.Segment{
			Speaker: speaker, Start: time.Duration(clock) * time.Second, End: time.Duration(clock+seconds) * time.Second, Text: text,
		}})
		clock += seconds
	}
	say("F", 5, "Nous recevons Marine Durand et Paul Lefèvre.")
	for _, address := range []string{"Madame Durand", "Monsieur Lefèvre", "Madame Durand", "Monsieur Lefèvre"} {
		say("F", 4, address+", vous avez deux minutes.")
		say("A", 60, "J'ai été maire de ma commune pendant douze ans.")
	}
	return events
}

func TestSpeakerNamerFailedTieBreakKeepsTheEarlierName(t *testing.T) {
	t.Parallel()
	participants := []speakername.Participant{
		{Name: "Marine Durand", Quote: "Nous recevons Marine Durand"},
		{Name: "Paul Lefèvre", Quote: "Paul Lefèvre"},
	}
	const quote = "J'ai été maire de ma commune pendant douze ans."
	model := &stubSpeakerModel{participants: participants, tieName: "Marine Durand", tieQuote: quote}
	store := newFakeSpeakerStore()
	namer := newTestNamer(t, fakeSnapshots{events: tiedEvents(), found: true}, store, model)
	ctx := t.Context()

	first, err := namer.Name(ctx, "v1")
	if err != nil || first[0].Name != "Marine Durand" {
		t.Fatalf("first run = %+v, %v; want label A settled by tie-break", first, err)
	}

	// The provider fails on the re-run: the name the same analysis already
	// earned stays, with its evidence.
	model.tieErr = errors.New("rate limited")
	second, err := namer.Name(ctx, "v1")
	if err != nil {
		t.Fatalf("Name (re-run): %v", err)
	}
	if diff := cmp.Diff(first[0], second[0]); diff != "" {
		t.Errorf("a failed tie-break changed the earlier name (-want +got):\n%s", diff)
	}

	// A model that answers and declines is new information: the name goes.
	model.tieErr, model.tieName = nil, ""
	third, err := namer.Name(ctx, "v1")
	if err != nil {
		t.Fatalf("Name (declined): %v", err)
	}
	if third[0].Name != "" {
		t.Errorf("a declined tie-break kept the earlier name: %+v", third[0])
	}
}

func TestSpeakerNamerTieBreak(t *testing.T) {
	t.Parallel()
	participants := []speakername.Participant{
		{Name: "Marine Durand", Quote: "Nous recevons Marine Durand"},
		{Name: "Paul Lefèvre", Quote: "Paul Lefèvre"},
	}
	tests := []struct {
		name     string
		model    stubSpeakerModel
		wantName string
	}{
		{
			name:     "a grounded decision names the label at the floor",
			model:    stubSpeakerModel{tieName: "Marine Durand", tieQuote: "J'ai été maire de ma commune pendant douze ans."},
			wantName: "Marine Durand",
		},
		{name: "a quote absent from the transcript is rejected", model: stubSpeakerModel{tieName: "Marine Durand", tieQuote: "Elle a été maire pendant douze ans."}},
		{name: "a name outside the contenders is rejected", model: stubSpeakerModel{tieName: "Karim Benali", tieQuote: "J'ai été maire de ma commune pendant douze ans."}},
		{name: "an undecided model leaves the label unnamed", model: stubSpeakerModel{}},
		{name: "a failing model leaves the label unnamed", model: stubSpeakerModel{tieErr: errors.New("boom")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := tc.model
			model.participants = participants
			namer := newTestNamer(t, fakeSnapshots{events: tiedEvents(), found: true}, newFakeSpeakerStore(), &model)

			got, err := namer.Name(t.Context(), "v1")
			if err != nil {
				t.Fatalf("Name: %v", err)
			}
			if diff := cmp.Diff([]string{"A"}, model.tieCalls); diff != "" {
				t.Errorf("tie-break calls mismatch (-want +got):\n%s", diff)
			}
			a := got[0]
			if a.Name != tc.wantName {
				t.Fatalf("label A named %q, want %q", a.Name, tc.wantName)
			}
			if tc.wantName != "" && (a.Method != domain.SpeakerMethodTieBreak || a.Confidence != speakername.ConfidenceFloor) {
				t.Errorf("tie-break result = %+v", a)
			}
		})
	}
}
