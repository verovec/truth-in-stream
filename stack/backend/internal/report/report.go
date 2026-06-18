// Package report assembles a development digest from git history, Linear card
// activity, and GitHub pull requests, and renders it for Slack (Block Kit) or
// the terminal. It runs in two modes: a daily digest (cards shipped within the
// window plus the project's remaining work) and an epic recap (one epic's
// shipped children plus the project's remaining work). Each data source is an
// interface so the collector runs best-effort: a source that is unconfigured or
// failing degrades to a note rather than aborting the whole digest.
package report

import (
	"context"
	"sort"
	"time"
)

// DefaultWindow is the look-back the daily digest summarizes.
const DefaultWindow = 24 * time.Hour

// stateTypeCompleted is the Linear workflow-state category for a finished card.
// It is stable across the team-specific state names ("Done", "Merged", ...), so
// the digest keys "shipped" off the category rather than a literal name.
const stateTypeCompleted = "completed"

// Mode selects what the digest reports.
type Mode int

const (
	// ModeDaily reports cards shipped within the window plus remaining work.
	ModeDaily Mode = iota
	// ModeEpic recaps one epic's shipped children plus remaining work.
	ModeEpic
)

// CardMove is a Linear card: its human identifier (e.g. VER-29), title, and
// workflow state. StateType is the state's category ("backlog", "started",
// "completed", "canceled"), stable across team-specific state names.
type CardMove struct {
	ID        string
	Title     string
	State     string
	StateType string
	UpdatedAt time.Time
}

// CardSummary is a shipped card paired with a one-line description of what it
// delivered. Summary is empty when no summarizer is configured or it failed;
// renderers fall back to Title.
type CardSummary struct {
	ID      string
	Title   string
	Summary string
}

// EpicSummary names the epic an epic-mode digest recaps.
type EpicSummary struct {
	ID    string
	Title string
}

