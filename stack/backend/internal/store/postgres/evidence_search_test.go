package postgres

import (
	"context"
	"strconv"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// embeddedEvidence builds a fully embedded chunk for a source so the unified
// search path can be exercised without a separate embedding step.
func embeddedEvidence(source, externalID string, v []float32) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     source,
		ExternalID: externalID,
		ChunkIndex: 0,
		Title:      source + " " + externalID,
		URL:        "https://example/" + source + "/" + externalID,
		Content:    source + "-" + externalID,
		Kind:       domain.EvidenceKindLead,
		Embedding:  v,
	}
}

func seedTwoSources(ctx context.Context, t *testing.T, store *Store) {
	t.Helper()
	// srcA occupies vector slots 0..3, srcB slots 4..7, all near-orthogonal, so
	// a query at slot 0 ranks srcA/0 first and every row is distinct.
	for i := range 4 {
		if err := store.UpsertEmbeddedChunk(ctx, embeddedEvidence("srcA", strconv.Itoa(i), unitVec(i))); err != nil {
			t.Fatalf("upsert srcA/%d: %v", i, err)
		}
		if err := store.UpsertEmbeddedChunk(ctx, embeddedEvidence("srcB", strconv.Itoa(i), unitVec(i+4))); err != nil {
			t.Fatalf("upsert srcB/%d: %v", i, err)
		}
	}
}

func TestSearchEvidenceSourceFilter(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedTwoSources(ctx, t, store)
	query := unitVec(0)

	tests := []struct {
		name    string
		sources []string
		wantLen int
		wantSrc map[string]bool // sources that may appear
	}{
		{"unfiltered returns every source", nil, 8, map[string]bool{"srcA": true, "srcB": true}},
		{"empty (non-nil) filter is the global search, not zero rows", []string{}, 8, map[string]bool{"srcA": true, "srcB": true}},
		{"scoped to one source returns only its rows", []string{"srcA"}, 4, map[string]bool{"srcA": true}},
		{"scoped to the other source", []string{"srcB"}, 4, map[string]bool{"srcB": true}},
		{"scoped to both is the same as unfiltered", []string{"srcA", "srcB"}, 8, map[string]bool{"srcA": true, "srcB": true}},
		{"scoped to an unknown source returns nothing", []string{"nope"}, 0, map[string]bool{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// topK 8 exceeds any single source's row count, so under iterative
			// scan the filtered search must still return every matching row (not
			// under-return the way a plain HNSW WHERE would).
			hits, err := store.SearchEvidence(ctx, query, 8, 0, tc.sources)
			if err != nil {
				t.Fatalf("SearchEvidence: %v", err)
			}
			if len(hits) != tc.wantLen {
				t.Fatalf("got %d hits, want %d: %+v", len(hits), tc.wantLen, hits)
			}
			for _, h := range hits {
				if !tc.wantSrc[h.Source] {
					t.Errorf("hit from unexpected source %q", h.Source)
				}
			}
		})
	}
}

func TestSearchEvidencePerQueryEfSearch(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedTwoSources(ctx, t, store)
	query := unitVec(0)

	// A per-query ef_search of 0 (session default), a low override, and a high
	// override all run through the tuned path and return the true nearest
	// neighbor (srcA/0, cosine distance 0). The override is transaction-local,
	// so it never leaks onto the pooled connection.
	for _, ef := range []int{0, 40, 400} {
		hits, err := store.SearchEvidence(ctx, query, 1, ef, nil)
		if err != nil {
			t.Fatalf("SearchEvidence ef=%d: %v", ef, err)
		}
		if len(hits) != 1 || hits[0].Source != "srcA" || hits[0].ExternalID != "0" {
			t.Fatalf("ef=%d: nearest = %+v, want srcA/0", ef, hits)
		}
	}
}

func TestSearchClaimsAndPoliticalAcceptEfSearch(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.Upsert(ctx, []domain.Claim{
		{ID: "c1", Text: "claim one", Verdict: domain.VerdictCorroborates, Embedding: unitVec(0)},
		{ID: "c2", Text: "claim two", Verdict: domain.VerdictContradicts, Embedding: unitVec(1)},
	}); err != nil {
		t.Fatalf("Upsert claims: %v", err)
	}
	if err := store.UpsertPoliticalClaim(ctx, politicalClaim("p1", "political one", unitVec(0))); err != nil {
		t.Fatalf("UpsertPoliticalClaim p1: %v", err)
	}
	if err := store.UpsertPoliticalClaim(ctx, politicalClaim("p2", "political two", unitVec(1))); err != nil {
		t.Fatalf("UpsertPoliticalClaim p2: %v", err)
	}

	// Both the default and an explicit ef_search return the nearest claim on
	// each curated store, so the tuned path (including its ef>0 transaction
	// branch) serves claims and political search too - they share the builder.
	for _, ef := range []int{0, 200} {
		got, err := store.Search(ctx, unitVec(0), 1, ef)
		if err != nil {
			t.Fatalf("Search ef=%d: %v", ef, err)
		}
		if len(got) != 1 || got[0].ID != "c1" {
			t.Fatalf("ef=%d: nearest claim = %+v, want c1", ef, got)
		}
		pol, err := store.SearchPoliticalClaims(ctx, unitVec(0), 1, ef)
		if err != nil {
			t.Fatalf("SearchPoliticalClaims ef=%d: %v", ef, err)
		}
		if len(pol) != 1 || pol[0].ID != "p1" {
			t.Fatalf("ef=%d: nearest political claim = %+v, want p1", ef, pol)
		}
	}
}
