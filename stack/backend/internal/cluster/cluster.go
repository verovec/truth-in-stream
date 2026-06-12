// Package cluster groups embedding vectors into topic clusters and scores each
// vector's importance, so an offline batch job can tell the ingestion producer
// which chunks to embed first. It is pure and transport-free: it takes vectors
// and returns assignments, with no database or model dependency, so the same
// logic drives the command and its unit tests.
//
// The method is spherical k-means - Lloyd's algorithm over L2-normalized
// vectors, so the squared-Euclidean assignment it minimizes is equivalent to
// cosine distance, the metric the corpus is indexed under (halfvec_cosine_ops).
// It is the simplest clustering that works directly on the existing embeddings;
// it needs no external model and no link/category graph the corpus does not
// store. Seeding is deterministic (k-means++ over a seeded PRNG), so a re-run
// reproduces the same clustering - the idempotency the batch job needs.
package cluster

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
)

// Assignment is one input vector's clustering result: the cluster it joined and
// a bounded importance in [0,1] for embedding-priority ordering.
type Assignment struct {
	Cluster    int32
	Importance float64
}

// Config tunes a clustering run. K is the number of clusters, clamped to
// [1, len(vectors)]. MaxIters caps Lloyd's iterations (it usually converges
// sooner). Seed makes the k-means++ initialization deterministic, so re-running
// the job over an unchanged corpus reproduces the same assignments.
type Config struct {
	K        int
	MaxIters int
	Seed     uint64
}

// Cluster groups the vectors with spherical k-means and scores each by
// importance = clusterProminence * memberCentrality, both in [0,1]:
// prominence is the cluster's size over the largest cluster's size (a common
// topic outranks a niche one), and centrality is how close the member sits to
// its cluster's centroid (a representative chunk outranks an outlier). The two
// together mean "embed the central members of the biggest topics first". It
// returns one Assignment per input vector in input order. Empty input returns
// nil. Vectors must be non-empty, equal-length, and non-zero (an embedding is
// never the zero vector); anything else is a caller error.
func Cluster(vectors [][]float32, cfg Config) ([]Assignment, error) {
	if len(vectors) == 0 {
		return nil, nil
	}
	units, err := normalize(vectors)
	if err != nil {
		return nil, err
	}

	k := cfg.K
	if k < 1 {
		k = 1
	}
	if k > len(units) {
		k = len(units)
	}
	maxIters := cfg.MaxIters
	if maxIters < 1 {
		maxIters = 1
	}

	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15))
	centroids := kmeansPlusPlusInit(units, k, rng)
	assign := make([]int32, len(units))
	for range maxIters {
		if changed := assignToNearest(units, centroids, assign); !changed {
			break
		}
		recomputeCentroids(units, assign, centroids)
	}
	// recomputeCentroids moves the centroids after the loop's last assignment, so
	// when the run exhausts maxIters without converging, assign still reflects the
	// pre-move centroids. Re-assign once against the final centroids so the
	// centrality score measures each member against the centroid it actually
	// belongs to (a no-op when the loop already converged).
	assignToNearest(units, centroids, assign)

	return score(units, centroids, assign), nil
}

// normalize copies each vector to a unit-L2 float64 vector, the representation
// spherical k-means works in. It rejects a zero, empty, or mismatched-length
// vector, since none can be normalized or compared.
func normalize(vectors [][]float32) ([][]float64, error) {
	dim := len(vectors[0])
	if dim == 0 {
		return nil, errors.New("cluster: vectors must be non-empty")
	}
	units := make([][]float64, len(vectors))
	for i, v := range vectors {
		if len(v) != dim {
			return nil, fmt.Errorf("cluster: vector %d has dimension %d, want %d", i, len(v), dim)
		}
		u := make([]float64, dim)
		var norm float64
		for d, x := range v {
			u[d] = float64(x)
			norm += float64(x) * float64(x)
		}
		norm = math.Sqrt(norm)
		if norm == 0 {
			return nil, fmt.Errorf("cluster: vector %d is the zero vector and cannot be normalized", i)
		}
		for d := range u {
			u[d] /= norm
		}
		units[i] = u
	}
	return units, nil
}

