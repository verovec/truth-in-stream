package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func recapSubtitle(speaker string, start, end int, text string) LiveEvent {
	return LiveEvent{Kind: LiveEventSubtitle, Segment: domain.Segment{
		Speaker: speaker, Start: time.Duration(start) * time.Second, End: time.Duration(end) * time.Second, Text: text,
	}}
}

func recapClaims(speaker string, start int, text string, claims ...AtomicClaim) LiveEvent {
	return LiveEvent{
		Kind: LiveEventClaims, Claims: claims,
		Segment: domain.Segment{Speaker: speaker, Start: time.Duration(start) * time.Second, Text: text},
	}
}

func recapVerdict(claimID, verdict string, sources ...domain.Source) LiveEvent {
	return LiveEvent{
		Kind: LiveEventResult, ClaimID: claimID, ClaimStatus: ClaimStatusVerified,
		Verdict: &VerifiedVerdict{
			Verdict: verdict, Rationale: "parce que " + claimID, Flags: []string{"cherry-picked"},
			Citations: []domain.SegmentMatch{{Sources: sources}},
		},
	}
}

func recapUnjudged(claimID string, status ClaimStatus) LiveEvent {
	return LiveEvent{Kind: LiveEventResult, ClaimID: claimID, ClaimStatus: status}
}

var (
	sourceINSEE = domain.Source{Title: "INSEE", URL: "https://example.org/insee"}
	sourceDARES = domain.Source{Title: "DARES", URL: "https://example.org/dares"}
)

// debateEvents is a moderated two-principal exchange covering every outcome:
// A has one disputed, one credible, one unverifiable and one never-judged
// claim; B has one claim whose verdict was upgraded and one shed under load;
// the moderator F makes one credible claim.
func debateEvents() []LiveEvent {
	return []LiveEvent{
		recapSubtitle("F", 0, 5, "Bonsoir."),
		recapSubtitle("A", 5, 65, "Le chômage a baissé. Les prix ont doublé."),
		recapClaims("A", 5, "Le chômage a baissé. Les prix ont doublé.",
			AtomicClaim{ClaimID: "a1", Quote: "Le chômage a baissé"},
			AtomicClaim{ClaimID: "a2", Quote: "Les prix ont doublé"},
			AtomicClaim{ClaimID: "a3"},
			AtomicClaim{ClaimID: "a4", Quote: "jamais jugée"},
		),
		recapUnjudged("a1", ClaimStatusChecking),
		recapVerdict("a1", VerdictCredible, sourceINSEE, sourceINSEE, domain.Source{Title: "sans lien"}),
		recapVerdict("a2", VerdictDisputed, sourceDARES),
		recapVerdict("a3", VerdictUnverifiable),
		recapSubtitle("F", 65, 70, "Monsieur, votre réponse. La dette est de trois mille milliards."),
		recapClaims("F", 65, "La dette est de trois mille milliards.", AtomicClaim{ClaimID: "f1", Quote: "La dette est de trois mille milliards"}),
		recapVerdict("f1", VerdictCredible, sourceINSEE),
		recapSubtitle("B", 70, 110, "C'est faux."),
		recapSubtitle("B", 110, 130, "La croissance est nulle."),
		recapClaims("B", 110, "La croissance est nulle.",
			AtomicClaim{ClaimID: "b1", Quote: "La croissance est nulle"},
			AtomicClaim{ClaimID: "b2", Quote: "C'est faux"},
		),
		recapVerdict("b1", VerdictDisputed, sourceDARES),
		recapVerdict("b1", VerdictCredible, sourceINSEE),
		recapUnjudged("b1", ClaimStatusError),
		recapUnjudged("b2", ClaimStatusUnchecked),
		recapSubtitle("F", 130, 134, "Merci."),
	}
}

func recapSpeaker(t *testing.T, recap Recap, label string) RecapSpeaker {
	t.Helper()
	for _, s := range recap.Speakers {
		if s.Label == label {
			return s
		}
	}
	t.Fatalf("no speaker %q in the recap", label)
	return RecapSpeaker{}
}

