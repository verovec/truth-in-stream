package evidencesrc

import (
	"testing"
	"time"
)

func TestParseDate(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	for _, ok := range []string{"2026-07-09", "2026-07-09 14:30:00", " 2026-07-09 "} {
		got := ParseDate(ok)
		if got == nil || !got.Equal(want) {
			t.Errorf("ParseDate(%q) = %v, want %v", ok, got, want)
		}
	}
	for _, bad := range []string{"", "09/07/2026", "jeudi 02 octobre 2025", "2026-13-01", "2026"} {
		if got := ParseDate(bad); got != nil {
			t.Errorf("ParseDate(%q) = %v, want nil (never guess a date)", bad, got)
		}
	}
}

func TestBuildRecordCarriesPublicationDate(t *testing.T) {
	t.Parallel()
	published := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	rec := BuildRecord("src", "ext-1", "Title", "https://x", "lead body text", &published, nil)
	if len(rec.Jobs) == 0 {
		t.Fatal("BuildRecord produced no jobs")
	}
	for _, j := range rec.Jobs {
		if j.PublishedAt == nil || !j.PublishedAt.Equal(published) {
			t.Errorf("job %d PublishedAt = %v, want %v on every chunk", j.ChunkIndex, j.PublishedAt, published)
		}
	}

	undated := BuildRecord("src", "ext-2", "Title", "https://x", "text", nil, nil)
	if undated.Jobs[0].PublishedAt != nil {
		t.Errorf("undated record job PublishedAt = %v, want nil", undated.Jobs[0].PublishedAt)
	}
}
