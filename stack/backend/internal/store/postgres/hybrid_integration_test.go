package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// figureChunk is an embedded evidence chunk with caller-set content, so a test
// can plant an exact figure a lexical query keys on while a distant embedding
// keeps cosine from ranking it near the top.
func figureChunk(source, externalID, title, content string, v []float32) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     source,
		ExternalID: externalID,
		ChunkIndex: 0,
		Title:      title,
		URL:        "https://example/" + source + "/" + externalID,
		Content:    content,
		Kind:       domain.EvidenceKindLead,
		Embedding:  v,
	}
}

func hasHit(hits []domain.EvidenceHit, externalID string) bool {
	for _, h := range hits {
		if h.ExternalID == externalID {
			return true
		}
	}
	return false
}

// TestSearchEvidenceHybridRescuesExactFigure is the card's headline regression:
// a claim quoting an exact figure retrieves the passage carrying that figure even
// though cosine alone ranked it below the vector top-k. The figure passage is
// planted at a far embedding (flipVec(30)) so the pure vector search never
// surfaces it within topK, while the lexical branch matches its unaccented
// French text and RRF rescues it into the hybrid result.
func TestSearchEvidenceHybridRescuesExactFigure(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// Six near, generic passages (flipVec(1)..flipVec(6)) with no figure, so the
	// vector top-5 is saturated by them.
	for i := 1; i <= 6; i++ {
		c := figureChunk("hyb", "near-"+strconv.Itoa(i), "region "+strconv.Itoa(i), "donnees regionales sans chiffre", flipVec(i))
		if err := store.UpsertEmbeddedChunk(ctx, c); err != nil {
			t.Fatalf("upsert near-%d: %v", i, err)
		}
	}
	// The figure passage: accented French content, far from the query by cosine.
	fig := figureChunk("hyb", "figure", "Emploi", "Le taux de chômage atteint 9,7 pour cent en 2025.", flipVec(30))
	if err := store.UpsertEmbeddedChunk(ctx, fig); err != nil {
		t.Fatalf("upsert figure: %v", err)
	}

	query := flipVec(0)
	// The query text is unaccented ("chomage"), proving the immutable_unaccent
	// folding matches the accented stored content symmetrically.
	const queryText = "chomage 9,7 pour cent"

	vectorHits, err := store.SearchEvidence(ctx, query, 5, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if hasHit(vectorHits, "figure") {
		t.Fatalf("precondition failed: pure vector top-5 already contains the figure passage: %v", hitKeys(vectorHits))
	}

	hybridHits, err := store.SearchEvidenceHybrid(ctx, queryText, query, 5, 20, DefaultRRFConstant, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidenceHybrid: %v", err)
	}
	if !hasHit(hybridHits, "figure") {
		t.Fatalf("hybrid search did not rescue the exact-figure passage: %v", hitKeys(hybridHits))
	}
	// The wire shape is unchanged: results are nearest-first by cosine distance.
	for i := 1; i < len(hybridHits); i++ {
		if hybridHits[i-1].Distance > hybridHits[i].Distance {
			t.Fatalf("hybrid result not nearest-first: %v", hitKeys(hybridHits))
		}
	}
}

// TestSearchHybridRescuesExactFigureClaims is the same rescue over the claims
// corpus, exercising the claims lexical branch and generated column.
func TestSearchHybridRescuesExactFigureClaims(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	claims := make([]domain.Claim, 0, 7)
	for i := 1; i <= 6; i++ {
		claims = append(claims, domain.Claim{
			ID:        "near-" + strconv.Itoa(i),
			Text:      "affirmation generale sans chiffre precis",
			Verdict:   domain.VerdictCorroborates,
			Embedding: flipVec(i),
		})
	}
	claims = append(claims, domain.Claim{
		ID:        "figure",
		Text:      "Le déficit budgétaire s'élève à 5,4 pour cent du PIB.",
		Verdict:   domain.VerdictContradicts,
		Embedding: flipVec(30),
	})
	if err := store.Upsert(ctx, claims); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	query := flipVec(0)
	const queryText = "deficit 5,4 pour cent"

	vectorMatches, err := store.Search(ctx, query, 5, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if claimIDsContain(vectorMatches, "figure") {
		t.Fatalf("precondition failed: pure vector top-5 already contains the figure claim")
	}

	hybridMatches, err := store.SearchHybrid(ctx, queryText, query, 5, 20, DefaultRRFConstant, 0)
	if err != nil {
		t.Fatalf("SearchHybrid: %v", err)
	}
	if !claimIDsContain(hybridMatches, "figure") {
		t.Fatalf("hybrid search did not rescue the exact-figure claim")
	}
}

// TestSearchHybridEmptyTextFallsBackToVector proves an empty query text is the
// pure vector search: no lexical signal, identical results.
func TestSearchHybridEmptyTextFallsBackToVector(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedForBQ(ctx, t, store, 8)
	query := flipVec(0)

	vec, err := store.SearchEvidence(ctx, query, 5, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	hyb, err := store.SearchEvidenceHybrid(ctx, "   ", query, 5, 20, DefaultRRFConstant, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidenceHybrid: %v", err)
	}
	if strings.Join(hitKeys(vec), ",") != strings.Join(hitKeys(hyb), ",") {
		t.Fatalf("empty-text hybrid diverged from vector: %v vs %v", hitKeys(hyb), hitKeys(vec))
	}
}

func claimIDsContain(ms []domain.ClaimMatch, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

// TestLexicalIndexUsage is the EXPLAIN-verified index-usage guard: with
// sequential scans disabled the planner must reach each corpus's GIN index for
// the lexical @@ filter, proving the generated tsvector column and its index are
// built and applicable. enable_seqscan is forced off transaction-locally because
// on the tiny seeded corpus a seq scan is legitimately cheaper; the guard is that
// the index CAN serve the query, which is what the schema must guarantee.
func TestLexicalIndexUsage(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.Upsert(ctx, []domain.Claim{{
		ID: "c1", Text: "Le chômage recule en 2025.", Verdict: domain.VerdictCorroborates, Embedding: flipVec(0),
	}}); err != nil {
		t.Fatalf("Upsert claim: %v", err)
	}
	if err := store.UpsertEmbeddedChunk(ctx, figureChunk("hyb", "e1", "Emploi", "Le chômage recule en 2025.", flipVec(0))); err != nil {
		t.Fatalf("UpsertEmbeddedChunk: %v", err)
	}

	tests := []struct {
		name  string
		sql   string
		index string
		table string
	}{
		{
			name:  "claims lexical uses the claims GIN index",
			sql:   "EXPLAIN (FORMAT JSON) SELECT id FROM claims, websearch_to_tsquery('french', immutable_unaccent($1::text)) q WHERE search_vector @@ q ORDER BY ts_rank_cd(search_vector, q) DESC, id LIMIT 5",
			index: "claims_search_vector_gin",
			table: "claims",
		},
		{
			name:  "evidence lexical uses the evidence GIN index",
			sql:   "EXPLAIN (FORMAT JSON) SELECT source FROM evidence_chunks, websearch_to_tsquery('french', immutable_unaccent($1::text)) q WHERE search_vector @@ q AND embedding IS NOT NULL ORDER BY ts_rank_cd(search_vector, q) DESC LIMIT 5",
			index: "evidence_chunks_search_vector_gin",
			table: "evidence_chunks",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainPlan(ctx, t, store, tc.sql, "chomage")
			if !strings.Contains(plan, tc.index) {
				t.Fatalf("plan does not use %s:\n%s", tc.index, plan)
			}
			if strings.Contains(plan, `"Seq Scan"`) && strings.Contains(plan, `"Relation Name": "`+tc.table+`"`) {
				t.Fatalf("plan seq-scans %s despite the GIN index:\n%s", tc.table, plan)
			}
		})
	}
}

// explainPlan runs an EXPLAIN (FORMAT JSON) with seq scans disabled for the
// statement and returns the plan as a string. Both run in one transaction so the
// SET LOCAL is scoped to the EXPLAIN and never leaks onto a pooled connection.
func explainPlan(ctx context.Context, t *testing.T, store *Store, sql, arg string) string {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}
	var raw []byte
	if err := tx.QueryRow(ctx, sql, arg).Scan(&raw); err != nil {
		t.Fatalf("explain: %v", err)
	}
	// The plan is valid JSON; round-trip it to a compact string for substring
	// assertions that do not depend on EXPLAIN's whitespace.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	compact, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode plan: %v", err)
	}
	return string(compact)
}
