// Package report assembles a daily development digest from git history, Linear
// card activity, and GitHub pull requests, and renders it for Slack (Block Kit)
// or the terminal. Each data source is an interface so the collector can run
// best-effort: a source that is unconfigured or failing degrades to a note
// rather than aborting the whole digest.
package report

import (
	"context"
	"time"
)

// DefaultWindow is the look-back the digest summarizes.
const DefaultWindow = 24 * time.Hour

// Commit is one git commit within the window.
type Commit struct {
	Hash    string
	Author  string
	Subject string
	Files   int
}

// CardMove is a Linear card that changed within the window, or a card in a
// given state. ID is the human identifier (e.g. VER-29).
type CardMove struct {
	ID        string
	Title     string
	State     string
	UpdatedAt time.Time
}

// PullRequest is an open GitHub pull request.
type PullRequest struct {
	Number int
	Title  string
	Author string
	URL    string
	Draft  bool
}

// Blocker is an In Progress card with no branch commit within the window: work
// that is claimed but stalled.
type Blocker struct {
	ID    string
	Title string
}

// Payload is the assembled digest. Notes carries per-source degradation
// messages so a missing key or a failing API surfaces in the report instead of
// failing it.
type Payload struct {
	GeneratedAt time.Time
	Window      time.Duration
	Commits     []Commit
	CardMoves   []CardMove
	OpenPRs     []PullRequest
	Blockers    []Blocker
	Notes       []string
}

// CommitSource reads recent git history.
type CommitSource interface {
	// Commits returns commits within window, newest first.
	Commits(ctx context.Context, window time.Duration) ([]Commit, error)
	// ActiveCardIDs returns the set of card IDs whose branch has a commit within
	// window; the blocker heuristic treats an In Progress card absent from this
	// set as stalled.
	ActiveCardIDs(ctx context.Context, window time.Duration) (map[string]bool, error)
}

// LinearSource reads Linear card activity.
type LinearSource interface {
	// RecentMoves returns cards updated within window.
	RecentMoves(ctx context.Context, window time.Duration) ([]CardMove, error)
	// InProgress returns cards currently in the In Progress state.
	InProgress(ctx context.Context) ([]CardMove, error)
}

// PRSource reads open pull requests.
type PRSource interface {
	OpenPRs(ctx context.Context) ([]PullRequest, error)
}

// Collector assembles a Payload from its sources. A nil source contributes a
// note and an empty section.
type Collector struct {
	commits CommitSource
	linear  LinearSource
	prs     PRSource
	now     func() time.Time
	window  time.Duration
}

// CollectorOption customizes a Collector.
type CollectorOption func(*Collector)

// WithWindow overrides the look-back window.
func WithWindow(d time.Duration) CollectorOption {
	return func(c *Collector) {
		if d > 0 {
			c.window = d
		}
	}
}

// WithClock overrides the clock, for deterministic timestamps in tests.
func WithClock(now func() time.Time) CollectorOption {
	return func(c *Collector) {
		if now != nil {
			c.now = now
		}
	}
}

// NewCollector builds a Collector. Any source may be nil; the digest degrades
// to a note for that section.
func NewCollector(commits CommitSource, linear LinearSource, prs PRSource, opts ...CollectorOption) *Collector {
	c := &Collector{
		commits: commits,
		linear:  linear,
		prs:     prs,
		now:     time.Now,
		window:  DefaultWindow,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Collect gathers every section. It never returns an error: a failed source
// records a note and leaves its section empty so the rest of the digest still
// renders.
func (c *Collector) Collect(ctx context.Context) Payload {
	p := Payload{GeneratedAt: c.now(), Window: c.window}

	if c.commits != nil {
		if commits, err := c.commits.Commits(ctx, c.window); err != nil {
			p.Notes = append(p.Notes, "git commits: "+err.Error())
		} else {
			p.Commits = commits
		}
	} else {
		p.Notes = append(p.Notes, "git: source not configured")
	}

	var inProgress []CardMove
	if c.linear != nil {
		if moves, err := c.linear.RecentMoves(ctx, c.window); err != nil {
			p.Notes = append(p.Notes, "linear activity: "+err.Error())
		} else {
			p.CardMoves = moves
		}
		if ip, err := c.linear.InProgress(ctx); err != nil {
			p.Notes = append(p.Notes, "linear in-progress: "+err.Error())
		} else {
			inProgress = ip
		}
	} else {
		p.Notes = append(p.Notes, "linear: not configured (set LINEAR_API_KEY)")
	}

	if c.prs != nil {
		if prs, err := c.prs.OpenPRs(ctx); err != nil {
			p.Notes = append(p.Notes, "github pull requests: "+err.Error())
		} else {
			p.OpenPRs = prs
		}
	} else {
		p.Notes = append(p.Notes, "github: not configured")
	}

	p.Blockers = c.computeBlockers(ctx, &p, inProgress)
	return p
}

// computeBlockers flags In Progress cards whose branch saw no commit in the
// window. It needs both Linear (the In Progress set) and git (the active set);
// if either is unavailable it records a note and returns nothing.
func (c *Collector) computeBlockers(ctx context.Context, p *Payload, inProgress []CardMove) []Blocker {
	if len(inProgress) == 0 {
		return nil
	}
	if c.commits == nil {
		p.Notes = append(p.Notes, "blockers: git source unavailable, not computed")
		return nil
	}
	active, err := c.commits.ActiveCardIDs(ctx, c.window)
	if err != nil {
		p.Notes = append(p.Notes, "blockers: "+err.Error())
		return nil
	}
	var blockers []Blocker
	for _, card := range inProgress {
		if !active[card.ID] {
			blockers = append(blockers, Blocker{ID: card.ID, Title: card.Title})
		}
	}
	return blockers
}
