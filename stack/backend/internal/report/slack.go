package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	slackHTTPTimeout = 10 * time.Second
	// slackSectionCap bounds items per Slack section, and slackSectionByteBudget
	// keeps a section's mrkdwn text under Slack's hard 3000-character limit even
	// when items are long. The terminal renderer carries the full untruncated
	// list. The budget leaves headroom below 3000 for the title and overflow note.
	slackSectionCap        = 12
	slackSectionByteBudget = 2800
)

// SlackRenderer formats a Payload as a Block Kit message and posts it to an
// incoming webhook.
type SlackRenderer struct {
	httpClient *http.Client
	webhookURL string
}

// NewSlackRenderer builds a renderer that posts to webhookURL. An empty URL is
// allowed for RenderJSON (dry runs); Post rejects it.
func NewSlackRenderer(webhookURL string) *SlackRenderer {
	return &SlackRenderer{
		httpClient: &http.Client{Timeout: slackHTTPTimeout},
		webhookURL: webhookURL,
	}
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

type slackMessage struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

// Post sends the digest to the configured webhook.
func (r *SlackRenderer) Post(ctx context.Context, p Payload) error {
	if r.webhookURL == "" {
		return errors.New("report: slack webhook URL is empty")
	}
	body, err := json.Marshal(r.buildMessage(p))
	if err != nil {
		return fmt.Errorf("report: marshal slack message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.webhookURL, bytes.NewReader(body))
	if err != nil {
		// A URL parse failure returns a *url.Error whose message embeds the
		// webhook URL; keep the secret out of the returned (and logged) error.
		return errors.New("report: invalid slack webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		// err may embed the webhook URL; do not surface it into a logged message.
		return errors.New("report: post to slack failed")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report: slack returned status %d", resp.StatusCode)
	}
	return nil
}

// RenderJSON returns the indented Block Kit message, for a dry run.
func (r *SlackRenderer) RenderJSON(p Payload) ([]byte, error) {
	out, err := json.MarshalIndent(r.buildMessage(p), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("report: marshal slack message: %w", err)
	}
	return out, nil
}

func (r *SlackRenderer) buildMessage(p Payload) slackMessage {
	title := digestTitle(p)
	blocks := []slackBlock{
		{Type: "header", Text: &slackText{Type: "plain_text", Text: title}},
		contextBlock(digestContext(p)),
		{Type: "divider"},
		mrkdwnSection("Shipped", slackShippedLines(p.Shipped), shippedEmpty(p)),
		mrkdwnSection("Remaining", slackRemainingLines(p.Remaining), "No cards remaining."),
		mrkdwnSection("Open pull requests", slackPRLines(p.OpenPRs), "No open pull requests."),
		mrkdwnSection("Blockers", slackBlockerLines(p.Blockers), "No stalled In Progress cards."),
	}
	if len(p.Notes) > 0 {
		notes := make([]string, len(p.Notes))
		for i, n := range p.Notes {
			notes[i] = escapeMrkdwn(n)
		}
		blocks = append(blocks, slackBlock{Type: "divider"},
			contextBlock("Notes: "+strings.Join(notes, " | ")))
	}
	return slackMessage{Text: title, Blocks: blocks}
}

func contextBlock(text string) slackBlock {
	return slackBlock{Type: "context", Elements: []slackText{{Type: "mrkdwn", Text: text}}}
}

// mrkdwnSection renders a titled, bullet-listed section, capped for Slack.
func mrkdwnSection(title string, lines []string, empty string) slackBlock {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", title)
	if len(lines) == 0 {
		b.WriteString(empty)
		return slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: b.String()}}
	}
	// Stop at the item cap or the byte budget, whichever comes first, so a few
	// long lines cannot push the section past Slack's per-section character
	// limit. The first line is always included so a section never renders as a
	// title with an overflow note but no items.
	shown := 0
	for _, line := range lines {
		if shown > 0 && (shown >= slackSectionCap || b.Len()+len(line)+1 > slackSectionByteBudget) {
			break
		}
		if shown > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		shown++
	}
	if shown < len(lines) {
		fmt.Fprintf(&b, "\n_and %d more_", len(lines)-shown)
	}
	return slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: b.String()}}
}

func slackShippedLines(cards []CardSummary) []string {
	lines := make([]string, len(cards))
	for i, c := range cards {
		lines[i] = fmt.Sprintf("- *%s* %s", escapeMrkdwn(c.ID), escapeMrkdwn(shippedDescription(c)))
	}
	return lines
}

func slackRemainingLines(cards []CardMove) []string {
	lines := make([]string, len(cards))
	for i, c := range cards {
		lines[i] = fmt.Sprintf("- *%s* %s _(%s)_", escapeMrkdwn(c.ID), escapeMrkdwn(c.Title), escapeMrkdwn(c.State))
	}
	return lines
}

func slackPRLines(prs []PullRequest) []string {
	lines := make([]string, len(prs))
	for i, pr := range prs {
		draft := ""
		if pr.Draft {
			draft = " _(draft)_"
		}
		lines[i] = fmt.Sprintf("- <%s|#%d> %s - _%s_%s", pr.URL, pr.Number, escapeMrkdwn(pr.Title), escapeMrkdwn(pr.Author), draft)
	}
	return lines
}

func slackBlockerLines(blockers []Blocker) []string {
	lines := make([]string, len(blockers))
	for i, b := range blockers {
		lines[i] = fmt.Sprintf("- *%s* %s", escapeMrkdwn(b.ID), escapeMrkdwn(b.Title))
	}
	return lines
}

// digestTitle is the report headline, shared by both renderers: the epic's name
// in epic mode, the dated daily title otherwise.
func digestTitle(p Payload) string {
	if p.Mode == ModeEpic && p.Epic != nil {
		return fmt.Sprintf("Epic recap: %s - %s", p.Epic.ID, p.Epic.Title)
	}
	return "Daily development digest - " + p.GeneratedAt.Format("Mon 2 Jan 2006")
}

// digestContext is the sub-header line: the window for the daily digest, the
// epic identifier for an epic recap, plus the generation time.
func digestContext(p Payload) string {
	if p.Mode == ModeEpic && p.Epic != nil {
		return fmt.Sprintf("Epic %s | generated %s", p.Epic.ID, p.GeneratedAt.Format("15:04 MST"))
	}
	return fmt.Sprintf("Window: last %dh | generated %s", windowHours(p.Window), p.GeneratedAt.Format("15:04 MST"))
}

// shippedEmpty is the placeholder for an empty Shipped section, worded for the
// mode.
func shippedEmpty(p Payload) string {
	if p.Mode == ModeEpic {
		return "No cards shipped in this epic yet."
	}
	return "No cards shipped in the window."
}

// shippedDescription is what a shipped card shows: its synthesized summary, or
// its title when no summary is available.
func shippedDescription(c CardSummary) string {
	if c.Summary != "" {
		return c.Summary
	}
	return c.Title
}

// escapeMrkdwn escapes the three characters Slack mrkdwn reserves so card titles
// and summaries render literally. Ampersand must be replaced first.
func escapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func windowHours(d time.Duration) int {
	h := int(d.Hours())
	if h < 1 {
		h = 1
	}
	return h
}
