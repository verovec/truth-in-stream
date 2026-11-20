package postgres

import (
	"math"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestReciprocalRankFusion(t *testing.T) {
	const k = 60
	tests := []struct {
		name  string
		lists [][]string
		want  map[string]float64
	}{
		{
			name:  "empty lists score nothing",
			lists: nil,
			want:  map[string]float64{},
		},
		{
			name:  "single list is one over k plus one-based rank",
			lists: [][]string{{"a", "b", "c"}},
			want: map[string]float64{
				"a": 1.0 / (k + 1),
				"b": 1.0 / (k + 2),
				"c": 1.0 / (k + 3),
			},
		},
		{
			name:  "a key in both lists sums its two contributions",
			lists: [][]string{{"a", "b"}, {"b", "a"}},
			want: map[string]float64{
				"a": 1.0/(k+1) + 1.0/(k+2),
				"b": 1.0/(k+2) + 1.0/(k+1),
			},
		},
		{
			name:  "a key present in one list only counts once",
			lists: [][]string{{"x"}, {"y"}},
			want: map[string]float64{
				"x": 1.0 / (k + 1),
				"y": 1.0 / (k + 1),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reciprocalRankFusion(tc.lists, k)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d keys, want %d (%v)", len(got), len(tc.want), got)
			}
			for key, want := range tc.want {
				if math.Abs(got[key]-want) > 1e-12 {
					t.Errorf("key %q: got %v, want %v", key, got[key], want)
				}
			}
		})
	}
}

// TestReciprocalRankFusionKFlattens shows the smoothing constant only re-weights
// the top rank's dominance: a larger k narrows the gap between rank 1 and rank 2.
func TestReciprocalRankFusionKFlattens(t *testing.T) {
	list := [][]string{{"a", "b"}}
	small := reciprocalRankFusion(list, 1)
	large := reciprocalRankFusion(list, 1000)
	if small["a"]-small["b"] <= large["a"]-large["b"] {
		t.Fatalf("expected a larger k to flatten the rank-1/rank-2 gap: small=%v large=%v", small, large)
	}
}

// claim builds a claim match with just the id and cosine distance the fusion
// reasons over.
func claim(id string, dist float32) domain.ClaimMatch {
	return domain.ClaimMatch{ID: id, Distance: dist}
}

func ids(ms []domain.ClaimMatch) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func fuseClaims(vector, lexical []domain.ClaimMatch, topK int) []domain.ClaimMatch {
	return fuseHybrid(vector, lexical,
		func(m domain.ClaimMatch) string { return m.ID },
		func(m domain.ClaimMatch) float32 { return m.Distance },
		topK, DefaultRRFConstant)
}

func TestFuseHybrid(t *testing.T) {
	tests := []struct {
		name    string
		vector  []domain.ClaimMatch
		lexical []domain.ClaimMatch
		topK    int
		want    []string // expected ids, in returned (distance-ascending) order
	}{
		{
			name:    "vector only degrades to the vector ranking, nearest first",
			vector:  []domain.ClaimMatch{claim("a", 0.1), claim("b", 0.2), claim("c", 0.3)},
			lexical: nil,
			topK:    3,
			want:    []string{"a", "b", "c"},
		},
		{
			name:    "lexical-only exact match is rescued into the topK",
			vector:  []domain.ClaimMatch{claim("a", 0.10), claim("b", 0.12), claim("c", 0.14)},
			lexical: []domain.ClaimMatch{claim("z", 0.40)},
			topK:    3,
			// z is rank-1 lexical, so its RRF beats the third vector-only hit c;
			// z is furthest by cosine so it is returned last (nearest-first).
			want: []string{"a", "b", "z"},
		},
		{
			name:    "a hit in both branches outranks single-branch hits",
			vector:  []domain.ClaimMatch{claim("a", 0.30), claim("shared", 0.31), claim("c", 0.32)},
			lexical: []domain.ClaimMatch{claim("shared", 0.31), claim("d", 0.50)},
			topK:    2,
			// shared is in both lists (rank 2 + rank 1) so it wins; a is vector
			// rank 1. Both survive; returned nearest-first: a (0.30) then shared.
			want: []string{"a", "shared"},
		},
		{
			name:    "topK truncates the fused ranking",
			vector:  []domain.ClaimMatch{claim("a", 0.1), claim("b", 0.2), claim("c", 0.3), claim("d", 0.4)},
			lexical: nil,
			topK:    2,
			want:    []string{"a", "b"},
		},
		{
			name:    "the same chunk from both branches is returned once",
			vector:  []domain.ClaimMatch{claim("a", 0.2)},
			lexical: []domain.ClaimMatch{claim("a", 0.2)},
			topK:    5,
			want:    []string{"a"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(fuseClaims(tc.vector, tc.lexical, tc.topK))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("position %d: got %v, want %v", i, got, tc.want)
				}
			}
		})
	}
}

// TestFuseHybridReturnsNearestFirst asserts the invariant every caller relies on:
// whatever RRF selects, the result is ordered by ascending cosine distance so the
// break-on-threshold match loops stay correct.
func TestFuseHybridReturnsNearestFirst(t *testing.T) {
	vector := []domain.ClaimMatch{claim("far", 0.9), claim("near", 0.1)}
	lexical := []domain.ClaimMatch{claim("mid", 0.5)}
	got := fuseClaims(vector, lexical, 3)
	for i := 1; i < len(got); i++ {
		if got[i-1].Distance > got[i].Distance {
			t.Fatalf("result not nearest-first: %v", ids(got))
		}
	}
}

// TestFuseHybridDeterministic guards against map-iteration order leaking into the
// selection: equal fused scores must break ties deterministically.
func TestFuseHybridDeterministic(t *testing.T) {
	vector := []domain.ClaimMatch{claim("a", 0.2), claim("b", 0.2), claim("c", 0.2)}
	lexical := []domain.ClaimMatch{claim("c", 0.2), claim("b", 0.2), claim("a", 0.2)}
	first := ids(fuseClaims(vector, lexical, 2))
	for range 50 {
		got := ids(fuseClaims(vector, lexical, 2))
		if len(got) != len(first) {
			t.Fatalf("nondeterministic length: %v vs %v", got, first)
		}
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("nondeterministic selection: %v vs %v", got, first)
			}
		}
	}
}
