package report

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cardIDPattern matches a workspace card identifier (the Linear team prefix
// VER plus a number) inside a branch name such as VER-29-upload_ui or a commit
// subject such as "feat: do a thing (VER-29)".
var cardIDPattern = regexp.MustCompile(`VER-\d+`)

// cardIDGrepPattern is the same identifier written for git's grep engine. Git's
// POSIX extended-regexp (--extended-regexp) does not support the \d shorthand
// that Go's RE2 accepts, so a literal [0-9] class is used instead; with \d the
// grep would match nothing and every commit subject would be silently dropped.
const cardIDGrepPattern = `VER-[0-9]+`

// Runner executes a command and returns its stdout. Injected so tests can stand
// in for the git CLI.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", name, err)
	}
	return out, nil
}

// GitCommitSource reads commit and branch activity via the git CLI.
type GitCommitSource struct {
	run Runner
	dir string
	now func() time.Time
}

// NewGitCommitSource reads history from the repository containing dir.
func NewGitCommitSource(dir string) *GitCommitSource {
	return &GitCommitSource{run: execRunner, dir: dir, now: time.Now}
}

// SubjectsForCards returns, per requested card ID, the subjects of the commits
// that reference it. It scans every card-referencing commit in history (via
// --grep), not just the digest window, so an epic recap of older cards still has
// real input. Subjects are grouped by the card ID they name and arrive newest
// first; requested IDs with no matching commit are absent from the result.
func (g *GitCommitSource) SubjectsForCards(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out, err := g.run(ctx, "git", "-C", g.dir, "log", "--no-merges",
		"--extended-regexp", "--grep", cardIDGrepPattern, "--pretty=format:%s")
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	subjects := map[string][]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		subject := scanner.Text()
		if subject == "" {
			continue
		}
		id := cardIDPattern.FindString(subject)
		if id == "" || !want[id] {
			continue
		}
		subjects[id] = append(subjects[id], subject)
	}
	return subjects, nil
}

// ActiveCardIDs returns the set of card IDs whose local or remote branch has a
// commit within window. Used to tell which In Progress cards are stalled.
func (g *GitCommitSource) ActiveCardIDs(ctx context.Context, window time.Duration) (map[string]bool, error) {
	out, err := g.run(ctx, "git", "-C", g.dir, "for-each-ref",
		"--format=%(refname:short)%09%(committerdate:unix)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, fmt.Errorf("git for-each-ref: %w", err)
	}
	cutoff := g.now().Add(-window).Unix()
	active := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		name, tsStr, ok := strings.Cut(scanner.Text(), "\t")
		if !ok {
			continue
		}
		ts, err := strconv.ParseInt(strings.TrimSpace(tsStr), 10, 64)
		if err != nil || ts < cutoff {
			continue
		}
		if id := cardIDPattern.FindString(name); id != "" {
			active[id] = true
		}
	}
	return active, nil
}
