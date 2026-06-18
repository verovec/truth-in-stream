package report

import (
	"fmt"
	"strings"
)

// TerminalRenderer formats a Payload as a detailed, untruncated terminal
// report. Unlike the Slack renderer it caps nothing, so /report shows the full
// picture.
type TerminalRenderer struct{}

// Render returns the report as plain text.
func (TerminalRenderer) Render(p Payload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", digestTitle(p))
	fmt.Fprintf(&b, "%s | generated %s\n", digestContext(p), p.GeneratedAt.Format("Mon 2 Jan 2006 15:04 MST"))
	b.WriteString(strings.Repeat("=", 60) + "\n\n")

	heading(&b, fmt.Sprintf("Shipped (%d)", len(p.Shipped)))
	if len(p.Shipped) == 0 {
		fmt.Fprintf(&b, "  %s\n", shippedEmpty(p))
	}
	for _, c := range p.Shipped {
		fmt.Fprintf(&b, "  %-8s %s\n", c.ID, shippedDescription(c))
	}
	b.WriteString("\n")

	heading(&b, fmt.Sprintf("Remaining (%d)", len(p.Remaining)))
	if len(p.Remaining) == 0 {
		b.WriteString("  none\n")
	}
	for _, c := range p.Remaining {
		fmt.Fprintf(&b, "  %-8s %s [%s]\n", c.ID, c.Title, c.State)
	}
	b.WriteString("\n")

	heading(&b, fmt.Sprintf("Open pull requests (%d)", len(p.OpenPRs)))
	if len(p.OpenPRs) == 0 {
		b.WriteString("  none\n")
	}
	for _, pr := range p.OpenPRs {
		draft := ""
		if pr.Draft {
			draft = " (draft)"
		}
		fmt.Fprintf(&b, "  #%-4d %s%s\n", pr.Number, pr.Title, draft)
		fmt.Fprintf(&b, "        %s  %s\n", pr.Author, pr.URL)
	}
	b.WriteString("\n")

	heading(&b, fmt.Sprintf("Blockers (%d)", len(p.Blockers)))
	if len(p.Blockers) == 0 {
		b.WriteString("  none\n")
	}
	for _, bl := range p.Blockers {
		fmt.Fprintf(&b, "  %-8s %s\n", bl.ID, bl.Title)
	}
	b.WriteString("\n")

	if len(p.Notes) > 0 {
		heading(&b, "Notes")
		for _, n := range p.Notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}
	return b.String()
}

func heading(b *strings.Builder, title string) {
	fmt.Fprintf(b, "%s\n%s\n", title, strings.Repeat("-", len(title)))
}