// kmeansPlusPlusInit picks k initial centroids with k-means++: the first
// uniformly at random, each next with probability proportional to its squared
// distance to the nearest chosen centroid. For unit vectors squared Euclidean
// distance is 2(1-cosine), so a far-apart spread is chosen, which converges
// faster and avoids the empty-cluster degeneracy of random seeding.
func kmeansPlusPlusInit(units [][]float64, k int, rng *rand.Rand) [][]float64 {
	dim := len(units[0])
	centroids := make([][]float64, 0, k)
	first := rng.IntN(len(units))
	centroids = append(centroids, clone(units[first]))

	dist2 := make([]float64, len(units))
	for i := range dist2 {
		dist2[i] = squaredDist(units[i], centroids[0])
	}
	for len(centroids) < k {
		var total float64
		for _, d := range dist2 {
			total += d
		}
		next := len(units) - 1
		if total > 0 {
			target := rng.Float64() * total
			var acc float64
			for i, d := range dist2 {
				acc += d
				if acc >= target {
					next = i
					break
				}
			}
		} else {
			// Every remaining point coincides with a centroid; pick deterministically.
			next = rng.IntN(len(units))
		}
		centroids = append(centroids, clone(units[next]))
		newC := centroids[len(centroids)-1]
		for i := range units {
			if d := squaredDist(units[i], newC); d < dist2[i] {
				dist2[i] = d
			}
		}
	}
	// Pad defensively if duplicates collapsed the count; never happens for
	// distinct points but keeps the centroid count exactly k.
	for len(centroids) < k {
		centroids = append(centroids, make([]float64, dim))
		copy(centroids[len(centroids)-1], units[0])
	}
	return centroids
}

// assignToNearest assigns each unit vector to the centroid it has the highest
// cosine with (equivalently the nearest on the unit sphere), breaking ties by
// the lower centroid index so the result is deterministic. It reports whether
// any assignment changed, the convergence signal for the Lloyd loop.
func assignToNearest(units, centroids [][]float64, assign []int32) bool {
	changed := false
	for i, u := range units {
		best := int32(0)
		bestDot := math.Inf(-1)
		for c, centroid := range centroids {
			if d := dot(u, centroid); d > bestDot {
				bestDot = d
				best = int32(c)
			}
		}
		if assign[i] != best {
			assign[i] = best
			changed = true
		}
	}
	return changed
}

// recomputeCentroids sets each centroid to the normalized mean of its members.
// A centroid that drew no members is re-seeded to the worst-served point (the
// one least similar to its own centroid), the standard deterministic fix that
// keeps k non-empty clusters without a random restart.
func recomputeCentroids(units [][]float64, assign []int32, centroids [][]float64) {
	dim := len(units[0])
	sums := make([][]float64, len(centroids))
	counts := make([]int, len(centroids))
	for c := range sums {
		sums[c] = make([]float64, dim)
	}
	for i, u := range units {
		c := assign[i]
		counts[c]++
		for d := range u {
			sums[c][d] += u[d]
		}
	}
	for c := range centroids {
		if counts[c] == 0 {
			reseedEmptyCentroid(units, centroids, assign, c)
			continue
		}
		normalizeInto(sums[c], centroids[c])
	}
}

// reseedEmptyCentroid moves an empty centroid onto the point currently least
// similar to its assigned centroid, so the next assignment pass can split a
// crowded cluster rather than leave c empty. Deterministic: lowest index wins a
// tie.
func reseedEmptyCentroid(units, centroids [][]float64, assign []int32, c int) {
	worst := 0
	worstDot := math.Inf(1)
	for i, u := range units {
		if d := dot(u, centroids[assign[i]]); d < worstDot {
			worstDot = d
			worst = i
		}
	}
	copy(centroids[c], units[worst])
}

// score turns the final assignment into per-vector importance. prominence is the
// member's cluster size over the largest cluster size; centrality maps the
// member's cosine to its centroid from [-1,1] onto [0,1]; importance is their
// product, clamped to [0,1] against floating-point drift.
func score(units, centroids [][]float64, assign []int32) []Assignment {
	sizes := make([]int, len(centroids))
	for _, c := range assign {
		sizes[c]++
	}
	maxSize := 0
	for _, s := range sizes {
		maxSize = max(maxSize, s)
	}

	out := make([]Assignment, len(units))
	for i, u := range units {
		c := assign[i]
		prominence := float64(sizes[c]) / float64(maxSize)
		centrality := (dot(u, centroids[c]) + 1) / 2
		importance := clamp01(prominence * clamp01(centrality))
		out[i] = Assignment{Cluster: c, Importance: importance}
	}
	return out
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func squaredDist(a, b []float64) float64 {
	var s float64
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return s
}

// normalizeInto writes the unit vector of sum into dst; a zero sum (members
// canceling out exactly) leaves the prior centroid in place rather than
// producing NaNs.
func normalizeInto(sum, dst []float64) {
	var norm float64
	for _, x := range sum {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return
	}
	for d := range sum {
		dst[d] = sum[d] / norm
	}
}

func clone(v []float64) []float64 {
	c := make([]float64, len(v))
	copy(c, v)
	return c
}

func clamp01(x float64) float64 {
	return min(1, max(0, x))
}
