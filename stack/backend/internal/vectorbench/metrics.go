package vectorbench

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// RecallAtK returns the mean fraction of exact nearest neighbors recovered by
// the approximate search, averaged over queries. exact[i] and approx[i] are
// the result id lists for query i; order within a list does not matter.
func RecallAtK(exact, approx [][]int64) (float64, error) {
	if len(exact) == 0 {
		return 0, errors.New("vectorbench: no queries to score")
	}
	if len(exact) != len(approx) {
		return 0, fmt.Errorf("vectorbench: %d exact result sets vs %d approximate", len(exact), len(approx))
	}
	var total float64
	for i := range exact {
		if len(exact[i]) == 0 {
			return 0, fmt.Errorf("vectorbench: query %d has no exact neighbors", i)
		}
		got := make(map[int64]struct{}, len(approx[i]))
		for _, id := range approx[i] {
			got[id] = struct{}{}
		}
		hits := 0
		for _, id := range exact[i] {
			if _, ok := got[id]; ok {
				hits++
			}
		}
		total += float64(hits) / float64(len(exact[i]))
	}
	return total / float64(len(exact)), nil
}

// Percentile returns the pth percentile (0-100) of samples using the
// nearest-rank method. Empty input yields zero.
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	rank = max(rank, 1)
	rank = min(rank, len(sorted))
	return sorted[rank-1]
}
