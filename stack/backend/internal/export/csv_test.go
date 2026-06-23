package export_test

import (
	"bytes"
	"encoding/csv"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/export"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

func parseCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	return rows
}

func headerIndex(t *testing.T, header []string) map[string]int {
	t.Helper()
	idx := make(map[string]int, len(header))
	for i, name := range header {
		idx[name] = i
	}
	return idx
}

func TestCSVHeaderAndVerifiedClaimRow(t *testing.T) {
	events := []service.LiveEvent{
		{
			Kind: service.LiveEventResult,
			ID:   "stmt-1",
			Segment: domain.Segment{
				Start:   2 * time.Second,
				End:     5 * time.Second,
				Speaker: "Speaker 1",
				Text:    "Unemployment fell last year.",
			},
			ClaimID:     "claim-1",
			ClaimStatus: service.ClaimStatusVerified,
			Source:      service.SourceVerified,
			Verdict: &service.VerifiedVerdict{
				Verdict:    "disputed",
				Basis:      "evidence",
				Confidence: 0.82,
				Rationale:  "Official series shows a rise, not a fall.",
				Literal:    "inaccurate",
				Flags:      []string{"missing-context", "cherry-picked"},
				Citations: []domain.SegmentMatch{
					{
						Kind:       domain.MatchKindEvidence,
						Sources:    []domain.Source{{Title: "INSEE", URL: "https://insee.fr/x"}},
						Similarity: 0.91,
						EvidenceID: "ev-1",
					},
				},
			},
		},
	}

	rows := parseCSV(t, export.CSV(events))
	if len(rows) != 2 {
		t.Fatalf("want header + 1 row, got %d rows", len(rows))
	}
	idx := headerIndex(t, rows[0])
	for _, col := range []string{
		"segment_start", "segment_end", "speaker", "statement", "claim_id",
		"claim_status", "skip_reason", "verdict_source", "credibility_verdict",
		"literal_verdict", "manipulation_flags", "basis", "confidence",
		"rationale", "citations",
	} {
		if _, ok := idx[col]; !ok {
			t.Fatalf("missing column %q in header %v", col, rows[0])
		}
	}

	row := rows[1]
	checks := map[string]string{
		"segment_start":       "00:00:02,000",
		"segment_end":         "00:00:05,000",
		"speaker":             "Speaker 1",
		"statement":           "Unemployment fell last year.",
		"claim_id":            "claim-1",
		"claim_status":        "verified",
		"skip_reason":         "",
		"verdict_source":      "verified",
		"credibility_verdict": "disputed",
		"literal_verdict":     "inaccurate",
		"manipulation_flags":  "missing-context | cherry-picked",
		"basis":               "evidence",
		"confidence":          "0.82",
		"rationale":           "Official series shows a rise, not a fall.",
	}
	for col, want := range checks {
		if got := row[idx[col]]; got != want {
			t.Fatalf("column %q = %q, want %q", col, got, want)
		}
	}
	if got := row[idx["citations"]]; got != "INSEE <https://insee.fr/x> [ev-1] sim=0.91" {
		t.Fatalf("citations = %q", got)
	}
}

func TestCSVSkippedClaimRowHasReasonAndNoVerdict(t *testing.T) {
	events := []service.LiveEvent{
		{
			Kind: service.LiveEventResult,
			ID:   "stmt-2",
			Segment: domain.Segment{
				Start:   time.Second,
				End:     2 * time.Second,
				Speaker: "Speaker 2",
				Text:    "Some unverifiable assertion.",
			},
			ClaimID:     "claim-2",
			ClaimStatus: service.ClaimStatusUnchecked,
			SkipReason:  domain.SkipReasonNotChecked,
		},
	}

	rows := parseCSV(t, export.CSV(events))
	if len(rows) != 2 {
		t.Fatalf("want header + 1 row, got %d", len(rows))
	}
	idx := headerIndex(t, rows[0])
	row := rows[1]
	if got := row[idx["claim_status"]]; got != "unchecked" {
		t.Fatalf("claim_status = %q, want unchecked", got)
	}
	if got := row[idx["skip_reason"]]; got != "not_checked" {
		t.Fatalf("skip_reason = %q, want not_checked", got)
	}
	if got := row[idx["credibility_verdict"]]; got != "" {
		t.Fatalf("expected empty verdict for a skipped claim, got %q", got)
	}
	if got := row[idx["citations"]]; got != "" {
		t.Fatalf("expected no citations for a skipped claim, got %q", got)
	}
}

