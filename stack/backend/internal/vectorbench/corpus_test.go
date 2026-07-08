package vectorbench

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func validCorpusConfig() CorpusConfig {
	return CorpusConfig{
		Rows:     200,
		Queries:  10,
		Dims:     16,
		Sources:  4,
		Clusters: 8,
		Seed:     42,
	}
}

func TestGenerateCorpusDeterministic(t *testing.T) {
	t.Parallel()
	cfg := validCorpusConfig()
	a, err := GenerateCorpus(cfg)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	b, err := GenerateCorpus(cfg)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	if diff := cmp.Diff(a, b); diff != "" {
		t.Errorf("same seed produced different corpora (-first +second):\n%s", diff)
	}
}

func TestGenerateCorpusSeedChangesVectors(t *testing.T) {
	t.Parallel()
	cfg := validCorpusConfig()
	a, err := GenerateCorpus(cfg)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	cfg.Seed = 43
	b, err := GenerateCorpus(cfg)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	if cmp.Equal(a.Vectors[0], b.Vectors[0]) {
		t.Error("different seeds produced an identical first vector")
	}
}

func TestGenerateCorpusShape(t *testing.T) {
	t.Parallel()
	cfg := validCorpusConfig()
	c, err := GenerateCorpus(cfg)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	if len(c.Vectors) != cfg.Rows {
		t.Errorf("len(Vectors) = %d, want %d", len(c.Vectors), cfg.Rows)
	}
	if len(c.Sources) != cfg.Rows {
		t.Errorf("len(Sources) = %d, want %d", len(c.Sources), cfg.Rows)
	}
	if len(c.Queries) != cfg.Queries {
		t.Errorf("len(Queries) = %d, want %d", len(c.Queries), cfg.Queries)
	}
	for i, v := range c.Vectors {
		if len(v) != cfg.Dims {
			t.Fatalf("Vectors[%d] has %d dims, want %d", i, len(v), cfg.Dims)
		}
	}
	for i, q := range c.Queries {
		if len(q) != cfg.Dims {
			t.Fatalf("Queries[%d] has %d dims, want %d", i, len(q), cfg.Dims)
		}
	}
}

func TestGenerateCorpusUnitNorm(t *testing.T) {
	t.Parallel()
	c, err := GenerateCorpus(validCorpusConfig())
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	for i, v := range c.Vectors {
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if norm := math.Sqrt(sum); math.Abs(norm-1) > 1e-3 {
			t.Fatalf("Vectors[%d] norm = %f, want 1", i, norm)
		}
	}
}

func TestGenerateCorpusSourcesSkewed(t *testing.T) {
	t.Parallel()
	cfg := validCorpusConfig()
	cfg.Rows = 2000
	c, err := GenerateCorpus(cfg)
	if err != nil {
		t.Fatalf("GenerateCorpus: %v", err)
	}
	counts := map[string]int{}
	for _, s := range c.Sources {
		counts[s]++
	}
	if len(counts) != cfg.Sources {
		t.Fatalf("got %d distinct sources, want %d", len(counts), cfg.Sources)
	}
	first := SourceLabel(0)
	last := SourceLabel(cfg.Sources - 1)
	if counts[first] <= counts[last] {
		t.Errorf("source distribution not skewed: %s=%d, %s=%d", first, counts[first], last, counts[last])
	}
}

func TestSourceLabel(t *testing.T) {
	t.Parallel()
	if got := SourceLabel(0); got != "source_00" {
		t.Errorf("SourceLabel(0) = %q, want source_00", got)
	}
	if got := SourceLabel(11); got != "source_11" {
		t.Errorf("SourceLabel(11) = %q, want source_11", got)
	}
}

func TestGenerateCorpusRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*CorpusConfig)
	}{
		{"zero rows", func(c *CorpusConfig) { c.Rows = 0 }},
		{"zero queries", func(c *CorpusConfig) { c.Queries = 0 }},
		{"zero dims", func(c *CorpusConfig) { c.Dims = 0 }},
		{"zero sources", func(c *CorpusConfig) { c.Sources = 0 }},
		{"zero clusters", func(c *CorpusConfig) { c.Clusters = 0 }},
		{"more sources than rows", func(c *CorpusConfig) { c.Sources = c.Rows + 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validCorpusConfig()
			tc.mutate(&cfg)
			if _, err := GenerateCorpus(cfg); err == nil {
				t.Error("GenerateCorpus accepted an invalid config")
			}
		})
	}
}