func TestBuildRecapCountsEveryOutcomeSeparately(t *testing.T) {
	t.Parallel()
	video := domain.Video{ID: "v1", Title: "Le débat", AnalyzedAt: time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)}
	recap := BuildRecap(video, debateEvents(), nil)

	if recap.VideoID != "v1" || recap.Title != "Le débat" || !recap.AnalyzedAt.Equal(video.AnalyzedAt) {
		t.Errorf("recap header = %+v", recap)
	}
	if recap.Duration != 134*time.Second {
		t.Errorf("duration = %s, want the last event's end when the media length is unknown", recap.Duration)
	}

	wantCounts := map[string]RecapCounts{
		"A": {Detected: 4, Credible: 1, Disputed: 1, Unverifiable: 1, NotVerified: 1},
		"B": {Detected: 2, Credible: 1, NotVerified: 1},
		"F": {Detected: 1, Credible: 1},
	}
	for label, want := range wantCounts {
		got := recapSpeaker(t, recap, label).Counts
		if got != want {
			t.Errorf("counts of %s = %+v, want %+v", label, got, want)
		}
		if got.Detected != got.Credible+got.Disputed+got.Unverifiable+got.NotVerified {
			t.Errorf("counts of %s do not sum to detected: %+v", label, got)
		}
	}

	a := recapSpeaker(t, recap, "A")
	wantCredible := []RecapStatement{{
		ClaimID: "a1", At: 5 * time.Second, Verdict: VerdictCredible, Flags: []string{"cherry-picked"},
		Quote: "Le chômage a baissé", Rationale: "parce que a1", Sources: []domain.Source{sourceINSEE},
	}}
	if diff := cmp.Diff(wantCredible, a.Credible); diff != "" {
		t.Errorf("credible statements of A mismatch (-want +got):\n%s", diff)
	}
	if len(a.Disputed) != 1 || a.Disputed[0].ClaimID != "a2" || a.Disputed[0].Sources[0] != sourceDARES {
		t.Errorf("disputed statements of A = %+v", a.Disputed)
	}

	// The upgraded claim stands at its last verdict, with that verdict's
	// sources; the later error does not erase it.
	b := recapSpeaker(t, recap, "B")
	if len(b.Disputed) != 0 || len(b.Credible) != 1 || b.Credible[0].ClaimID != "b1" || b.Credible[0].Sources[0] != sourceINSEE {
		t.Errorf("upgraded claim of B: disputed %+v, credible %+v", b.Disputed, b.Credible)
	}
}

func TestBuildRecapSpeakingTimeRolesAndGroups(t *testing.T) {
	t.Parallel()
	recap := BuildRecap(domain.Video{ID: "v1", DurationMS: 140_000}, debateEvents(), nil)

	if recap.Duration != 140*time.Second {
		t.Errorf("duration = %s, want the probed media length", recap.Duration)
	}
	labels := make([]string, 0, len(recap.Speakers))
	for _, s := range recap.Speakers {
		labels = append(labels, s.Label)
	}
	if diff := cmp.Diff([]string{"A", "B", "F"}, labels); diff != "" {
		t.Errorf("speakers must be ordered by speaking time (-want +got):\n%s", diff)
	}

	a, b, f := recapSpeaker(t, recap, "A"), recapSpeaker(t, recap, "B"), recapSpeaker(t, recap, "F")
	if a.SpeakingTime != 60*time.Second || a.Turns != 1 || a.Role != domain.SpeakerRolePrincipal {
		t.Errorf("A = time %s, turns %d, role %s", a.SpeakingTime, a.Turns, a.Role)
	}
	// B's two consecutive segments are one turn.
	if b.SpeakingTime != 60*time.Second || b.Turns != 1 || b.Role != domain.SpeakerRolePrincipal {
		t.Errorf("B = time %s, turns %d, role %s", b.SpeakingTime, b.Turns, b.Role)
	}
	if f.SpeakingTime != 14*time.Second || f.Turns != 3 || f.Role != domain.SpeakerRoleModerator {
		t.Errorf("F = time %s, turns %d, role %s", f.SpeakingTime, f.Turns, f.Role)
	}
	if a.Share != 0.5 || b.Share != 0.5 || f.Share != 0 {
		t.Errorf("shares = %v %v %v, want the principals' time split and none for the moderator", a.Share, b.Share, f.Share)
	}

	wantPrincipals := RecapGroup{SpeakingTime: 120 * time.Second, Turns: 2, Counts: RecapCounts{Detected: 6, Credible: 2, Disputed: 1, Unverifiable: 1, NotVerified: 2}}
	wantOthers := RecapGroup{SpeakingTime: 14 * time.Second, Turns: 3, Counts: RecapCounts{Detected: 1, Credible: 1}}
	if diff := cmp.Diff(wantPrincipals, recap.Principals); diff != "" {
		t.Errorf("principals group mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantOthers, recap.Others); diff != "" {
		t.Errorf("others group mismatch (-want +got):\n%s", diff)
	}
}

// TestBuildRecapAgreesWithStoredCounters pins the reconciliation the report
// promises: the principals and the others together are the whole analysis, and
// equal the counters stored with it.
func TestBuildRecapAgreesWithStoredCounters(t *testing.T) {
	t.Parallel()
	for name, events := range map[string][]LiveEvent{"debate": debateEvents(), "stored fixture": storedEvents()} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recap := BuildRecap(domain.Video{ID: "v1"}, events, nil)
			all := recap.Principals.Counts
			all.add(recap.Others.Counts)
			total, credible, disputed, unverifiable := countClaimVerdicts(events)
			want := RecapCounts{Detected: total, Credible: credible, Disputed: disputed, Unverifiable: unverifiable, NotVerified: total - credible - disputed - unverifiable}
			if all != want {
				t.Errorf("recap totals %+v, stored counters %+v", all, want)
			}
		})
	}
}