func TestCSVLegacyStatementResultRow(t *testing.T) {
	events := []service.LiveEvent{
		{
			Kind: service.LiveEventResult,
			ID:   "stmt-3",
			Segment: domain.Segment{
				Start:   0,
				End:     time.Second,
				Speaker: "Speaker 1",
				Text:    "The earth orbits the sun.",
			},
			Matches: []domain.SegmentMatch{
				{
					Kind:       domain.MatchKindClaim,
					Claim:      "Heliocentrism",
					Verdict:    domain.VerdictCorroborates,
					Sources:    []domain.Source{{Title: "NASA", URL: "https://nasa.gov"}},
					Similarity: 0.88,
				},
			},
			Confidence: &domain.Confidence{Score: 0.95},
		},
	}

	rows := parseCSV(t, export.CSV(events))
	idx := headerIndex(t, rows[0])
	row := rows[1]
	if got := row[idx["claim_status"]]; got != "checked" {
		t.Fatalf("legacy checked statement claim_status = %q, want checked", got)
	}
	if got := row[idx["credibility_verdict"]]; got != "corroborates" {
		t.Fatalf("legacy verdict = %q, want corroborates", got)
	}
	if got := row[idx["confidence"]]; got != "0.95" {
		t.Fatalf("confidence = %q, want 0.95", got)
	}
	if got := row[idx["citations"]]; got != "NASA <https://nasa.gov> sim=0.88" {
		t.Fatalf("citations = %q", got)
	}
}

func TestCSVErrorClaimRow(t *testing.T) {
	events := []service.LiveEvent{
		{
			Kind:        service.LiveEventResult,
			ID:          "stmt-4",
			Segment:     domain.Segment{Text: "Failed claim."},
			ClaimID:     "claim-4",
			ClaimStatus: service.ClaimStatusError,
			Err:         "retrieval timeout",
		},
	}

	rows := parseCSV(t, export.CSV(events))
	idx := headerIndex(t, rows[0])
	row := rows[1]
	if got := row[idx["claim_status"]]; got != "error" {
		t.Fatalf("claim_status = %q, want error", got)
	}
	if got := row[idx["skip_reason"]]; got != "retrieval timeout" {
		t.Fatalf("error reason should appear in skip_reason, got %q", got)
	}
}

func TestCSVEscapesSpecialCharacters(t *testing.T) {
	events := []service.LiveEvent{
		{
			Kind: service.LiveEventResult,
			ID:   "stmt-5",
			Segment: domain.Segment{
				Speaker: "Le « Président »",
				Text:    "Il a dit: \"oui, non\", puis\nil est parti.",
			},
			ClaimID:     "claim-5",
			ClaimStatus: service.ClaimStatusVerified,
			Source:      service.SourceVerified,
			Verdict: &service.VerifiedVerdict{
				Verdict:   "credible",
				Rationale: "Comma, \"quote\", and\nnewline.",
			},
		},
	}

	raw := export.CSV(events)
	rows := parseCSV(t, raw)
	idx := headerIndex(t, rows[0])
	row := rows[1]
	if got := row[idx["statement"]]; got != "Il a dit: \"oui, non\", puis\nil est parti." {
		t.Fatalf("statement round-trip failed: %q", got)
	}
	if got := row[idx["rationale"]]; got != "Comma, \"quote\", and\nnewline." {
		t.Fatalf("rationale round-trip failed: %q", got)
	}
}

func TestCSVDropsNonTerminalClaimPlaceholders(t *testing.T) {
	events := []service.LiveEvent{
		{
			Kind:        service.LiveEventResult,
			ID:          "stmt-6",
			Segment:     domain.Segment{Text: "A claim being checked."},
			ClaimID:     "claim-6",
			ClaimStatus: service.ClaimStatusChecking,
		},
		{
			Kind:        service.LiveEventResult,
			ID:          "stmt-6",
			Segment:     domain.Segment{Text: "A claim being checked."},
			ClaimID:     "claim-6",
			ClaimStatus: service.ClaimStatusVerified,
			Source:      service.SourceVerified,
			Verdict:     &service.VerifiedVerdict{Verdict: "credible"},
		},
	}

	rows := parseCSV(t, export.CSV(events))
	if len(rows) != 2 {
		t.Fatalf("want header + 1 terminal row (checking placeholder dropped), got %d rows", len(rows))
	}
	idx := headerIndex(t, rows[0])
	if got := rows[1][idx["claim_status"]]; got != "verified" {
		t.Fatalf("kept row claim_status = %q, want verified", got)
	}
}

func TestCSVIgnoresNonResultEvents(t *testing.T) {
	events := []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, ID: "x", Segment: domain.Segment{Text: "caption"}},
		{Kind: service.LiveEventInterim},
		{Kind: service.LiveEventClaims, ID: "x", Claims: []service.AtomicClaim{{ClaimID: "c", Text: "a claim"}}},
		{Kind: service.LiveEventSpeakerTally, SpeakerTally: &service.SpeakerTally{Speaker: "S"}},
	}

	rows := parseCSV(t, export.CSV(events))
	if len(rows) != 1 {
		t.Fatalf("want header only, got %d rows: %v", len(rows), rows)
	}
}
