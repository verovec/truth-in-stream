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
		p.Shipped = append(p.Shipped, CardSummary{ID: "VER-" + itoa(i), Title: "t", Summary: "summary number " + itoa(i)})
	}
	p.Remaining = []CardMove{{ID: "VER-1", Title: "Card", State: "In Review"}}
	p.OpenPRs = []PullRequest{{Number: 7, Title: "PR", Author: "Bob", URL: "https://gh/7", Draft: true}}
	p.Blockers = []Blocker{{ID: "VER-2", Title: "stalled"}}

	out := TerminalRenderer{}.Render(p)

	for _, want := range []string{
		"Daily development digest",
		"Shipped (" + itoa(len(p.Shipped)) + ")",
		"Remaining (1)",
		"Open pull requests (1)",
		"Blockers (1)",
		"VER-1", "VER-2", "#7", "(draft)",
		"github: not configured",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal report missing %q", want)
		}
	}
	// Every shipped summary must appear (no truncation).
	for i := 0; i < len(p.Shipped); i++ {
		if !strings.Contains(out, "summary number "+itoa(i)) {
			t.Errorf("shipped %d truncated from terminal report", i)
		}
	}
}

func TestTerminalRenderEpicHeader(t *testing.T) {
	t.Parallel()
	out := TerminalRenderer{}.Render(Payload{
		GeneratedAt: time.Now(),
		Mode:        ModeEpic,
		Epic:        &EpicSummary{ID: "VER-93", Title: "Political fact-check"},
	})
	if !strings.Contains(out, "Epic recap: VER-93 - Political fact-check") {
		t.Errorf("epic header missing from terminal report: %s", out)
	}
}

func TestTerminalShippedFallsBackToTitle(t *testing.T) {
	t.Parallel()
	out := TerminalRenderer{}.Render(Payload{
		GeneratedAt: time.Now(),
		Shipped:     []CardSummary{{ID: "VER-1", Title: "Bare title"}},
	})
	if !strings.Contains(out, "Bare title") {
		t.Errorf("shipped card without a summary must show its title: %s", out)
	}
}

func TestTerminalRenderEmpty(t *testing.T) {
	t.Parallel()
	out := TerminalRenderer{}.Render(Payload{GeneratedAt: time.Now(), Window: 24 * time.Hour})
	if !strings.Contains(out, "Shipped (0)") || !strings.Contains(out, "none") {
		t.Errorf("empty report missing zero-count sections: %s", out)
	}
}

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
