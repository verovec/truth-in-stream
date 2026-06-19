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

	connector, period := renderPeriod(d.Period)
	b.WriteByte(' ')
	b.WriteString(connector)
	b.WriteByte(' ')
	b.WriteString(period)

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

// renderPeriod renders a period into French prose and the preposition that
// precedes it: an annual period is "en <year>", a monthly period is "en <month>
// <year>", and an INSEE BDM quarterly period is "au <n>er/e trimestre <year>". A
// period that does not parse (already rejected by Datapoint.Validate before
// rendering) falls back to "en <raw>".
func renderPeriod(period string) (connector, text string) {
	head, tail, hasTail := strings.Cut(period, "-")
	if !hasTail {
		return "en", period
	}
	if q, ok := strings.CutPrefix(tail, "Q"); ok {
		if quarter, err := strconv.Atoi(q); err == nil && quarter >= 1 && quarter <= 4 {
			return "au", frenchQuarter(quarter) + " " + head
		}
		return "en", period
	}
	if m, err := strconv.Atoi(tail); err == nil && m >= 1 && m <= 12 {
		return "en", frenchMonths[m] + " " + head
	}
	return "en", period
}

// frenchQuarter renders a quarter [1,4] as the French ordinal trimester label,
// e.g. "1er trimestre" or "2e trimestre".
func frenchQuarter(quarter int) string {
	if quarter == 1 {
		return "1er trimestre"
	}
	return strconv.Itoa(quarter) + "e trimestre"
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
