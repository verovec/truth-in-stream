// Package vectorbench measures pgvector index, quantization, and partitioning
// strategies against a deterministic synthetic corpus, so the datastore-scale
// decisions (index type, partition choice, instance sizing) are made from
// numbers rather than guesses. It is offline tooling: it owns its own schema
// and never touches application tables.
package vectorbench

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
)

// CorpusConfig controls the deterministic synthetic corpus. Vectors are drawn
// from a seeded Gaussian mixture and unit-normalized, which approximates the
// clustered geometry of real text embeddings far better than uniform noise
// (uniform high-dimensional vectors are near-orthogonal and make ANN look
// artificially easy).
type CorpusConfig struct {
	Rows     int
	Queries  int
	Dims     int
	Sources  int
	Clusters int
	Seed     uint64
}

func (c CorpusConfig) validate() error {
	switch {
	case c.Rows <= 0:
		return errors.New("vectorbench: rows must be positive")
	case c.Queries <= 0:
		return errors.New("vectorbench: queries must be positive")
	case c.Dims <= 0:
		return errors.New("vectorbench: dims must be positive")
	case c.Sources <= 0:
		return errors.New("vectorbench: sources must be positive")
	case c.Clusters <= 0:
		return errors.New("vectorbench: clusters must be positive")
	case c.Sources > c.Rows:
		return fmt.Errorf("vectorbench: %d sources cannot cover %d rows", c.Sources, c.Rows)
	}
	return nil
}

// Corpus is the generated dataset: row vectors with their source labels, plus
// query vectors drawn from the same cluster distribution so every query has
// meaningful near neighbors.
type Corpus struct {
	Vectors [][]float32
	Sources []string
	Queries [][]float32
}

// SourceLabel names the ith synthetic source. Labels are identifier-safe so
// they double as partition-table suffixes.
func SourceLabel(i int) string {
	return fmt.Sprintf("source_%02d", i)
}

// GenerateCorpus builds the corpus for cfg. The same config always yields the
// same corpus, so benchmark runs are comparable across changes. Source labels
// follow a harmonic skew (source_00 largest), mirroring real corpora where one
// source dominates.
func GenerateCorpus(cfg CorpusConfig) (Corpus, error) {
	if err := cfg.validate(); err != nil {
		return Corpus{}, err
	}
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed<<1|1))
	centers := make([][]float32, cfg.Clusters)
	for i := range centers {
		centers[i] = randomUnitVector(rng, cfg.Dims)
	}
	sigma := 0.6 / math.Sqrt(float64(cfg.Dims))
	c := Corpus{
		Vectors: make([][]float32, cfg.Rows),
		Sources: make([]string, cfg.Rows),
		Queries: make([][]float32, cfg.Queries),
	}
	weights := harmonicWeights(cfg.Sources)
	for i := range c.Vectors {
		c.Vectors[i] = clusterMember(rng, centers, sigma)
		c.Sources[i] = SourceLabel(pickWeighted(rng, weights))
	}
	for i := range c.Queries {
		c.Queries[i] = clusterMember(rng, centers, sigma)
	}
	return c, nil
}

// clusterMember samples a unit vector near a randomly chosen cluster center.
func clusterMember(rng *rand.Rand, centers [][]float32, sigma float64) []float32 {
	center := centers[rng.IntN(len(centers))]
	v := make([]float32, len(center))
	for d := range v {
		v[d] = center[d] + float32(rng.NormFloat64()*sigma)
	}
	normalize(v)
	return v
}

func randomUnitVector(rng *rand.Rand, dims int) []float32 {
	v := make([]float32, dims)
	for d := range v {
		v[d] = float32(rng.NormFloat64())
	}
	normalize(v)
	return v
}

func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		v[0] = 1
		return
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
}

// harmonicWeights returns cumulative weights proportional to 1/(i+1), the
// skew used to spread rows across sources.
func harmonicWeights(n int) []float64 {
	cum := make([]float64, n)
	var total float64
	for i := range cum {
		total += 1 / float64(i+1)
		cum[i] = total
	}
	for i := range cum {
		cum[i] /= total
	}
	return cum
}

func pickWeighted(rng *rand.Rand, cumulative []float64) int {
	r := rng.Float64()
	for i, c := range cumulative {
		if r <= c {
			return i
		}
	}
	return len(cumulative) - 1
}
