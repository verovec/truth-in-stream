package vectorbench

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSetupDDL(t *testing.T) {
	t.Parallel()
	got, err := SetupDDL(16, []string{"source_00", "source_01"})
	if err != nil {
		t.Fatalf("SetupDDL: %v", err)
	}
	want := []string{
		"DROP SCHEMA IF EXISTS vectorbench CASCADE",
		"CREATE SCHEMA vectorbench",
		"CREATE TABLE vectorbench.single_vectors (id bigint NOT NULL, source text NOT NULL, embedding halfvec(16) NOT NULL)",
		"CREATE TABLE vectorbench.part_vectors (id bigint NOT NULL, source text NOT NULL, embedding halfvec(16) NOT NULL) PARTITION BY LIST (source)",
		"CREATE TABLE vectorbench.part_vectors_source_00 PARTITION OF vectorbench.part_vectors FOR VALUES IN ('source_00')",
		"CREATE TABLE vectorbench.part_vectors_source_01 PARTITION OF vectorbench.part_vectors FOR VALUES IN ('source_01')",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SetupDDL mismatch (-want +got):\n%s", diff)
	}
}

func TestSetupDDLRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dims    int
		sources []string
	}{
		{"zero dims", 0, []string{"source_00"}},
		{"no sources", 16, nil},
		{"unsafe source label", 16, []string{"source'; DROP TABLE x; --"}},
		{"uppercase source label", 16, []string{"Source_00"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := SetupDDL(tc.dims, tc.sources); err == nil {
				t.Error("SetupDDL accepted invalid input")
			}
		})
	}
}

func TestIndexDDL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		scenario Scenario
		want     []string
	}{
		{
			name:     "hnsw single index matches production parameters",
			scenario: ScenarioHNSW,
			want: []string{
				"CREATE INDEX single_vectors_embedding_hnsw ON vectorbench.single_vectors USING hnsw (embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 200)",
			},
		},
		{
			name:     "bq adds the bit expression index",
			scenario: ScenarioBQ,
			want: []string{
				"CREATE INDEX single_vectors_embedding_bit_hnsw ON vectorbench.single_vectors USING hnsw ((binary_quantize(embedding)::bit(16)) bit_hamming_ops) WITH (m = 16, ef_construction = 200)",
			},
		},
		{
			name:     "partitioned parent index propagates per partition",
			scenario: ScenarioPartitioned,
			want: []string{
				"CREATE INDEX part_vectors_embedding_hnsw ON vectorbench.part_vectors USING hnsw (embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 200)",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := IndexDDL(tc.scenario, 16)
			if err != nil {
				t.Fatalf("IndexDDL: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("IndexDDL mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if _, err := IndexDDL(Scenario("ivfflat"), 16); err == nil {
		t.Error("IndexDDL accepted an unknown scenario")
	}
	if _, err := IndexDDL(ScenarioHNSW, 0); err == nil {
		t.Error("IndexDDL accepted zero dims")
	}
}

func TestSearchSQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		scenario Scenario
		filter   Filter
		want     string
	}{
		{
			name:     "hnsw unfiltered",
			scenario: ScenarioHNSW,
			filter:   FilterNone,
			want:     "SELECT id FROM vectorbench.single_vectors ORDER BY embedding <=> $1::halfvec(16) LIMIT $2",
		},
		{
			name:     "hnsw filtered by source",
			scenario: ScenarioHNSW,
			filter:   FilterSource,
			want:     "SELECT id FROM vectorbench.single_vectors WHERE source = $3 ORDER BY embedding <=> $1::halfvec(16) LIMIT $2",
		},
		{
			name:     "partitioned unfiltered",
			scenario: ScenarioPartitioned,
			filter:   FilterNone,
			want:     "SELECT id FROM vectorbench.part_vectors ORDER BY embedding <=> $1::halfvec(16) LIMIT $2",
		},
		{
			name:     "partitioned filtered prunes by source",
			scenario: ScenarioPartitioned,
			filter:   FilterSource,
			want:     "SELECT id FROM vectorbench.part_vectors WHERE source = $3 ORDER BY embedding <=> $1::halfvec(16) LIMIT $2",
		},
		{
			name:     "bq coarse then rerank unfiltered",
			scenario: ScenarioBQ,
			filter:   FilterNone,
			want:     "WITH candidates AS MATERIALIZED (SELECT id, embedding FROM vectorbench.single_vectors ORDER BY binary_quantize(embedding)::bit(16) <~> binary_quantize($1::halfvec(16)) LIMIT $3) SELECT id FROM candidates ORDER BY embedding <=> $1::halfvec(16) LIMIT $2",
		},
		{
			name:     "bq coarse then rerank filtered by source",
			scenario: ScenarioBQ,
			filter:   FilterSource,
			want:     "WITH candidates AS MATERIALIZED (SELECT id, embedding FROM vectorbench.single_vectors WHERE source = $4 ORDER BY binary_quantize(embedding)::bit(16) <~> binary_quantize($1::halfvec(16)) LIMIT $3) SELECT id FROM candidates ORDER BY embedding <=> $1::halfvec(16) LIMIT $2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := SearchSQL(tc.scenario, tc.filter, 16)
			if err != nil {
				t.Fatalf("SearchSQL: %v", err)
			}
			if got != tc.want {
				t.Errorf("SearchSQL = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := SearchSQL(Scenario("ivfflat"), FilterNone, 16); err == nil {
		t.Error("SearchSQL accepted an unknown scenario")
	}
}

func TestExactSQLMatchesSingleTableScan(t *testing.T) {
	t.Parallel()
	got, err := ExactSQL(FilterSource, 16)
	if err != nil {
		t.Fatalf("ExactSQL: %v", err)
	}
	want, err := SearchSQL(ScenarioHNSW, FilterSource, 16)
	if err != nil {
		t.Fatalf("SearchSQL: %v", err)
	}
	if got != want {
		t.Errorf("ExactSQL = %q, want the single-table scan %q", got, want)
	}
}

func TestVectorLiteral(t *testing.T) {
	t.Parallel()
	got := VectorLiteral([]float32{1, 2.5, -0.25})
	if got != "[1,2.5,-0.25]" {
		t.Errorf("VectorLiteral = %q, want [1,2.5,-0.25]", got)
	}
	if strings.Contains(VectorLiteral([]float32{0.1}), " ") {
		t.Error("VectorLiteral must not contain spaces")
	}
}
