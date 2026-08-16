package hatvp

import (
	"testing"
	"time"
)

func TestPublicationDate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		row  indexRow
		decl declaration
		want *time.Time
	}{
		{
			"index publication date wins",
			indexRow{DatePublication: "2025-06-17", DateDepot: "2024-08-04"},
			declaration{DateDepot: "04/08/2024 11:04:36"},
			datePtr(2025, 6, 17),
		},
		{
			"index depot fallback",
			indexRow{DateDepot: "2024-08-04"},
			declaration{},
			datePtr(2024, 8, 4),
		},
		{
			"xml french depot fallback",
			indexRow{},
			declaration{DateDepot: "04/08/2024 11:04:36"},
			timePtr(time.Date(2024, 8, 4, 11, 4, 36, 0, time.UTC)),
		},
		{"no date stays nil", indexRow{}, declaration{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := publicationDate(tc.row, tc.decl)
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("publicationDate = %v, want nil", got)
			case tc.want != nil && (got == nil || !got.Equal(*tc.want)):
				t.Errorf("publicationDate = %v, want %v", got, tc.want)
			}
		})
	}
}

func datePtr(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func timePtr(t time.Time) *time.Time { return &t }
