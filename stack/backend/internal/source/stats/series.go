// Package stats retrieves official statistics as evidence for the fact-check
// verify path. Its defining job is to return the surrounding series, not just
// the cited point: a verifier can only flag a cherry-picked timeframe if it can
// see the adjacent periods. The package wraps two keyless French/EU statistics
// APIs (INSEE BDM and Eurostat) behind the source.Retriever contract; their wire
// formats never leak past this package.
package stats

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// Observation is one period of a statistics series. Period is the source-native
// time label (e.g. "2024-Q4", "2023"); Value is the figure; Missing marks a
// period the source reported with no value (INSEE "NaN", a Eurostat gap), which
// is itself evidence the verifier should see rather than have silently dropped.
type Observation struct {
	Period  string
	Value   float64
	Missing bool
}

// Series is a single statistics series with its provenance: a title, unit, the
// source-stable id (INSEE IDBANK / Eurostat dataset code), the canonical URL,
// the last-updated date, and the observations. Observations are ordered oldest
// to newest so the timeline reads naturally in the rendered passage.
type Series struct {
	SourceID    string
	Title       string
	Unit        string
	URL         string
	LastUpdated string
	Obs         []Observation
}

// sortChronologically orders observations oldest-first by period label. SDMX
// period labels ("2024-Q4", "2023-03", "2022") sort lexicographically in
// chronological order within a single frequency, which is what a single series
// carries.
func (s *Series) sortChronologically() {
	slices.SortStableFunc(s.Obs, func(a, b Observation) int {
		return cmp.Compare(a.Period, b.Period)
	})
}

// render formats the whole series into a single evidence passage the verifier
// reads. Every period is listed so an adjacent-period cherry-pick is visible;
// missing periods are shown as such rather than omitted.
func (s *Series) render() string {
	var b strings.Builder
	b.WriteString(s.Title)
	if s.Unit != "" {
		b.WriteString(" (")
		b.WriteString(s.Unit)
		b.WriteString(")")
	}
	b.WriteString("\n")
	for _, o := range s.Obs {
		b.WriteString(o.Period)
		b.WriteString(": ")
		if o.Missing {
			b.WriteString("indisponible")
		} else {
			b.WriteString(strconv.FormatFloat(o.Value, 'f', -1, 64))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
