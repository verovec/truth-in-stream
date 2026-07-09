package postgres

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// flipVec builds the ith graded unit vector: it negates the first i of the
// otherwise-uniform components. Negating i of D components moves the vector both
// i bit-flips away in binary-quantized Hamming space AND a strictly increasing
// 2i/D in cosine distance from flipVec(0), so the coarse (Hamming) and exact
// (cosine) rankings agree and every row's distance is distinct - which makes the
// two-stage vs single-stage comparison deterministic (unlike tied orthogonal
// vectors, whose equal distances tie-break nondeterministically).
func flipVec(i int) []float32 {
	v := make([]float32, domain.EmbeddingDim)
	val := float32(1 / math.Sqrt(float64(domain.EmbeddingDim)))
	for j := range v {
		if j < i {
			v[j] = -val
		} else {
			v[j] = val
		}
	}
	return v
}

// hitKeys reduces hits to their ordered (source, external_id, chunk_index)
// coordinates so two searches are compared by identity and ranking, not by the
// float distances.
func hitKeys(hits []domain.EvidenceHit) []string {
	keys := make([]string, len(hits))
	for i, h := range hits {
		keys[i] = h.Source + "/" + h.ExternalID + "#" + strconv.Itoa(h.ChunkIndex)
	}
	return keys
}

func seedForBQ(ctx context.Context, t *testing.T, store *Store, n int) {
	t.Helper()
	for i := range n {
		if err := store.UpsertEmbeddedChunk(ctx, embeddedEvidence("bq", strconv.Itoa(i), flipVec(i))); err != nil {
			t.Fatalf("upsert bq/%d: %v", i, err)
		}
	}
}

func TestSearchEvidenceBinaryQuantizedMatchesFullPrecision(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedForBQ(ctx, t, store, 20)
	query := flipVec(0)

	// The single-stage halfvec search is the ground truth.
	store.bqMultiplier = 0
	baseline, err := store.SearchEvidence(ctx, query, 5, 0, nil)
	if err != nil {
		t.Fatalf("single-stage SearchEvidence: %v", err)
	}
	if len(baseline) != 5 {
		t.Fatalf("baseline returned %d hits, want 5", len(baseline))
	}

	// With a candidate multiplier large enough to gather the true neighbors,
	// the two-stage rerank must reproduce the single-stage ranking exactly - the
	// rerank on the full-precision halfvec column is what protects final order.
	for _, mult := range []int{2, 4, 10} {
		store.bqMultiplier = mult
		twoStage, err := store.SearchEvidence(ctx, query, 5, 0, nil)
		if err != nil {
			t.Fatalf("two-stage SearchEvidence mult=%d: %v", mult, err)
		}
		if diff := cmp.Diff(hitKeys(baseline), hitKeys(twoStage)); diff != "" {
			t.Errorf("mult=%d: two-stage ranking differs from full-precision (-want +got):\n%s", mult, diff)
		}
	}
}

func TestSearchEvidenceBinaryQuantizedRespectsSourceFilter(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedTwoSources(ctx, t, store) // srcA 0..3, srcB 4..7
	query := unitVec(0)
	store.bqMultiplier = 4

	// A scoped two-stage search returns only the scoped source's rows.
	a, err := store.SearchEvidence(ctx, query, 8, 0, []string{"srcA"})
	if err != nil {
		t.Fatalf("scoped two-stage: %v", err)
	}
	if len(a) != 4 {
		t.Fatalf("scoped two-stage returned %d hits, want 4", len(a))
	}
	for _, h := range a {
		if h.Source != "srcA" {
			t.Errorf("scoped two-stage returned a %q hit", h.Source)
		}
	}
	// An unfiltered two-stage search returns every source.
	all, err := store.SearchEvidence(ctx, query, 8, 0, nil)
	if err != nil {
		t.Fatalf("unfiltered two-stage: %v", err)
	}
	if len(all) != 8 {
		t.Fatalf("unfiltered two-stage returned %d hits, want 8", len(all))
	}
}

// TestBQCoarseLimit pins the coarse-pool sizing: multiplier*topK, floored at the
// caller's efSearch so a full-recall probe keeps its budget through the lossy
// coarse stage rather than collapsing to multiplier*topK.
func TestBQCoarseLimit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                       string
		multiplier, topK, efSearch int
		want                       int
	}{
		{"multiplier times topK", 2, 5, 0, 10},
		{"larger multiplier widens the pool", 10, 5, 0, 50},
		{"efSearch floors the coverage probe pool", 4, 1, 200, 200},
		{"pool wins when it exceeds efSearch", 50, 5, 200, 250},
		{"minimum pool is topK at multiplier 1", 1, 1, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bqCoarseLimit(c.multiplier, c.topK, c.efSearch); got != c.want {
				t.Errorf("bqCoarseLimit(%d, %d, %d) = %d, want %d", c.multiplier, c.topK, c.efSearch, got, c.want)
			}
		})
	}
}

