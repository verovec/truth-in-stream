package domain_test

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestComposeEvidenceIDRoundTrips(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     domain.MatchKind
		sourceID string
		chunk    int
	}{
		{name: "claim id with no chunk", kind: domain.MatchKindClaim, sourceID: "great-wall-from-space", chunk: 0},
		{name: "wiki page and chunk", kind: domain.MatchKindEvidence, sourceID: "12345", chunk: 7},
		{name: "source id containing the separator", kind: domain.MatchKindClaim, sourceID: "topic:sub:detail", chunk: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := domain.ComposeEvidenceID(tc.kind, tc.sourceID, tc.chunk)
			if id == "" {
				t.Fatal("ComposeEvidenceID returned empty id")
			}
			gotKind, gotSource, gotChunk, err := domain.ParseEvidenceID(id)
			if err != nil {
				t.Fatalf("ParseEvidenceID(%q): %v", id, err)
			}
			if gotKind != tc.kind || gotSource != tc.sourceID || gotChunk != tc.chunk {
				t.Errorf("round-trip = (%q, %q, %d), want (%q, %q, %d)", gotKind, gotSource, gotChunk, tc.kind, tc.sourceID, tc.chunk)
			}
		})
	}
}

func TestComposeEvidenceIDStable(t *testing.T) {
	t.Parallel()
	a := domain.ComposeEvidenceID(domain.MatchKindEvidence, "999", 2)
	b := domain.ComposeEvidenceID(domain.MatchKindEvidence, "999", 2)
	if a != b {
		t.Errorf("ComposeEvidenceID not stable: %q != %q", a, b)
	}
	if c := domain.ComposeEvidenceID(domain.MatchKindClaim, "999", 2); c == a {
		t.Errorf("different kinds produced the same id %q", c)
	}
}

func TestParseEvidenceIDRejectsMalformed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "missing fields", id: "claim"},
		{name: "missing chunk", id: "claim:foo"},
		{name: "unknown kind", id: "bogus:foo:0"},
		{name: "non-numeric chunk", id: "claim:foo:notanumber"},
		{name: "negative chunk", id: "claim:foo:-1"},
		{name: "empty source", id: "claim::0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, _, err := domain.ParseEvidenceID(tc.id); err == nil {
				t.Errorf("ParseEvidenceID(%q) = nil error, want error", tc.id)
			}
		})
	}
}
