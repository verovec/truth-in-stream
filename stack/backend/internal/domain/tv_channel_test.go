package domain

import "testing"

func TestTVSourceKindValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind TVSourceKind
		want bool
	}{
		{TVSourceYouTube, true},
		{TVSourceHLS, true},
		{"", false},
		{"widevine", false},
		{"YouTube", false},
	}
	for _, tc := range tests {
		if got := tc.kind.Valid(); got != tc.want {
			t.Errorf("TVSourceKind(%q).Valid() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestVideoKindTVIsValid guards that the archive kind the TV epic adds is a
// recognized video kind, so a captured recording can be stored like any video.
func TestVideoKindTVIsValid(t *testing.T) {
	t.Parallel()
	if !VideoKindTV.Valid() {
		t.Fatalf("VideoKindTV should be a valid video kind")
	}
	if VideoKindTV != "tv" {
		t.Fatalf("VideoKindTV = %q, want \"tv\"", VideoKindTV)
	}
}
