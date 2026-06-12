package domain

import "testing"

func TestWikiChunkKindValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind WikiChunkKind
		want bool
	}{
		{WikiChunkKindLead, true},
		{WikiChunkKindBody, true},
		{"", false},
		{"Lead", false},
		{"summary", false},
	}
	for _, tc := range tests {
		if got := tc.kind.Valid(); got != tc.want {
			t.Errorf("WikiChunkKind(%q).Valid() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}
