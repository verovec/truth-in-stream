package report

import (
	"strings"
	"testing"
	"time"
)

func TestTerminalRenderFullAndUntruncated(t *testing.T) {
	t.Parallel()
	p := Payload{
		GeneratedAt: time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC),
		Window:      24 * time.Hour,
		Notes:       []string{"github: not configured"},
	}
	// More than the Slack cap, to prove the terminal renderer truncates nothing.
	for i := 0; i < slackSectionCap+8; i++ {
		p.Commits = append(p.Commits, Commit{Hash: padHash(i), Author: "Dev", Subject: "commit number " + itoa(i), Files: 1})
	}
	p.CardMoves = []CardMove{{ID: "VER-1", Title: "Card", State: "In Review"}}
	p.OpenPRs = []PullRequest{{Number: 7, Title: "PR", Author: "Bob", URL: "https://gh/7", Draft: true}}
	p.Blockers = []Blocker{{ID: "VER-2", Title: "stalled"}}

	out := TerminalRenderer{}.Render(p)

	for _, want := range []string{
		"Daily development digest",
		"Commits (" + itoa(len(p.Commits)) + ")",
		"Linear activity (1)",
		"Open pull requests (1)",
		"Blockers (1)",
		"VER-1", "VER-2", "#7", "(draft)",
		"github: not configured",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal report missing %q", want)
		}
	}
	// Every commit subject must appear (no truncation).
	for i := 0; i < len(p.Commits); i++ {
		if !strings.Contains(out, "commit number "+itoa(i)) {
			t.Errorf("commit %d truncated from terminal report", i)
		}
	}
}

func TestTerminalRenderEmpty(t *testing.T) {
	t.Parallel()
	out := TerminalRenderer{}.Render(Payload{GeneratedAt: time.Now(), Window: 24 * time.Hour})
	if !strings.Contains(out, "Commits (0)") || !strings.Contains(out, "none") {
		t.Errorf("empty report missing zero-count sections: %s", out)
	}
}

func padHash(i int) string { return "hash" + itoa(i) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
