package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCommitSource struct {
	commits    []Commit
	active     map[string]bool
	commitsErr error
	activeErr  error
}

func (f *fakeCommitSource) Commits(context.Context, time.Duration) ([]Commit, error) {
	return f.commits, f.commitsErr
}

func (f *fakeCommitSource) ActiveCardIDs(context.Context, time.Duration) (map[string]bool, error) {
	return f.active, f.activeErr
}

type fakeLinear struct {
	moves      []CardMove
	inProgress []CardMove
	movesErr   error
	ipErr      error
}

func (f *fakeLinear) RecentMoves(context.Context, time.Duration) ([]CardMove, error) {
	return f.moves, f.movesErr
}

func (f *fakeLinear) InProgress(context.Context) ([]CardMove, error) {
	return f.inProgress, f.ipErr
}

type fakePRs struct {
	prs []PullRequest
	err error
}

func (f *fakePRs) OpenPRs(context.Context) ([]PullRequest, error) { return f.prs, f.err }

var _ CommitSource = (*fakeCommitSource)(nil)
var _ LinearSource = (*fakeLinear)(nil)
var _ PRSource = (*fakePRs)(nil)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestCollectAssemblesAllSections(t *testing.T) {
	t.Parallel()
	commits := &fakeCommitSource{
		commits: []Commit{{Hash: "abc1234", Author: "Dev", Subject: "do a thing", Files: 3}},
		active:  map[string]bool{"VER-1": true},
	}
	linear := &fakeLinear{
		moves:      []CardMove{{ID: "VER-1", Title: "Card one", State: "In Review"}},
		inProgress: []CardMove{{ID: "VER-1", Title: "Card one"}},
	}
	prs := &fakePRs{prs: []PullRequest{{Number: 7, Title: "PR", Author: "Dev", URL: "https://x"}}}

	c := NewCollector(commits, linear, prs, WithClock(fixedClock(time.Unix(1_700_000_000, 0))))
	p := c.Collect(context.Background())

	if len(p.Commits) != 1 || len(p.CardMoves) != 1 || len(p.OpenPRs) != 1 {
		t.Fatalf("sections not all populated: %+v", p)
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

func TestCollectNilSourcesDegradeToNotes(t *testing.T) {
	t.Parallel()
	p := NewCollector(nil, nil, nil).Collect(context.Background())
	if len(p.Commits) != 0 || len(p.CardMoves) != 0 || len(p.OpenPRs) != 0 || len(p.Blockers) != 0 {
		t.Fatalf("nil sources must produce empty sections: %+v", p)
	}
	joined := strings.Join(p.Notes, "\n")
	for _, want := range []string{"git", "linear", "github"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q source: %v", want, p.Notes)
		}
	}
}

func TestCollectSourceErrorsBecomeNotes(t *testing.T) {
	t.Parallel()
	commits := &fakeCommitSource{commitsErr: errors.New("git boom"), active: map[string]bool{}}
	linear := &fakeLinear{movesErr: errors.New("linear boom"), ipErr: errors.New("ip boom")}
	prs := &fakePRs{err: errors.New("gh boom")}

	p := NewCollector(commits, linear, prs).Collect(context.Background())

	joined := strings.Join(p.Notes, "\n")
	for _, want := range []string{"git boom", "linear boom", "ip boom", "gh boom"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q: %v", want, p.Notes)
		}
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
