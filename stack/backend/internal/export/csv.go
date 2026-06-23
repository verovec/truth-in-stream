package export

import (
	"bytes"
	"encoding/csv"
	"strconv"
	"strings"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// multiValueDelimiter joins the values of a multi-value cell (manipulation
// flags, citations) into one stable, human-readable field. encoding/csv quotes
// the cell when needed, so the delimiter never has to be CSV-special.
const multiValueDelimiter = " | "

var csvHeader = []string{
	"segment_start",
	"segment_end",
	"speaker",
	"statement",
	"claim_id",
	"claim_status",
	"skip_reason",
	"verdict_source",
	"credibility_verdict",
	"literal_verdict",
	"manipulation_flags",
	"basis",
	"confidence",
	"rationale",
	"citations",
}

// CSV renders the fact-check decision trace of a snapshot: a header row plus one
// row per result event (LiveEventResult), in order. Each row captures the full
// trace behind a verdict - the segment timing, speaker and statement, the claim
// id and status, the skip reason when a claim was not checked, the verdict source
// and credibility verdict, the political path's literal axis and manipulation
// flags, the basis, confidence and one-line rationale, and every source citation
// behind the decision. A detected-but-unchecked claim still produces a row, so
// "what is unverifiable and why" is explicit. Multi-value cells are joined with a
// stable delimiter; encoding/csv handles all quoting and escaping. Non-result
// events (subtitles, interims, claim announcements, speaker tallies) produce no
// rows.
func CSV(events []service.LiveEvent) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(csvHeader)
	for _, ev := range events {
		if ev.Kind != service.LiveEventResult {
			continue
		}
		if isNonTerminalClaim(ev) {
			continue
		}
		_ = w.Write(resultRow(ev))
	}
	w.Flush()
	return buf.Bytes()
}

// isNonTerminalClaim reports a verify-path per-claim result that is only an
// intermediate placeholder (pending or checking) the live UI replaces in place
// once the terminal verdict arrives. The decision trace records one row per claim
// from its terminal event (verified, unchecked, or error), so these are dropped
// to avoid a duplicate row per claim. A statement-level result (no ClaimID) is
// always terminal and never dropped.
func isNonTerminalClaim(ev service.LiveEvent) bool {
	if ev.ClaimID == "" {
		return false
	}
	return ev.ClaimStatus == service.ClaimStatusPending || ev.ClaimStatus == service.ClaimStatusChecking
}

// resultRow flattens one result event into its CSV columns. A per-claim event
// (verify path, ClaimID set) reads its status and verdict from the claim fields;
// a statement-level event (legacy path) derives its status from the skip reason
// and its verdict from the best curated match.
func resultRow(ev service.LiveEvent) []string {
	row := make([]string, len(csvHeader))
	row[0] = srtTimestamp(ev.Segment.Start)
	row[1] = srtTimestamp(ev.Segment.End)
	row[2] = ev.Segment.Speaker
	row[3] = ev.Segment.Text
	row[4] = ev.ClaimID

	if ev.ClaimID != "" {
		fillClaimColumns(row, ev)
	} else {
		fillStatementColumns(row, ev)
	}
	return row
}

// fillClaimColumns populates the verdict columns for a verify-path per-claim
// result. The skip reason carries the capacity reason for an unchecked claim or
// the failure reason for an errored one, so an operator sees why a claim has no
// verdict.
func fillClaimColumns(row []string, ev service.LiveEvent) {
	row[5] = string(ev.ClaimStatus)
	row[6] = claimSkipReason(ev)
	row[7] = string(ev.Source)
	if v := ev.Verdict; v != nil {
		row[8] = v.Verdict
		row[9] = v.Literal
		row[10] = strings.Join(v.Flags, multiValueDelimiter)
		row[11] = v.Basis
		row[12] = formatConfidence(v.Confidence)
		row[13] = v.Rationale
		row[14] = formatCitations(v.Citations)
	}
}

// fillStatementColumns populates the verdict columns for a legacy statement-level
// result. A statement with no skip reason was checked; the credibility verdict and
// citations come from its curated matches, and the confidence from its aggregate.
func fillStatementColumns(row []string, ev service.LiveEvent) {
	if ev.SkipReason != domain.SkipReasonNone {
		row[5] = "skipped"
		row[6] = string(ev.SkipReason)
		return
	}
	row[5] = "checked"
	if match, ok := primaryClaimMatch(ev.Matches); ok {
		row[8] = string(match.Verdict)
	}
	if ev.Confidence != nil {
		row[12] = formatConfidence(ev.Confidence.Score)
	}
	row[14] = formatCitations(ev.Matches)
}

// claimSkipReason is the reason a per-claim verdict is absent: the explicit skip
// reason when set, otherwise the non-fatal analysis error for an errored claim.
func claimSkipReason(ev service.LiveEvent) string {
	if ev.SkipReason != domain.SkipReasonNone {
		return string(ev.SkipReason)
	}
	return ev.Err
}

// primaryClaimMatch returns the first curated claim match, which carries the
// statement's borrowed verdict; evidence-only matches carry no verdict.
func primaryClaimMatch(matches []domain.SegmentMatch) (domain.SegmentMatch, bool) {
	for _, m := range matches {
		if m.Kind == domain.MatchKindClaim && m.Verdict != "" {
			return m, true
		}
	}
	return domain.SegmentMatch{}, false
}

// formatCitations renders the sources behind a verdict as one stable cell. Each
// match contributes one entry per source title+URL, with the evidence id (when
// the retrieve-then-verify path set one) and the match similarity, so the
// grounding is auditable from the spreadsheet alone.
func formatCitations(matches []domain.SegmentMatch) string {
	var entries []string
	for _, m := range matches {
		suffix := citationSuffix(m)
		if len(m.Sources) == 0 {
			continue
		}
		for _, s := range m.Sources {
			entries = append(entries, s.Title+" <"+s.URL+">"+suffix)
		}
	}
	return strings.Join(entries, multiValueDelimiter)
}

// citationSuffix is the per-match trailer appended to each of its sources: the
// evidence id when present, then the similarity, each space-prefixed so a missing
// piece leaves no dangling separator.
func citationSuffix(m domain.SegmentMatch) string {
	var b strings.Builder
	if m.EvidenceID != "" {
		b.WriteString(" [" + m.EvidenceID + "]")
	}
	b.WriteString(" sim=" + strconv.FormatFloat(m.Similarity, 'f', -1, 64))
	return b.String()
}

// formatConfidence renders a confidence score with two decimals, the precision an
// operator reads, while preserving 0 as a meaningful "no corroboration" value.
func formatConfidence(score float64) string {
	return strconv.FormatFloat(score, 'f', 2, 64)
}
