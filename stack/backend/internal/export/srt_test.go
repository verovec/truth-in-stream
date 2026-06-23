package export_test

import (
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/export"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

func subtitle(id string, start, end time.Duration, speaker, text string) service.LiveEvent {
	return service.LiveEvent{
		Kind: service.LiveEventSubtitle,
		ID:   id,
		Segment: domain.Segment{
			Start:   start,
			End:     end,
			Speaker: speaker,
			Text:    text,
		},
	}
}

func TestSRTFormatsCuesInOrder(t *testing.T) {
	events := []service.LiveEvent{
		subtitle("a", 0, 2*time.Second+500*time.Millisecond, "", "Hello world"),
		subtitle("b", 3*time.Second, 5*time.Second, "Speaker 1", "Second line"),
	}

	got := string(export.SRT(events))

	want := strings.Join([]string{
		"1",
		"00:00:00,000 --> 00:00:02,500",
		"Hello world",
		"",
		"2",
		"00:00:03,000 --> 00:00:05,000",
		"Speaker 1: Second line",
		"",
		"",
	}, "\n")

	if got != want {
		t.Fatalf("SRT mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestSRTSkipsInterimAndNonSubtitleEvents(t *testing.T) {
	events := []service.LiveEvent{
		{Kind: service.LiveEventInterim, Segment: domain.Segment{Text: "partial..."}},
		subtitle("a", 0, time.Second, "", "Final caption"),
		{Kind: service.LiveEventResult, ID: "a"},
		{Kind: service.LiveEventClaims, ID: "a"},
	}

	got := string(export.SRT(events))

	if strings.Contains(got, "partial") {
		t.Fatalf("interim caption leaked into SRT:\n%s", got)
	}
	if want := "1\n00:00:00,000 --> 00:00:01,000\nFinal caption\n"; !strings.Contains(got, want) {
		t.Fatalf("missing finalized cue:\n%s", got)
	}
	if strings.Contains(got, "2\n") {
		t.Fatalf("non-subtitle event produced a cue:\n%s", got)
	}
}

func TestSRTTimestampZeroPaddingAndHours(t *testing.T) {
	events := []service.LiveEvent{
		subtitle("a",
			time.Hour+2*time.Minute+3*time.Second+40*time.Millisecond,
			time.Hour+2*time.Minute+4*time.Second+5*time.Millisecond,
			"", "Late cue"),
	}

	got := string(export.SRT(events))

	if want := "01:02:03,040 --> 01:02:04,005"; !strings.Contains(got, want) {
		t.Fatalf("timestamp formatting wrong, want %q in:\n%s", want, got)
	}
}

func TestSRTPreservesMultilineTextAndAccents(t *testing.T) {
	events := []service.LiveEvent{
		subtitle("a", 0, time.Second, "Élu", "Première ligne\nDeuxième ligne"),
	}

	got := string(export.SRT(events))

	if !strings.Contains(got, "Élu: Première ligne\nDeuxième ligne") {
		t.Fatalf("accented multiline text not preserved:\n%s", got)
	}
}

func TestSRTEmptyWhenNoFinalizedSegments(t *testing.T) {
	events := []service.LiveEvent{
		{Kind: service.LiveEventInterim, Segment: domain.Segment{Text: "still typing"}},
	}

	if got := export.SRT(events); len(got) != 0 {
		t.Fatalf("expected empty SRT, got %q", got)
	}
}
