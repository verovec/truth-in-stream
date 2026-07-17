package service

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestLocateQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		text  string
		quote string
		want  []runeRange
	}{
		{
			name:  "exact substring",
			text:  "Le chomage a baisse. Les impots montent.",
			quote: "Les impots montent",
			want:  []runeRange{{start: 21, end: 39}},
		},
		{
			name:  "case difference tolerated",
			text:  "Le chomage a baisse.",
			quote: "le Chomage A baisse",
			want:  []runeRange{{start: 0, end: 19}},
		},
		{
			name:  "whitespace runs collapse on both sides",
			text:  "Le chomage  a\tbaisse fortement.",
			quote: "chomage a baisse",
			want:  []runeRange{{start: 3, end: 20}},
		},
		{
			name:  "multibyte offsets count runes not bytes",
			text:  "Écoutez. Ça va très bien.",
			quote: "Ça va très bien",
			want:  []runeRange{{start: 9, end: 24}},
		},
		{
			name:  "a repeated quote anchors every occurrence",
			text:  "Le chomage a baisse. Je repete: le chomage a baisse.",
			quote: "chomage a baisse",
			want:  []runeRange{{start: 3, end: 19}, {start: 35, end: 51}},
		},
		{
			name:  "absent quote yields nothing",
			text:  "Le chomage a baisse.",
			quote: "les impots montent",
			want:  nil,
		},
		{
			name:  "empty quote yields nothing",
			text:  "Le chomage a baisse.",
			quote: "",
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := locateQuote(tc.text, tc.quote)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(runeRange{})); diff != "" {
				t.Errorf("locateQuote mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClaimSpans(t *testing.T) {
	t.Parallel()
	members := []unitMember{
		{id: "0", seg: domain.Segment{Text: "Le chomage a baisse."}},
		{id: "1", seg: domain.Segment{Text: "Les impots montent fortement."}},
	}
	text := combinedText(members)
	tests := []struct {
		name  string
		quote string
		want  []domain.ClaimSpan
	}{
		{
			name:  "quote inside one member yields one local span",
			quote: "impots montent",
			want:  []domain.ClaimSpan{{SegmentID: "1", Start: 4, End: 18}},
		},
		{
			name:  "quote crossing the boundary yields one span per member",
			quote: "baisse. Les impots",
			want: []domain.ClaimSpan{
				{SegmentID: "0", Start: 13, End: 20},
				{SegmentID: "1", Start: 0, End: 10},
			},
		},
		{
			name:  "whitespace-only quote yields no spans",
			quote: "   ",
			want:  nil,
		},
		{
			name:  "unlocatable quote yields no spans",
			quote: "la dette augmente",
			want:  nil,
		},
		{
			name:  "empty quote yields no spans",
			quote: "",
			want:  nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := claimSpans(members, text, tc.quote)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("claimSpans mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestClaimSpansRepeatedQuoteMarksEveryOccurrence(t *testing.T) {
	t.Parallel()
	// The same words in two different segments: the decomposer does not say
	// which repetition it quoted, so both rows are anchored rather than
	// guessing one and highlighting the wrong row.
	members := []unitMember{
		{id: "0", seg: domain.Segment{Text: "Le chomage a baisse."}},
		{id: "1", seg: domain.Segment{Text: "Je repete: le chomage a baisse."}},
	}
	got := claimSpans(members, combinedText(members), "chomage a baisse")
	want := []domain.ClaimSpan{
		{SegmentID: "0", Start: 3, End: 19},
		{SegmentID: "1", Start: 14, End: 30},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("claimSpans mismatch (-want +got):\n%s", diff)
	}
}
