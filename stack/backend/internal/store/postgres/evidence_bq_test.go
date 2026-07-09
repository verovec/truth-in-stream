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