func TestBuildRecapAppliesTheSpeakerMapping(t *testing.T) {
	t.Parallel()
	mapping := []domain.VideoSpeaker{
		{Label: "A", Name: "Marine Durand", Role: domain.SpeakerRolePrincipal},
		{Label: "B", Name: "", Role: domain.SpeakerRoleGuest, OperatorSet: true},
		{Label: "F", Name: "Claire Martin", Role: domain.SpeakerRoleModerator, OperatorSet: true},
		{Label: "Z", Name: "Absent From The Analysis", Role: domain.SpeakerRolePrincipal},
	}
	recap := BuildRecap(domain.Video{ID: "v1"}, debateEvents(), mapping)

	a, b, f := recapSpeaker(t, recap, "A"), recapSpeaker(t, recap, "B"), recapSpeaker(t, recap, "F")
	if a.Name != "Marine Durand" || a.NameConfirmed {
		t.Errorf("A = %q confirmed %v", a.Name, a.NameConfirmed)
	}
	if f.Name != "Claire Martin" || !f.NameConfirmed {
		t.Errorf("F = %q confirmed %v", f.Name, f.NameConfirmed)
	}
	// The mapping's role wins over the structural one: B leaves the sheets,
	// and an unnamed voice keeps an empty name, never an invented one.
	if b.Name != "" || b.Role != domain.SpeakerRoleGuest || b.Share != 0 {
		t.Errorf("B = %+v", b)
	}
	if a.Share != 1 {
		t.Errorf("A's share = %v, want all of the principals' time", a.Share)
	}
	if len(recap.Speakers) != 3 {
		t.Errorf("a mapped label absent from the analysis must not create a speaker: %d speakers", len(recap.Speakers))
	}
	if recap.Principals.Counts.Detected != 4 || recap.Others.Counts.Detected != 3 {
		t.Errorf("groups after the role override: principals %+v, others %+v", recap.Principals.Counts, recap.Others.Counts)
	}
}

func TestBuildRecapEdgeShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		events         []LiveEvent
		wantSpeakers   []RecapSpeaker
		wantPrincipals RecapGroup
		wantOthers     RecapGroup
	}{
		{
			name:   "a single speaker is the one principal",
			events: []LiveEvent{recapSubtitle("A", 0, 30, "Un monologue."), recapSubtitle("A", 30, 60, "Qui continue.")},
			wantSpeakers: []RecapSpeaker{
				{Label: "A", Role: domain.SpeakerRolePrincipal, SpeakingTime: 60 * time.Second, Turns: 1, Share: 1},
			},
			wantPrincipals: RecapGroup{SpeakingTime: 60 * time.Second, Turns: 1},
		},
		{
			name: "an analysis with no verdict still reports its statements as not verified",
			events: []LiveEvent{
				recapSubtitle("A", 0, 30, "Une affirmation."),
				recapClaims("A", 0, "Une affirmation.", AtomicClaim{ClaimID: "c1", Quote: "Une affirmation"}),
				recapUnjudged("c1", ClaimStatusUnchecked),
			},
			wantSpeakers: []RecapSpeaker{
				{Label: "A", Role: domain.SpeakerRolePrincipal, SpeakingTime: 30 * time.Second, Turns: 1, Share: 1, Counts: RecapCounts{Detected: 1, NotVerified: 1}},
			},
			wantPrincipals: RecapGroup{SpeakingTime: 30 * time.Second, Turns: 1, Counts: RecapCounts{Detected: 1, NotVerified: 1}},
		},
		{
			name: "voices diarized over the same instant each keep their time",
			events: []LiveEvent{
				recapSubtitle("A", 0, 40, "Je parle."),
				recapSubtitle("B", 30, 70, "Je parle aussi."),
			},
			wantSpeakers: []RecapSpeaker{
				{Label: "A", Role: domain.SpeakerRolePrincipal, SpeakingTime: 40 * time.Second, Turns: 1, Share: 0.5},
				{Label: "B", Role: domain.SpeakerRolePrincipal, SpeakingTime: 40 * time.Second, Turns: 1, Share: 0.5},
			},
			wantPrincipals: RecapGroup{SpeakingTime: 80 * time.Second, Turns: 2},
		},
		{
			name: "a claim on undiarized speech is kept, under no label",
			events: []LiveEvent{
				recapSubtitle("A", 0, 30, "Bonsoir."),
				{Kind: LiveEventResult, ClaimID: "c1", ClaimStatus: ClaimStatusVerified, Segment: domain.Segment{Start: 40 * time.Second, Text: "Voix off."}, Verdict: &VerifiedVerdict{Verdict: VerdictUnverifiable}},
			},
			wantSpeakers: []RecapSpeaker{
				{Label: "A", Role: domain.SpeakerRolePrincipal, SpeakingTime: 30 * time.Second, Turns: 1, Share: 1},
				{Label: "", Role: domain.SpeakerRoleUnknown, Counts: RecapCounts{Detected: 1, Unverifiable: 1}},
			},
			wantPrincipals: RecapGroup{SpeakingTime: 30 * time.Second, Turns: 1},
			wantOthers:     RecapGroup{Counts: RecapCounts{Detected: 1, Unverifiable: 1}},
		},
		{
			name:         "an analysis with no transcript yields an empty report",
			events:       []LiveEvent{{Kind: LiveEventInterim}},
			wantSpeakers: []RecapSpeaker{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recap := BuildRecap(domain.Video{ID: "v1"}, tc.events, nil)
			if diff := cmp.Diff(tc.wantSpeakers, recap.Speakers, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("speakers mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPrincipals, recap.Principals); diff != "" {
				t.Errorf("principals mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantOthers, recap.Others); diff != "" {
				t.Errorf("others mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type fakeStoredEvents struct {
	events []LiveEvent
	err    error
}

func (f fakeStoredEvents) StoredEvents(context.Context, string) ([]LiveEvent, error) {
	return f.events, f.err
}

func TestBuildRecapSpeakingTimeExcludesGapsAndUnattributedSpeech(t *testing.T) {
	t.Parallel()
	events := []LiveEvent{
		recapSubtitle("A", 0, 10, "Avant l'extrait."),
		recapSubtitle("", 10, 300, "Un extrait diffusé à l'antenne."),
		recapSubtitle("A", 300, 310, "Après l'extrait."),
		recapSubtitle("A", 400, 410, "Après un silence."),
	}
	a := recapSpeaker(t, BuildRecap(domain.Video{ID: "v1"}, events, nil), "A")
	if a.SpeakingTime != 30*time.Second {
		t.Errorf("speaking time = %s, want the 30s A actually spoke", a.SpeakingTime)
	}
	// The clip ends A's first turn; the silence alone does not end the second.
	if a.Turns != 2 {
		t.Errorf("turns = %d, want 2", a.Turns)
	}
}

func TestBuildRecapQuotesAndTimesAClaimWithinItsUnit(t *testing.T) {
	t.Parallel()
	sub := func(id string, start int, text string) LiveEvent {
		ev := recapSubtitle("A", start, start+10, text)
		ev.ID = id
		return ev
	}
	events := []LiveEvent{
		sub("s1", 100, "Première phrase."),
		sub("s2", 110, "Deuxième phrase."),
		sub("s3", 125, "La dette a doublé."),
		{
			Kind: LiveEventClaims, ID: "s1", SegmentIDs: []string{"s1", "s2", "s3"},
			Segment: domain.Segment{Speaker: "A", Start: 100 * time.Second, Text: "Première phrase."},
			Claims: []AtomicClaim{
				{ClaimID: "anchored", Quote: "La dette a doublé", Spans: []domain.ClaimSpan{{SegmentID: "s3", Start: 0, End: 17}}},
				{ClaimID: "unanchored"},
				{ClaimID: "stale span", Quote: "Deuxième phrase", Spans: []domain.ClaimSpan{{SegmentID: "gone"}}},
			},
		},
		recapVerdict("anchored", VerdictDisputed, sourceINSEE),
		recapVerdict("unanchored", VerdictDisputed, sourceINSEE),
		recapVerdict("stale span", VerdictDisputed, sourceINSEE),
	}
	got := recapSpeaker(t, BuildRecap(domain.Video{ID: "v1"}, events, nil), "A").Disputed

	if got[0].Quote != "La dette a doublé" || got[0].At != 125*time.Second {
		t.Errorf("anchored claim = %q at %s, want its own sentence's time", got[0].Quote, got[0].At)
	}
	// With no verbatim quote the whole unit is quoted, never its first
	// sentence alone, and the time is the unit's start.
	if got[1].Quote != "Première phrase. Deuxième phrase. La dette a doublé." || got[1].At != 100*time.Second {
		t.Errorf("unanchored claim = %q at %s", got[1].Quote, got[1].At)
	}
	if got[2].At != 100*time.Second {
		t.Errorf("a span naming an unknown segment must fall back to the unit start, got %s", got[2].At)
	}
}

type fakeRecapSpeakers struct {
	speakers []domain.VideoSpeaker
	err      error
}

func (f fakeRecapSpeakers) ListVideoSpeakers(context.Context, string) ([]domain.VideoSpeaker, error) {
	return f.speakers, f.err
}

func TestNewRecapReaderValidation(t *testing.T) {
	t.Parallel()
	videos, analyses, speakers := fakeSpeakerVideos{}, fakeStoredEvents{}, fakeRecapSpeakers{}
	if _, err := NewRecapReader(nil, analyses, speakers); err == nil {
		t.Error("expected an error without a video store")
	}
	if _, err := NewRecapReader(videos, nil, speakers); err == nil {
		t.Error("expected an error without a stored analysis source")
	}
	if _, err := NewRecapReader(videos, analyses, nil); err == nil {
		t.Error("expected an error without a speaker source")
	}
}

func TestRecapReaderGet(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	complete := domain.Video{ID: "v1", Title: "Le débat", AnalysisStatus: domain.VideoAnalysisComplete}
	stored := fakeStoredEvents{events: debateEvents()}
	tests := []struct {
		name      string
		videos    fakeSpeakerVideos
		snapshots fakeStoredEvents
		speakers  fakeRecapSpeakers
		wantIs    error
	}{
		{name: "complete analysis", videos: fakeSpeakerVideos{video: complete}, snapshots: stored, speakers: fakeRecapSpeakers{speakers: []domain.VideoSpeaker{{Label: "A", Name: "Marine Durand", Role: domain.SpeakerRolePrincipal}}}},
		{name: "unknown video", videos: fakeSpeakerVideos{err: domain.ErrVideoNotFound}, wantIs: domain.ErrVideoNotFound},
		{name: "analysis still running", videos: fakeSpeakerVideos{video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisAnalysing}}, snapshots: stored, wantIs: domain.ErrVideoAnalysisNotFound},
		{name: "never analyzed", videos: fakeSpeakerVideos{video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisNone}}, wantIs: domain.ErrVideoAnalysisNotFound},
		{name: "complete but unreadable", videos: fakeSpeakerVideos{video: complete}, snapshots: fakeStoredEvents{err: domain.ErrVideoAnalysisNotFound}, wantIs: domain.ErrVideoAnalysisNotFound},
		{name: "stored analysis read fails", videos: fakeSpeakerVideos{video: complete}, snapshots: fakeStoredEvents{err: boom}, wantIs: boom},
		{name: "speaker read fails", videos: fakeSpeakerVideos{video: complete}, snapshots: stored, speakers: fakeRecapSpeakers{err: boom}, wantIs: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader, err := NewRecapReader(tc.videos, tc.snapshots, tc.speakers)
			if err != nil {
				t.Fatalf("NewRecapReader: %v", err)
			}
			recap, err := reader.Get(t.Context(), "v1")
			if !errors.Is(err, tc.wantIs) {
				t.Fatalf("Get error = %v, want %v", err, tc.wantIs)
			}
			if tc.wantIs != nil {
				// A backend fault must stay a fault: reported as a missing
				// analysis it would tell the reader to wait for nothing.
				if errors.Is(tc.wantIs, boom) && errors.Is(err, domain.ErrVideoAnalysisNotFound) {
					t.Errorf("a backend fault was reported as a missing analysis: %v", err)
				}
				return
			}
			if recap.Title != "Le débat" || recapSpeaker(t, recap, "A").Name != "Marine Durand" {
				t.Errorf("recap = %+v", recap)
			}
		})
	}
}
