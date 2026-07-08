package vectorbench

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"
)

// Result is one measured cell. A non-empty Skipped explains why the cell was
// not run (for example the server's pgvector predates iterative scans); its
// numeric fields are then meaningless.
type Result struct {
	Cell    Cell
	Recall  float64
	P50     time.Duration
	P95     time.Duration
	Skipped string
}

// Footprint is the on-disk size of one benchmark object (table or index),
// the number instance sizing is derived from.
type Footprint struct {
	Name  string
	Bytes int64
}

// RenderMarkdown renders results and footprints as two markdown tables, ready
// to paste into the verdict document.
func RenderMarkdown(results []Result, footprints []Footprint) string {
	var b strings.Builder
	b.WriteString("| scenario | filter | ef | iter | mult | recall@k | p50 | p95 | note |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range results {
		fields := resultFields(r)
		b.WriteString("| " + strings.Join(fields, " | ") + " |\n")
	}
	b.WriteString("\n| object | size |\n| --- | --- |\n")
	for _, f := range footprints {
		fmt.Fprintf(&b, "| %s | %s |\n", f.Name, formatBytes(f.Bytes))
	}
	return b.String()
}

// RenderText renders results and footprints as aligned plain-text tables for
// terminal output. Writes to the tabwriter cannot fail (the sink is a
// strings.Builder), so their errors are explicitly discarded.
func RenderText(results []Result, footprints []Footprint) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 2, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "scenario\tfilter\tef\titer\tmult\trecall@k\tp50\tp95\tnote")
	for _, r := range results {
		_, _ = fmt.Fprintln(w, strings.Join(resultFields(r), "\t"))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "object\tsize")
	for _, f := range footprints {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", f.Name, formatBytes(f.Bytes))
	}
	_ = w.Flush()
	return b.String()
}

func resultFields(r Result) []string {
	mult := "-"
	if r.Cell.Multiplier > 0 {
		mult = fmt.Sprintf("%d", r.Cell.Multiplier)
	}
	fields := []string{
		string(r.Cell.Scenario),
		string(r.Cell.Filter),
		fmt.Sprintf("%d", r.Cell.EfSearch),
		r.Cell.Iterative,
		mult,
	}
	if r.Skipped != "" {
		return append(fields, "-", "-", "-", "skipped: "+r.Skipped)
	}
	return append(
		fields,
		fmt.Sprintf("%.3f", r.Recall),
		formatMs(r.P50),
		formatMs(r.P95),
		"-",
	)
}

func formatMs(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}
