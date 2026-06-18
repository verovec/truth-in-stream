package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCommitSource struct {
	subjects    map[string][]string
	active      map[string]bool
	subjectsErr error
	activeErr   error
	gotIDs      []string
}

func (f *fakeCommitSource) SubjectsForCards(_ context.Context, ids []string) (map[string][]string, error) {
	f.gotIDs = ids
	return f.subjects, f.subjectsErr
}

func (f *fakeCommitSource) ActiveCardIDs(context.Context, time.Duration) (map[string]bool, error) {
	return f.active, f.activeErr
}

type fakeLinear struct {
	moves        []CardMove
	remaining    []CardMove
	inProgress   []CardMove
	epicTitle    string
	epicChildren []CardMove
	movesErr     error
	remainingErr error
	ipErr        error
	epicErr      error
	gotEpicID    string
}

func (f *fakeLinear) RecentMoves(context.Context, time.Duration) ([]CardMove, error) {
	return f.moves, f.movesErr
}

func (f *fakeLinear) Remaining(context.Context) ([]CardMove, error) {
	return f.remaining, f.remainingErr
}

func (f *fakeLinear) EpicChildren(_ context.Context, epicID string) (string, []CardMove, error) {
	f.gotEpicID = epicID
	return f.epicTitle, f.epicChildren, f.epicErr
}

func (f *fakeLinear) InProgress(context.Context) ([]CardMove, error) {
	return f.inProgress, f.ipErr
}

type fakePRs struct {
	prs []PullRequest
	err error
}

func (f *fakePRs) OpenPRs(context.Context) ([]PullRequest, error) { return f.prs, f.err }

type fakeSummarizer struct {
	out    map[string]string
	err    error
	gotIDs []string
}

func (f *fakeSummarizer) Summarize(_ context.Context, cards []CardInput) (map[string]string, error) {
	for _, c := range cards {
		f.gotIDs = append(f.gotIDs, c.ID)
	}
	return f.out, f.err
}

var _ CommitSource = (*fakeCommitSource)(nil)
var _ LinearSource = (*fakeLinear)(nil)
var _ PRSource = (*fakePRs)(nil)
var _ CardSummarizer = (*fakeSummarizer)(nil)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func done(id, title string) CardMove {
	return CardMove{ID: id, Title: title, State: "Done", StateType: stateTypeCompleted}
}

func TestCollectDailyAssemblesAllSections(t *testing.T) {
	t.Parallel()
	commits := &fakeCommitSource{active: map[string]bool{"VER-1": true}}
	linear := &fakeLinear{
		moves:      []CardMove{done("VER-1", "Card one"), {ID: "VER-3", Title: "WIP", State: "In Progress", StateType: "started"}},
		remaining:  []CardMove{{ID: "VER-2", Title: "Next", State: "Todo", StateType: "unstarted"}},
		inProgress: []CardMove{{ID: "VER-1", Title: "Card one"}},
	}
	prs := &fakePRs{prs: []PullRequest{{Number: 7, Title: "PR", Author: "Dev", URL: "https://x"}}}

	c := NewCollector(commits, linear, prs, WithClock(fixedClock(time.Unix(1_700_000_000, 0))))
	p := c.Collect(context.Background())

	if p.Mode != ModeDaily {
		t.Errorf("Mode = %v, want daily", p.Mode)
	}
	if len(p.Shipped) != 1 || p.Shipped[0].ID != "VER-1" {
		t.Fatalf("shipped = %+v, want only the Done card VER-1", p.Shipped)
	}
	if len(p.Remaining) != 1 || p.Remaining[0].ID != "VER-2" {
		t.Fatalf("remaining = %+v", p.Remaining)
	}
	if len(p.OpenPRs) != 1 {
		t.Fatalf("open PRs = %+v", p.OpenPRs)
	}
	if len(p.Blockers) != 0 {
		t.Errorf("VER-1 has a recent branch commit, must not be a blocker: %+v", p.Blockers)
	}
	if len(p.Notes) != 0 {
		t.Errorf("no source failed, want no notes, got %v", p.Notes)
	}
	if p.GeneratedAt != time.Unix(1_700_000_000, 0) {
		t.Errorf("GeneratedAt = %v, want injected clock", p.GeneratedAt)
	}
}