// CardInput is one card handed to a CardSummarizer: its identifier, title, and
// the subjects of the commits that reference it.
type CardInput struct {
	ID       string
	Title    string
	Subjects []string
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

// Payload is the assembled digest. Notes carries per-source degradation messages
// so a missing key or a failing API surfaces in the report instead of failing
// it.
type Payload struct {
	GeneratedAt time.Time
	Window      time.Duration
	Mode        Mode
	Epic        *EpicSummary
	Shipped     []CardSummary
	Remaining   []CardMove
	OpenPRs     []PullRequest
	Blockers    []Blocker
	Notes       []string
}

// CommitSource reads recent git history.
type CommitSource interface {
	// SubjectsForCards returns, per requested card ID, the subjects of the
	// commits that reference it, so a summarizer can describe what each card
	// delivered. IDs with no matching commit are absent from the map.
	SubjectsForCards(ctx context.Context, ids []string) (map[string][]string, error)
	// ActiveCardIDs returns the set of card IDs whose branch has a commit within
	// window; the blocker heuristic treats an In Progress card absent from this
	// set as stalled.
	ActiveCardIDs(ctx context.Context, window time.Duration) (map[string]bool, error)
}

// LinearSource reads Linear card activity.
type LinearSource interface {
	// RecentMoves returns project cards updated within window.
	RecentMoves(ctx context.Context, window time.Duration) ([]CardMove, error)
	// Remaining returns project cards that are not finished or canceled.
	Remaining(ctx context.Context) ([]CardMove, error)
	// EpicChildren returns the epic's title and its child cards.
	EpicChildren(ctx context.Context, epicID string) (string, []CardMove, error)
	// InProgress returns cards currently in the In Progress state.
	InProgress(ctx context.Context) ([]CardMove, error)
}

// PRSource reads open pull requests.
type PRSource interface {
	OpenPRs(ctx context.Context) ([]PullRequest, error)
}

// CardSummarizer turns shipped cards into one-line "what was implemented"
// descriptions.
type CardSummarizer interface {
	// Summarize returns id -> one-line description. A card absent from the map
	// (or mapped to an empty string) falls back to its title at render time.
	Summarize(ctx context.Context, cards []CardInput) (map[string]string, error)
}

// Collector assembles a Payload from its sources. A nil source contributes a
// note and an empty section.
type Collector struct {
	commits    CommitSource
	linear     LinearSource
	prs        PRSource
	summarizer CardSummarizer
	now        func() time.Time
	window     time.Duration
	epicID     string
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

// WithEpic puts the collector in epic mode: the shipped section recaps the named
// epic's children instead of the window's activity.
func WithEpic(epicID string) CollectorOption {
	return func(c *Collector) { c.epicID = epicID }
}

// WithSummarizer attaches a summarizer that describes each shipped card. Without
// one, shipped cards render with their titles.
func WithSummarizer(s CardSummarizer) CollectorOption {
	return func(c *Collector) { c.summarizer = s }
}

// NewCollector builds a Collector. Any source may be nil; the digest degrades to
// a note for that section.
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
	p := Payload{GeneratedAt: c.now(), Window: c.window, Mode: ModeDaily}
	if c.epicID != "" {
		p.Mode = ModeEpic
	}

	var shipped []CardMove
	var inProgress []CardMove
	if c.linear == nil {
		p.Notes = append(p.Notes, "linear: not configured (set LINEAR_API_KEY)")
	} else {
		shipped = c.collectShipped(ctx, &p)
		if rem, err := c.linear.Remaining(ctx); err != nil {
			p.Notes = append(p.Notes, "linear remaining: "+err.Error())
		} else {
			p.Remaining = sortCards(rem)
		}
		if ip, err := c.linear.InProgress(ctx); err != nil {
			p.Notes = append(p.Notes, "linear in-progress: "+err.Error())
		} else {
			inProgress = ip
		}
	}

	p.Shipped = c.summarize(ctx, &p, shipped)
	p.Blockers = c.computeBlockers(ctx, &p, inProgress)

	if c.prs != nil {
		if prs, err := c.prs.OpenPRs(ctx); err != nil {
			p.Notes = append(p.Notes, "github pull requests: "+err.Error())
		} else {
			p.OpenPRs = prs
		}
	} else {
		p.Notes = append(p.Notes, "github: not configured")
	}

	return p
}

// collectShipped returns the cards counted as shipped: in epic mode the epic's
// finished children (and records the epic's title), otherwise the finished cards
// from the window's activity.
func (c *Collector) collectShipped(ctx context.Context, p *Payload) []CardMove {
	if c.epicID != "" {
		title, children, err := c.linear.EpicChildren(ctx, c.epicID)
		if err != nil {
			p.Notes = append(p.Notes, "linear epic: "+err.Error())
			return nil
		}
		p.Epic = &EpicSummary{ID: c.epicID, Title: title}
		return doneCards(children)
	}
	moves, err := c.linear.RecentMoves(ctx, c.window)
	if err != nil {
		p.Notes = append(p.Notes, "linear activity: "+err.Error())
		return nil
	}
	return doneCards(moves)
}

// summarize attaches a one-line description to each shipped card. With no
// summarizer, summaries stay empty and renderers fall back to the title. A
// summarizer (or commit-subject) error records a note and degrades the same way;
// the digest never fails on the summarizer.
func (c *Collector) summarize(ctx context.Context, p *Payload, shipped []CardMove) []CardSummary {
	out := make([]CardSummary, len(shipped))
	ids := make([]string, len(shipped))
	for i, card := range shipped {
		out[i] = CardSummary{ID: card.ID, Title: card.Title}
		ids[i] = card.ID
	}
	if len(shipped) == 0 || c.summarizer == nil {
		return out
	}

	var subjects map[string][]string
	if c.commits != nil {
		if s, err := c.commits.SubjectsForCards(ctx, ids); err != nil {
			p.Notes = append(p.Notes, "commit subjects: "+err.Error())
		} else {
			subjects = s
		}
	}

	inputs := make([]CardInput, len(shipped))
	for i, card := range shipped {
		inputs[i] = CardInput{ID: card.ID, Title: card.Title, Subjects: subjects[card.ID]}
	}
	summaries, err := c.summarizer.Summarize(ctx, inputs)
	if err != nil {
		p.Notes = append(p.Notes, "card summaries: "+err.Error())
		return out
	}
	for i := range out {
		out[i].Summary = summaries[out[i].ID]
	}
	return out
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

// doneCards keeps only cards whose state category is completed, in input order.
func doneCards(cards []CardMove) []CardMove {
	var done []CardMove
	for _, card := range cards {
		if card.StateType == stateTypeCompleted {
			done = append(done, card)
		}
	}
	return done
}

// sortCards orders remaining cards by state then identifier, so the section
// reads grouped by state and is deterministic regardless of API ordering.
func sortCards(cards []CardMove) []CardMove {
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].State != cards[j].State {
			return cards[i].State < cards[j].State
		}
		return cards[i].ID < cards[j].ID
	})
	return cards
}
