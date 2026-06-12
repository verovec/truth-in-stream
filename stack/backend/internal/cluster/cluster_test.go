package cluster

import (
	"math"
	"testing"
)

// blob builds n vectors scattered slightly around center, deterministic in the
// index so tests stay reproducible without a clock or RNG.
func blob(center []float32, n int, jitter float32) [][]float32 {
	out := make([][]float32, n)
	for i := range n {
		v := make([]float32, len(center))
		for d := range center {
			// A small index-derived perturbation keeps points near the center but
			// not identical, so centroids and centrality are non-degenerate.
			v[d] = center[d] + jitter*float32((i%3)-1)*float32(d+1)
		}
		out[i] = v
	}
	return out
}

func testConfig(k int) Config {
	return Config{K: k, MaxIters: 50, Seed: 42}
}

func TestClusterSeparatesWellSeparatedBlobs(t *testing.T) {
	t.Parallel()
	vectors := make([][]float32, 0, 15)
	vectors = append(vectors, blob([]float32{10, 0, 0}, 5, 0.01)...)
	vectors = append(vectors, blob([]float32{0, 10, 0}, 5, 0.01)...)
	vectors = append(vectors, blob([]float32{0, 0, 10}, 5, 0.01)...)

	got, err := Cluster(vectors, testConfig(3))
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if len(got) != len(vectors) {
		t.Fatalf("got %d assignments, want %d", len(got), len(vectors))
	}
	// Each contiguous blob of 5 must land in a single cluster, and the three
	// blobs must occupy three distinct clusters.
	clusters := map[int32]bool{}
	for b := range 3 {
		first := got[b*5].Cluster
		for i := b * 5; i < b*5+5; i++ {
			if got[i].Cluster != first {
				t.Errorf("blob %d point %d in cluster %d, want %d (blob must be one cluster)", b, i, got[i].Cluster, first)
			}
		}
		clusters[first] = true
	}
	if len(clusters) != 3 {
		t.Errorf("blobs occupy %d clusters, want 3 distinct", len(clusters))
	}
}

func TestClusterImportanceBounded(t *testing.T) {
	t.Parallel()
	vectors := make([][]float32, 0, 12)
	vectors = append(vectors, blob([]float32{5, 1, 0}, 8, 0.2)...)
	vectors = append(vectors, blob([]float32{0, 5, 1}, 4, 0.2)...)

	got, err := Cluster(vectors, testConfig(2))
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	for i, a := range got {
		if a.Importance < 0 || a.Importance > 1 || math.IsNaN(a.Importance) {
			t.Errorf("assignment %d importance = %v, want a finite value in [0,1]", i, a.Importance)
		}
	}
}

func TestClusterDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()
	vectors := make([][]float32, 0, 18)
	vectors = append(vectors, blob([]float32{3, 0, 1}, 6, 0.3)...)
	vectors = append(vectors, blob([]float32{0, 3, 1}, 6, 0.3)...)
	vectors = append(vectors, blob([]float32{1, 1, 3}, 6, 0.3)...)

	first, err := Cluster(vectors, testConfig(3))
	if err != nil {
		t.Fatalf("Cluster (first): %v", err)
	}
	second, err := Cluster(vectors, testConfig(3))
	if err != nil {
		t.Fatalf("Cluster (second): %v", err)
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic at %d: %+v vs %+v (a re-run must reproduce the same clustering)", i, first[i], second[i])
		}
	}
}

func TestClusterProminenceFavorsLargerCluster(t *testing.T) {
	t.Parallel()
	// A large topic cluster and a small one, both tight. Members of the larger
	// cluster should carry higher importance, since prominence scales with size
	// and centrality is comparable for two tight blobs.
	vectors := make([][]float32, 0, 15)
	vectors = append(vectors, blob([]float32{8, 0, 0}, 12, 0.02)...) // large
	vectors = append(vectors, blob([]float32{0, 8, 0}, 3, 0.02)...)  // small

	got, err := Cluster(vectors, testConfig(2))
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	largeAvg := avgImportance(got[:12])
	smallAvg := avgImportance(got[12:])
	if largeAvg <= smallAvg {
		t.Errorf("large-cluster avg importance %.4f, small-cluster avg %.4f; larger cluster must score higher", largeAvg, smallAvg)
	}
}

func TestClusterCentralityFavorsCentralPoints(t *testing.T) {
	t.Parallel()
	// One cluster (K=1): a tight core plus one far outlier. The central points
	// must out-score the outlier on centrality (hence importance, prominence=1).
	vectors := blob([]float32{6, 0, 0}, 9, 0.01)
	outlier := []float32{6, 4, 0} // pulled well off the core direction
	vectors = append(vectors, outlier)

	got, err := Cluster(vectors, testConfig(1))
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	outlierImp := got[len(got)-1].Importance
	coreAvg := avgImportance(got[:9])
	if outlierImp >= coreAvg {
		t.Errorf("outlier importance %.4f >= core avg %.4f; a less central member must score lower", outlierImp, coreAvg)
	}
}

func TestClusterKEqualsOneScoresByCentralityOnly(t *testing.T) {
	t.Parallel()
	vectors := blob([]float32{4, 2, 1}, 7, 0.1)
	got, err := Cluster(vectors, testConfig(1))
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	for i, a := range got {
		if a.Cluster != 0 {
			t.Errorf("assignment %d cluster = %d, want 0 (K=1)", i, a.Cluster)
		}
	}
}

func TestClusterKClampedToCount(t *testing.T) {
	t.Parallel()
	vectors := [][]float32{{1, 0}, {0, 1}, {1, 1}}
	got, err := Cluster(vectors, Config{K: 10, MaxIters: 20, Seed: 7})
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d assignments, want 3", len(got))
	}
	for i, a := range got {
		if a.Importance < 0 || a.Importance > 1 {
			t.Errorf("assignment %d importance %v out of [0,1]", i, a.Importance)
		}
	}
}

func TestClusterEmptyInput(t *testing.T) {
	t.Parallel()
	got, err := Cluster(nil, testConfig(3))
	if err != nil {
		t.Fatalf("Cluster(nil): %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for empty input", got)
	}
}

func TestClusterRejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		vectors [][]float32
	}{
		{"mismatched dimensions", [][]float32{{1, 0}, {1, 0, 0}}},
		{"zero vector cannot be normalized", [][]float32{{1, 0}, {0, 0}}},
		{"empty vector", [][]float32{{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Cluster(tc.vectors, testConfig(2)); err == nil {
				t.Errorf("Cluster(%v) = nil error, want an error", tc.vectors)
			}
		})
	}
}

func avgImportance(as []Assignment) float64 {
	if len(as) == 0 {
		return 0
	}
	var sum float64
	for _, a := range as {
		sum += a.Importance
	}
	return sum / float64(len(as))
}