func TestCollectEpicMode(t *testing.T) {
	t.Parallel()
	linear := &fakeLinear{
		epicTitle: "Political fact-check",
		epicChildren: []CardMove{
			done("VER-94", "French STT"),
			{ID: "VER-95", Title: "Still going", State: "In Progress", StateType: "started"},
		},
		remaining: []CardMove{{ID: "VER-95", Title: "Still going", State: "In Progress", StateType: "started"}},
	}
	c := NewCollector(&fakeCommitSource{}, linear, nil, WithEpic("VER-93"))
	p := c.Collect(context.Background())

	if p.Mode != ModeEpic {
		t.Fatalf("Mode = %v, want epic", p.Mode)
	}
	if linear.gotEpicID != "VER-93" {
		t.Errorf("EpicChildren called with %q, want VER-93", linear.gotEpicID)
	}
	if p.Epic == nil || p.Epic.ID != "VER-93" || p.Epic.Title != "Political fact-check" {
		t.Fatalf("epic = %+v", p.Epic)
	}
	if len(p.Shipped) != 1 || p.Shipped[0].ID != "VER-94" {
		t.Fatalf("shipped = %+v, want only the Done child VER-94", p.Shipped)
	}
}

func TestCollectSummariesAttachedAndDegrade(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		summarizer  CardSummarizer
		commits     CommitSource
		wantSummary string
		wantNote    string
	}{
		{
			name:        "summarizer attaches descriptions",
			summarizer:  &fakeSummarizer{out: map[string]string{"VER-1": "Did the thing."}},
			commits:     &fakeCommitSource{subjects: map[string][]string{"VER-1": {"feat: thing (VER-1)"}}},
			wantSummary: "Did the thing.",
		},
		{
			name:        "no summarizer leaves summary empty",
			summarizer:  nil,
			commits:     &fakeCommitSource{},
			wantSummary: "",
		},
		{
			name:        "summarizer error degrades to a note",
			summarizer:  &fakeSummarizer{err: errors.New("llm boom")},
			commits:     &fakeCommitSource{},
			wantSummary: "",
			wantNote:    "llm boom",
		},
		{
			name:        "commit-subject error still summarizes from titles",
			summarizer:  &fakeSummarizer{out: map[string]string{"VER-1": "From title."}},
			commits:     &fakeCommitSource{subjectsErr: errors.New("git boom")},
			wantSummary: "From title.",
			wantNote:    "git boom",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			linear := &fakeLinear{moves: []CardMove{done("VER-1", "Card one")}}
			opts := []CollectorOption{}
			if tc.summarizer != nil {
				opts = append(opts, WithSummarizer(tc.summarizer))
			}
			p := NewCollector(tc.commits, linear, nil, opts...).Collect(context.Background())

			if len(p.Shipped) != 1 {
				t.Fatalf("shipped = %+v", p.Shipped)
			}
			if p.Shipped[0].Summary != tc.wantSummary {
				t.Errorf("summary = %q, want %q", p.Shipped[0].Summary, tc.wantSummary)
			}
			if p.Shipped[0].Title != "Card one" {
				t.Errorf("title not preserved: %+v", p.Shipped[0])
			}
			if tc.wantNote != "" && !strings.Contains(strings.Join(p.Notes, "\n"), tc.wantNote) {
				t.Errorf("want note %q in %v", tc.wantNote, p.Notes)
			}
		})
	}
}

func TestCollectNilSourcesDegradeToNotes(t *testing.T) {
	t.Parallel()
	p := NewCollector(nil, nil, nil).Collect(context.Background())
	if len(p.Shipped) != 0 || len(p.Remaining) != 0 || len(p.OpenPRs) != 0 || len(p.Blockers) != 0 {
		t.Fatalf("nil sources must produce empty sections: %+v", p)
	}
	joined := strings.Join(p.Notes, "\n")
	for _, want := range []string{"linear", "github"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q source: %v", want, p.Notes)
		}
	}
}

