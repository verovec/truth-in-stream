// Package stats is the source-agnostic statistical-ingestion foundation: it
// renders typed datapoints into self-contained French evidence sentences,
// embeds them, and stores them in the evidence corpus with provenance, plus the
// store-facing contract the offline ingest entrypoint wires together. Concrete
// sources (the EU SDMX adapter in subpackage eurostat) yield the datapoints.
package stats

import (
	"strconv"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// frenchMonths maps a 1-based month number to its French name, used to render a
// monthly period as prose instead of a bare "YYYY-MM".
var frenchMonths = [...]string{
	"", "janvier", "février", "mars", "avril", "mai", "juin",
	"juillet", "août", "septembre", "octobre", "novembre", "décembre",
}

// RenderFrench renders a datapoint into a deterministic, self-contained French
// evidence sentence carrying the figure, unit, period, geography, the
// distinguishing dimension labels, and an exact source citation (publisher,
// dataset code, resolvable URL). The output depends only on the datapoint, so
// the same datapoint always embeds to the same vector. Callers validate the
// datapoint first; rendering itself never fails.
func RenderFrench(d domain.Datapoint) string {
	var b strings.Builder
	b.WriteString(d.Title)

	if dims := nonEmpty(d.Dimensions); len(dims) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(dims, ", "))
		b.WriteByte(')')
	}

	if d.Geography != "" {
		b.WriteString(" en ")
		b.WriteString(d.Geography)
	}

	b.WriteString(" en ")
	b.WriteString(renderPeriod(d.Period))

	b.WriteString(" : ")
	b.WriteString(formatFigure(d.Figure))
	if d.Unit == "%" {
		b.WriteString(" %")
	} else {
		b.WriteByte(' ')
		b.WriteString(d.Unit)
	}
	b.WriteByte('.')

	b.WriteString(" Source : ")
	b.WriteString(d.SourceName)
	b.WriteString(" (jeu de données ")
	b.WriteString(d.Dataset)
	b.WriteString("), ")
	b.WriteString(d.SourceURL)

	return b.String()
}

// nonEmpty drops blank dimension labels so an absent breakdown does not render
// an empty parenthetical.
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// renderPeriod renders an annual period as the year and a monthly period as
// "<month> <year>" in French. A period that does not parse (already rejected by
// Datapoint.Validate before rendering) falls back to the raw string.
func renderPeriod(period string) string {
	parts := strings.SplitN(period, "-", 2)
	if len(parts) == 2 {
		if m, err := strconv.Atoi(parts[1]); err == nil && m >= 1 && m <= 12 {
			return frenchMonths[m] + " " + parts[0]
		}
	}
	return period
}

// formatFigure renders a number in French convention: a regular space as the
// thousands separator and a comma as the decimal separator. Integers render
// without a fractional part. The fixed -1 precision in strconv keeps the
// shortest exact representation, so 66.5 stays "66,5" and 326948 stays an
// integer.
func formatFigure(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)

	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	intPart = groupThousands(intPart)

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(intPart)
	if hasFrac {
		b.WriteByte(',')
		b.WriteString(fracPart)
	}
	return b.String()
}

// groupThousands inserts a space every three digits from the right of an
// all-digit integer string.
func groupThousands(digits string) string {
	n := len(digits)
	if n <= 3 {
		return digits
	}
	lead := n % 3
	var b strings.Builder
	if lead > 0 {
		b.WriteString(digits[:lead])
	}
	for i := lead; i < n; i += 3 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}
