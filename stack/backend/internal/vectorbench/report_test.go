package vectorbench

import (
	"strings"
	"testing"
	"time"
)

func sampleResults() []Result {
	return []Result{
		{
			Cell:   Cell{Scenario: ScenarioHNSW, Filter: FilterNone, EfSearch: 40, Iterative: IterativeOff},
			Recall: 0.98,
			P50:    1200 * time.Microsecond,
			P95:    2400 * time.Microsecond,
		},
		{
			Cell:   Cell{Scenario: ScenarioBQ, Filter: FilterSource, EfSearch: 100, Iterative: IterativeRelaxed, Multiplier: 5},
			Recall: 0.95,
			P50:    800 * time.Microsecond,
			P95:    1600 * time.Microsecond,
		},
		{
			Cell:    Cell{Scenario: ScenarioHNSW, Filter: FilterNone, EfSearch: 100, Iterative: IterativeRelaxed},
			Skipped: "pgvector 0.7.4 lacks iterative scans",
		},
	}
}

func sampleFootprints() []Footprint {
	return []Footprint{
		{Name: "vectorbench.single_vectors table", Bytes: 419430400},
		{Name: "single_vectors_embedding_hnsw index", Bytes: 1572864},
	}
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()
	got := RenderMarkdown(sampleResults(), sampleFootprints())
	want := strings.Join([]string{
		"| scenario | filter | ef | iter | mult | recall@k | p50 | p95 | note |",
		"| --- | --- | --- | --- | --- | --- | --- | --- | --- |",
		"| hnsw | none | 40 | off | - | 0.980 | 1.20ms | 2.40ms | - |",
		"| bq-rerank | source | 100 | relaxed_order | 5 | 0.950 | 0.80ms | 1.60ms | - |",
		"| hnsw | none | 100 | relaxed_order | - | - | - | - | skipped: pgvector 0.7.4 lacks iterative scans |",
		"",
		"| object | size |",
		"| --- | --- |",
		"| vectorbench.single_vectors table | 400.0 MiB |",
		"| single_vectors_embedding_hnsw index | 1.5 MiB |",
		"",
	}, "\n")
	if got != want {
		t.Errorf("RenderMarkdown mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderText(t *testing.T) {
	t.Parallel()
	got := RenderText(sampleResults(), sampleFootprints())
	for _, fragment := range []string{
		"scenario", "recall@k",
		"hnsw", "bq-rerank",
		"0.980", "0.950",
		"1.20ms", "0.80ms",
		"skipped: pgvector 0.7.4 lacks iterative scans",
		"vectorbench.single_vectors table", "400.0 MiB",
	} {
		if !strings.Contains(got, fragment) {
			t.Errorf("RenderText output missing %q:\n%s", fragment, got)
		}
	}
	if lines := strings.Count(got, "\n"); lines < 7 {
		t.Errorf("RenderText produced %d lines, want at least 7", lines)
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1536, "1.5 KiB"},
		{419430400, "400.0 MiB"},
		{3221225472, "3.0 GiB"},
	}
	for _, tc := range tests {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
