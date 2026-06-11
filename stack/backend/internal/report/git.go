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

// commitMarker prefixes each commit header line in the git log format so the
// numstat lines that follow can be attributed to the right commit. It is the
// ASCII record-separator control character, which cannot appear in a subject.
const commitMarker = "\x1e"

// cardIDPattern matches a workspace card identifier (the Linear team prefix
// VER plus a number) inside a branch name such as VER-29-upload_ui.
var cardIDPattern = regexp.MustCompile(`VER-\d+`)

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

// Commits returns commits within window, newest first. Merge commits are
// excluded so the digest reflects authored work.
func (g *GitCommitSource) Commits(ctx context.Context, window time.Duration) ([]Commit, error) {
	out, err := g.run(ctx, "git", "-C", g.dir, "log",
		"--since="+sinceArg(window), "--no-merges",
		"--pretty=format:"+commitMarker+"%h%x09%an%x09%s", "--numstat")
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return parseCommits(out), nil
}

// parseCommits reads the combined --pretty/--numstat stream: a marker-prefixed
// header line per commit, followed by one numstat line per changed file.
func parseCommits(out []byte) []Commit {
	var commits []Commit
	cur := -1
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if header, ok := strings.CutPrefix(line, commitMarker); ok {
			fields := strings.SplitN(header, "\t", 3)
			c := Commit{Hash: fields[0]}
			if len(fields) > 1 {
				c.Author = fields[1]
			}
			if len(fields) > 2 {
				c.Subject = fields[2]
			}
			commits = append(commits, c)
			cur = len(commits) - 1
			continue
		}
		// A numstat line ("added\tdeleted\tpath") belongs to the current commit.
		if cur >= 0 {
			commits[cur].Files++
		}
	}
	return commits
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

// sinceArg renders window as a git relative date ("24 hours ago").
func sinceArg(window time.Duration) string {
	return fmt.Sprintf("%d hours ago", windowHours(window))
}
