package vectorbench

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSupportsIterativeScan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version string
		want    bool
	}{
		{"0.8.0", true},
		{"0.8.2", true},
		{"0.8.4", true},
		{"1.0.0", true},
		{"0.7.4", false},
		{"0.6.0", false},
	}
	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			got, err := supportsIterativeScan(tc.version)
			if err != nil {
				t.Fatalf("supportsIterativeScan(%q): %v", tc.version, err)
			}
			if got != tc.want {
				t.Errorf("supportsIterativeScan(%q) = %t, want %t", tc.version, got, tc.want)
			}
		})
	}
	if _, err := supportsIterativeScan("not-a-version"); err == nil {
		t.Error("supportsIterativeScan accepted a malformed version")
	}
}

func TestSourceForQuery(t *testing.T) {
	t.Parallel()
	if got := sourceForQuery(0, 3); got != "source_00" {
		t.Errorf("sourceForQuery(0, 3) = %q, want source_00", got)
	}
	if got := sourceForQuery(5, 3); got != "source_02" {
		t.Errorf("sourceForQuery(5, 3) = %q, want source_02", got)
	}
}

func TestClassifyFootprints(t *testing.T) {
	t.Parallel()
	relations := []relation{
		{Name: "single_vectors", Kind: "r", Bytes: 1000},
		{Name: "single_vectors_embedding_hnsw", Kind: "i", Bytes: 500},
		{Name: "single_vectors_embedding_bit_hnsw", Kind: "i", Bytes: 100},
		{Name: "part_vectors_source_00", Kind: "r", Bytes: 600},
		{Name: "part_vectors_source_01", Kind: "r", Bytes: 400},
		{Name: "part_vectors_source_00_embedding_idx", Kind: "i", Bytes: 300},
		{Name: "part_vectors_source_01_embedding_idx", Kind: "i", Bytes: 200},
	}
	got := classifyFootprints(relations)
	want := []Footprint{
		{Name: "single table", Bytes: 1000},
		{Name: "single hnsw index (halfvec)", Bytes: 500},
		{Name: "bit expression index (binary quantize)", Bytes: 100},
		{Name: "partitioned tables (total)", Bytes: 1000},
		{Name: "partitioned hnsw indexes (total)", Bytes: 500},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("classifyFootprints mismatch (-want +got):\n%s", diff)
	}
}

func TestClassifyFootprintsOmitsAbsentObjects(t *testing.T) {
	t.Parallel()
	got := classifyFootprints([]relation{{Name: "single_vectors", Kind: "r", Bytes: 42}})
	want := []Footprint{{Name: "single table", Bytes: 42}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("classifyFootprints mismatch (-want +got):\n%s", diff)
	}
}