// TestBQCoarseLimitClampsOverflow verifies the int64 pool clamps to MaxInt32
// before narrowing to the int32 LIMIT, so a huge multiplier cannot wrap.
func TestBQCoarseLimitClampsOverflow(t *testing.T) {
	t.Parallel()
	if got := bqCoarseLimit(1000, 1_000_000_000, 0); got != math.MaxInt32 {
		t.Errorf("bqCoarseLimit overflow = %d, want MaxInt32 %d", got, math.MaxInt32)
	}
}

// TestBQEfSearch verifies the coarse scan's ef_search tracks the pool size but
// caps at pgvector's ef_search maximum (iterative_scan carries the rest).
func TestBQEfSearch(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ coarse, want int }{
		{50, 50},
		{hnswEfSearchMax, hnswEfSearchMax},
		{hnswEfSearchMax + 500, hnswEfSearchMax},
	} {
		if got := bqEfSearch(c.coarse); got != c.want {
			t.Errorf("bqEfSearch(%d) = %d, want %d", c.coarse, got, c.want)
		}
	}
}

// TestSearchEvidenceBinaryQuantizedFillsCoarsePoolBeyondDefaultEfSearch proves
// the coarse stage raises hnsw.ef_search to the pool size: with more rows than
// the session-default ef_search (100), a request for all of them returns all of
// them. A bare HNSW scan would cap the coarse LIMIT at ~ef_search and return
// fewer, silently shrinking the pool a large multiplier is meant to widen.
func TestSearchEvidenceBinaryQuantizedFillsCoarsePoolBeyondDefaultEfSearch(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	const n = 150 // exceeds the session-default hnsw.ef_search of 100
	seedForBQ(ctx, t, store, n)

	store.bqMultiplier = 1 // coarse_limit = 1 * topK = n
	hits, err := store.SearchEvidence(ctx, flipVec(0), n, 0, nil)
	if err != nil {
		t.Fatalf("BQ search: %v", err)
	}
	if len(hits) != n {
		t.Fatalf("BQ search returned %d hits, want %d - coarse pool capped at the default ef_search", len(hits), n)
	}
}

// TestSearchEvidenceBinaryQuantizedFullRecallProbe guards the coverage-probe
// wiring (coverage.go calls SearchEvidence with topK=1, efSearch=200 for full
// recall). Routed through the BQ path it must still return the exact nearest
// neighbor, because coarse_limit floors at efSearch (200) instead of collapsing
// to multiplier*1.
func TestSearchEvidenceBinaryQuantizedFullRecallProbe(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedForBQ(ctx, t, store, 20)
	query := flipVec(7)
	const fullRecallEf = 200 // mirrors coverage.go's coverageEfSearch

	store.bqMultiplier = 0
	baseline, err := store.SearchEvidence(ctx, query, 1, fullRecallEf, nil)
	if err != nil {
		t.Fatalf("single-stage probe: %v", err)
	}
	store.bqMultiplier = 2 // small multiplier: coarse would be 2 without the efSearch floor
	bq, err := store.SearchEvidence(ctx, query, 1, fullRecallEf, nil)
	if err != nil {
		t.Fatalf("two-stage probe: %v", err)
	}
	if diff := cmp.Diff(hitKeys(baseline), hitKeys(bq)); diff != "" {
		t.Errorf("BQ full-recall probe differs from single-stage nearest (-want +got):\n%s", diff)
	}
}

// TestWithBinaryQuantizationOption checks the opt-in wiring: the option enables
// the two-stage path only for a positive multiplier, and is off by default.
func TestWithBinaryQuantizationOption(t *testing.T) {
	t.Parallel()
	var s Store
	if s.bqMultiplier != 0 {
		t.Fatal("default store must not enable binary quantization")
	}
	WithBinaryQuantization(0)(&s)
	if s.bqMultiplier != 0 {
		t.Error("a non-positive multiplier must leave binary quantization off")
	}
	WithBinaryQuantization(-3)(&s)
	if s.bqMultiplier != 0 {
		t.Error("a negative multiplier must leave binary quantization off")
	}
	WithBinaryQuantization(8)(&s)
	if s.bqMultiplier != 8 {
		t.Errorf("bqMultiplier = %d, want 8", s.bqMultiplier)
	}
}
