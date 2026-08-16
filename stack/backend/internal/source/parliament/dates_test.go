package parliament

import (
	"testing"
	"time"
)

func TestDocumentDate(t *testing.T) {
	t.Parallel()
	want := time.Date(2025, 7, 17, 0, 0, 0, 0, time.UTC)
	for _, ok := range []string{"2025-07-17", "2025-07-17 09:00:00"} {
		got := documentDate(ok)
		if got == nil || !got.Equal(want) {
			t.Errorf("documentDate(%q) = %v, want %v", ok, got, want)
		}
	}
	for _, bad := range []string{"", "jeudi 02 octobre 2025", "17/07/2025"} {
		if got := documentDate(bad); got != nil {
			t.Errorf("documentDate(%q) = %v, want nil", bad, got)
		}
	}
}

func TestSeanceDate(t *testing.T) {
	t.Parallel()
	want := time.Date(2025, 10, 2, 0, 0, 0, 0, time.UTC)
	got := seanceDate("20251002090000000")
	if got == nil || !got.Equal(want) {
		t.Errorf("seanceDate(compact) = %v, want %v", got, want)
	}
	for _, bad := range []string{"", "jeudi 02 octobre 2025", "2025100"} {
		if got := seanceDate(bad); got != nil {
			t.Errorf("seanceDate(%q) = %v, want nil", bad, got)
		}
	}
}
