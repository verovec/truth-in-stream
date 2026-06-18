package report

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// newGitSource returns a GitCommitSource whose runner replies with canned output
// keyed by the git subcommand (args[2], after "git -C <dir>").
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

func TestSubjectsForCards(t *testing.T) {
	t.Parallel()
	out := "feat(live): two-axis verdict UI (VER-104)\n" +
		"test(eval): golden eval (VER-105)\n" +
		"feat(live): more verdict polish (VER-104)\n" +
		"chore: unrelated card (VER-200)\n"

	src := newGitSource(t, time.Now(), map[string]string{"log": out})
	subjects, err := src.SubjectsForCards(context.Background(), []string{"VER-104", "VER-105"})
	if err != nil {
		t.Fatalf("SubjectsForCards: %v", err)
	}
	if len(subjects["VER-104"]) != 2 {
		t.Errorf("VER-104 subjects = %v, want 2", subjects["VER-104"])
	}
	if len(subjects["VER-105"]) != 1 {
		t.Errorf("VER-105 subjects = %v, want 1", subjects["VER-105"])
	}
	if _, ok := subjects["VER-200"]; ok {
		t.Errorf("VER-200 was not requested, must not appear: %v", subjects)
	}
}

func TestSubjectsForCardsEmptyIDs(t *testing.T) {
	t.Parallel()
	// No IDs requested means no git call at all; the runner would fail the test
	// if invoked because it has no canned output.
	src := newGitSource(t, time.Now(), map[string]string{})
	subjects, err := src.SubjectsForCards(context.Background(), nil)
	if err != nil {
		t.Fatalf("SubjectsForCards: %v", err)
	}
	if len(subjects) != 0 {
		t.Errorf("want empty result for no IDs, got %v", subjects)
	}
}

func TestSubjectsForCardsPassesGrepArgs(t *testing.T) {
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
	if _, err := src.SubjectsForCards(context.Background(), []string{"VER-1"}); err != nil {
		t.Fatalf("SubjectsForCards: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"log", "--no-merges", "--grep", "--pretty=format:%s"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, gotArgs)
		}
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