func TestCollectSourceErrorsBecomeNotes(t *testing.T) {
	t.Parallel()
	commits := &fakeCommitSource{active: map[string]bool{}}
	linear := &fakeLinear{
		movesErr:     errors.New("moves boom"),
		remainingErr: errors.New("remaining boom"),
		ipErr:        errors.New("ip boom"),
	}
	prs := &fakePRs{err: errors.New("gh boom")}

	p := NewCollector(commits, linear, prs).Collect(context.Background())

	joined := strings.Join(p.Notes, "\n")
	for _, want := range []string{"moves boom", "remaining boom", "ip boom", "gh boom"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q: %v", want, p.Notes)
		}
	}
}

func TestCollectEpicErrorBecomesNote(t *testing.T) {
	t.Parallel()
	linear := &fakeLinear{epicErr: errors.New("epic not found")}
	p := NewCollector(&fakeCommitSource{}, linear, nil, WithEpic("VER-99")).Collect(context.Background())
	if len(p.Shipped) != 0 {
		t.Errorf("epic error must leave shipped empty: %+v", p.Shipped)
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "epic not found") {
		t.Errorf("want epic note, got %v", p.Notes)
	}
}

func TestRemainingSortedByStateThenID(t *testing.T) {
	t.Parallel()
	linear := &fakeLinear{remaining: []CardMove{
		{ID: "VER-9", State: "Todo", StateType: "unstarted"},
		{ID: "VER-2", State: "In Progress", StateType: "started"},
		{ID: "VER-1", State: "Todo", StateType: "unstarted"},
	}}
	p := NewCollector(nil, linear, nil).Collect(context.Background())
	got := make([]string, 0, len(p.Remaining))
	for _, c := range p.Remaining {
		got = append(got, c.ID)
	}
	if strings.Join(got, ",") != "VER-2,VER-1,VER-9" {
		t.Errorf("remaining order = %v, want In Progress first then Todo by ID", got)
	}
}

func TestBlockerHeuristic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		inProgress []CardMove
		active     map[string]bool
		activeErr  error
		commitsNil bool
		want       []string
		wantNote   string
	}{
		{
			name:       "stalled card flagged",
			inProgress: []CardMove{{ID: "VER-1", Title: "a"}, {ID: "VER-2", Title: "b"}},
			active:     map[string]bool{"VER-1": true},
			want:       []string{"VER-2"},
		},
		{
			name:       "all active, none flagged",
			inProgress: []CardMove{{ID: "VER-1", Title: "a"}},
			active:     map[string]bool{"VER-1": true},
			want:       nil,
		},
		{
			name:       "active lookup error degrades to note",
			inProgress: []CardMove{{ID: "VER-9", Title: "x"}},
			activeErr:  errors.New("ref boom"),
			want:       nil,
			wantNote:   "ref boom",
		},
		{
			name:       "nil commit source degrades to note",
			inProgress: []CardMove{{ID: "VER-9", Title: "x"}},
			commitsNil: true,
			want:       nil,
			wantNote:   "git source unavailable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var commits CommitSource
			if !tc.commitsNil {
				commits = &fakeCommitSource{active: tc.active, activeErr: tc.activeErr}
			}
			linear := &fakeLinear{inProgress: tc.inProgress}
			p := NewCollector(commits, linear, nil).Collect(context.Background())

			var ids []string
			for _, b := range p.Blockers {
				ids = append(ids, b.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Errorf("blockers = %v, want %v", ids, tc.want)
			}
			if tc.wantNote != "" && !strings.Contains(strings.Join(p.Notes, "\n"), tc.wantNote) {
				t.Errorf("want note %q in %v", tc.wantNote, p.Notes)
			}
		})
	}
}

func TestWithWindowOverride(t *testing.T) {
	t.Parallel()
	p := NewCollector(nil, nil, nil, WithWindow(72*time.Hour)).Collect(context.Background())
	if p.Window != 72*time.Hour {
		t.Errorf("window = %v, want 72h", p.Window)
	}
}
