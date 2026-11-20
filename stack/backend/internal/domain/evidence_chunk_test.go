package domain

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestEvidenceChunkKindValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind EvidenceChunkKind
		want bool
	}{
		{EvidenceKindLead, true},
		{EvidenceKindBody, true},
		{EvidenceChunkKind("caption"), false},
		{EvidenceChunkKind(""), false},
	}
	for _, tc := range tests {
		if got := tc.kind.Valid(); got != tc.want {
			t.Errorf("EvidenceChunkKind(%q).Valid() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestWikiMetadataRoundTrip(t *testing.T) {
	t.Parallel()
	clusterID := int32(7)
	importance := 0.42
	meta := WikiMetadata{
		RevisionID: 12345,
		Section:    "History",
		ClusterID:  &clusterID,
		Importance: &importance,
	}
	m := meta.Map()
	got, err := ParseWikiMetadata(m)
	if err != nil {
		t.Fatalf("ParseWikiMetadata: %v", err)
	}
	if diff := cmp.Diff(meta, got); diff != "" {
		t.Errorf("round trip mismatch (-want +got):\n%s", diff)
	}
}

func TestWikiMetadataOmitsUnsetOptionalKeys(t *testing.T) {
	t.Parallel()
	meta := WikiMetadata{RevisionID: 99, Section: ""}
	m := meta.Map()
	if _, ok := m["cluster_id"]; ok {
		t.Error("unset cluster_id must be absent from the metadata map")
	}
	if _, ok := m["importance"]; ok {
		t.Error("unset importance must be absent from the metadata map")
	}
	if m["revision_id"] != int64(99) {
		t.Errorf("revision_id = %v, want 99", m["revision_id"])
	}
	// Section is always present (a plain string with a meaningful empty value),
	// so a reader never distinguishes absent from lead-section "".
	if _, ok := m["section"]; !ok {
		t.Error("section must be present even when empty")
	}
}

func TestParseWikiMetadataToleratesJSONNumbers(t *testing.T) {
	t.Parallel()
	// jsonb decoded by pgx yields float64 for every number, so the parser must
	// accept float64 where it stored int64/int32.
	clusterID := int32(3)
	importance := 0.9
	m := map[string]any{
		"revision_id": float64(555),
		"section":     "Intro",
		"cluster_id":  float64(3),
		"importance":  float64(0.9),
	}
	got, err := ParseWikiMetadata(m)
	if err != nil {
		t.Fatalf("ParseWikiMetadata: %v", err)
	}
	want := WikiMetadata{RevisionID: 555, Section: "Intro", ClusterID: &clusterID, Importance: &importance}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("parse mismatch (-want +got):\n%s", diff)
	}
}

func TestParseWikiMetadataEmpty(t *testing.T) {
	t.Parallel()
	// A new source that carries no wiki provenance still parses cleanly: the
	// absence of every key is the zero WikiMetadata, not an error.
	got, err := ParseWikiMetadata(map[string]any{})
	if err != nil {
		t.Fatalf("ParseWikiMetadata: %v", err)
	}
	if diff := cmp.Diff(WikiMetadata{}, got); diff != "" {
		t.Errorf("empty metadata mismatch (-want +got):\n%s", diff)
	}
}

func TestWithDuplicateFlag(t *testing.T) {
	t.Parallel()

	base := map[string]any{"revision_id": int64(42), "section": "Intro"}
	got := WithDuplicateFlag(base, 0.985)

	if got[MetaDuplicate] != true {
		t.Errorf("%s = %v, want true", MetaDuplicate, got[MetaDuplicate])
	}
	if got[MetaDuplicateSimilarity] != 0.985 {
		t.Errorf("%s = %v, want 0.985", MetaDuplicateSimilarity, got[MetaDuplicateSimilarity])
	}
	if got["revision_id"] != int64(42) || got["section"] != "Intro" {
		t.Errorf("provenance keys lost: %+v", got)
	}
	// The input map must be untouched (copy, not mutate).
	if _, ok := base[MetaDuplicate]; ok {
		t.Errorf("input map was mutated: %+v", base)
	}
}

func TestWithDuplicateFlagNilMetadata(t *testing.T) {
	t.Parallel()
	got := WithDuplicateFlag(nil, 1)
	if got == nil {
		t.Fatal("returned nil map; store jsonb marshaling must never see nil")
	}
	if got[MetaDuplicate] != true || got[MetaDuplicateSimilarity] != float64(1) {
		t.Errorf("flag not set on nil input: %+v", got)
	}
}
