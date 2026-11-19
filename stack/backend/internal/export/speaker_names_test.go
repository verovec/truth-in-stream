package export_test

import (
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/export"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

func TestSRTPrefixesTheResolvedNameAndFallsBackToTheLabel(t *testing.T) {
	events := []service.LiveEvent{
		subtitle("a", 0, time.Second, "A", "Bonsoir."),
		subtitle("b", time.Second, 2*time.Second, "B", "Merci."),
		subtitle("c", 2*time.Second, 3*time.Second, "", "Musique."),
	}
	names := map[string]string{"A": "Marine Durand", "B": ""}

	got := string(export.SRT(events, names))

	for _, want := range []string{"\nMarine Durand: Bonsoir.\n", "\nB: Merci.\n", "\nMusique.\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("SRT lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "A: Bonsoir.") {
		t.Errorf("SRT kept the letter for a named speaker:\n%s", got)
	}
}

func TestCSVCarriesTheResolvedNameBesideTheLabel(t *testing.T) {
	result := func(speaker string) service.LiveEvent {
		return service.LiveEvent{
			Kind: service.LiveEventResult, ClaimID: "c-" + speaker, ClaimStatus: service.ClaimStatusUnchecked,
			Segment: domain.Segment{Speaker: speaker, Text: "Le chômage a baissé."},
		}
	}
	events := []service.LiveEvent{result("A"), result("B")}

	rows := parseCSV(t, export.CSV(events, map[string]string{"A": "Marine Durand"}))
	if len(rows) != 3 {
		t.Fatalf("want header + 2 rows, got %d", len(rows))
	}
	idx := headerIndex(t, rows[0])
	if idx["speaker_name"] != len(rows[0])-1 {
		t.Errorf("speaker_name must be the last column so earlier positions are stable, got index %d", idx["speaker_name"])
	}
	if rows[1][idx["speaker"]] != "A" || rows[1][idx["speaker_name"]] != "Marine Durand" {
		t.Errorf("named row = %v", rows[1])
	}
	if rows[2][idx["speaker"]] != "B" || rows[2][idx["speaker_name"]] != "" {
		t.Errorf("unnamed row = %v", rows[2])
	}
}
