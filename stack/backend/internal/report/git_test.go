package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeGit returns canned output keyed by the git subcommand (args[2] after
// "git -C <dir>").
func newGitSource(t *testing.T, now time.Time, outputs map[string]string) *GitCommitSource {
	t.Helper()
	return &GitCommitSource{
		dir: ".",
		now: func() time.Time { return now },
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "git" {
				t.Fatalf("unexpected command %q", name)
			}
			sub := args[2] // git, -C, <dir>, <sub>, ...
			out, ok := outputs[sub]
			if !ok {
				t.Fatalf("no canned output for git %s (args %v)", sub, args)
			}
			return []byte(out), nil
		},
	}
}

func TestParseCommits(t *testing.T) {
	t.Parallel()
	m := commitMarker
	out := m + "abc1234\tAlice\tFix the parser\n" +
		"3\t1\tinternal/a.go\n" +
		"0\t5\tinternal/b.go\n" +
		m + "def5678\tBob\tAdd a feature\n" +
		"10\t0\tcmd/main.go\n"

	src := newGitSource(t, time.Now(), map[string]string{"log": out})
	commits, err := src.Commits(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Commits: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(commits))
	}
	if commits[0].Hash != "abc1234" || commits[0].Author != "Alice" || commits[0].Subject != "Fix the parser" {
		t.Errorf("commit0 = %+v", commits[0])
	}
	if commits[0].Files != 2 {
		t.Errorf("commit0 files = %d, want 2", commits[0].Files)
	}
	if commits[1].Files != 1 {
		t.Errorf("commit1 files = %d, want 1", commits[1].Files)
	}
}

func TestParseCommitsEmpty(t *testing.T) {
	t.Parallel()
	src := newGitSource(t, time.Now(), map[string]string{"log": ""})
	commits, err := src.Commits(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("Commits: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("got %d commits, want 0", len(commits))
	}
}

func TestActiveCardIDs(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	recent := now.Add(-1 * time.Hour).Unix()
	stale := now.Add(-48 * time.Hour).Unix()
	out := fmt.Sprintf("VER-29-upload_ui\t%d\n", recent) +
		fmt.Sprintf("origin/VER-30-live\t%d\n", recent) +
		fmt.Sprintf("VER-7-old_branch\t%d\n", stale) +
		fmt.Sprintf("main\t%d\n", recent)

	src := newGitSource(t, now, map[string]string{"for-each-ref": out})
	active, err := src.ActiveCardIDs(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("ActiveCardIDs: %v", err)
	}
	if !active["VER-29"] || !active["VER-30"] {
		t.Errorf("want VER-29 and VER-30 active, got %v", active)
	}
	if active["VER-7"] {
		t.Errorf("VER-7 branch is stale (48h), must not be active: %v", active)
	}
	if len(active) != 2 {
		t.Errorf("active set = %v, want exactly VER-29 and VER-30", active)
	}
}

func TestCommitsPassesExpectedArgs(t *testing.T) {
	t.Parallel()
	var gotArgs []string
	src := &GitCommitSource{
		dir: ".",
		now: time.Now,
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			gotArgs = args
			return nil, nil
		},
	}
	if _, err := src.Commits(context.Background(), 24*time.Hour); err != nil {
		t.Fatalf("Commits: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"log", "--no-merges", "--since=24 hours ago", "--numstat"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, gotArgs)
		}
	}
}

func TestSinceArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		window time.Duration
		want   string
	}{
		{24 * time.Hour, "24 hours ago"},
		{72 * time.Hour, "72 hours ago"},
		{30 * time.Minute, "1 hours ago"},
	}
	for _, tc := range tests {
		if got := sinceArg(tc.window); got != tc.want {
			t.Errorf("sinceArg(%v) = %q, want %q", tc.window, got, tc.want)
		}
	}
}
