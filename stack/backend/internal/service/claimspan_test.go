package service

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestLocateQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		text        string
		quote       string
		wantStart   int
		wantEnd     int
		wantMissing bool
	}{
		{
			name:      "exact substring",
			text:      "Le chomage a baisse. Les impots montent.",
			quote:     "Les impots montent",
			wantStart: 21,
			wantEnd:   39,
		},
		{
			name:      "case difference tolerated",
			text:      "Le chomage a baisse.",
			quote:     "le Chomage A baisse",
			wantStart: 0,
			wantEnd:   19,
		},
		{
			name:      "whitespace runs collapse on both sides",
			text:      "Le chomage  a\tbaisse fortement.",
			quote:     "chomage a baisse",
			wantStart: 3,
			wantEnd:   20,
		},
		{
			name:      "multibyte offsets count runes not bytes",
			text:      "Écoutez. Ça va très bien.",
			quote:     "Ça va très bien",
			wantStart: 9,
			wantEnd:   24,
		},
		{
			name:        "absent quote reports missing",
			text:        "Le chomage a baisse.",
			quote:       "les impots montent",
			wantMissing: true,
		},
		{
			name:        "empty quote reports missing",
			text:        "Le chomage a baisse.",
			quote:       "",
			wantMissing: true,
		},
		{
			name:        "whitespace-only quote reports missing",
			text:        "Le chomage a baisse.",
			quote:       "   ",
			wantMissing: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start, end, ok := locateQuote(tc.text, tc.quote)
			if tc.wantMissing {
				if ok {
					t.Fatalf("locateQuote found [%d, %d), want a miss", start, end)
				}
				return
			}
			if !ok {
				t.Fatal("locateQuote missed, want a hit")
			}
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("locateQuote = [%d, %d), want [%d, %d)", start, end, tc.wantStart, tc.wantEnd)
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
			got := claimSpans(members, tc.quote)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("claimSpans mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
