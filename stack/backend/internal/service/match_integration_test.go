package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/ingest"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// claimsSchemaLock is the advisory lock key serializing every integration
// test that resets the shared claims schema; the store integration tests
// (store/postgres) take the same key.
const claimsSchemaLock = int64(0x747275746873)

// lockSchema takes the schema advisory lock for the duration of the test.
// Closing the session at cleanup releases the lock.
func lockSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("lock: connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", claimsSchemaLock); err != nil {
		t.Fatalf("lock: acquire: %v", err)
	}
}

// vecEmbedder is a deterministic embedder backed by fixed text-to-vector
// lookup tables, one per input type, so document and query embeddings are
// crafted independently and no live API is involved.
type vecEmbedder struct {
	docs    map[string][]float32
	queries map[string][]float32
}

func (e vecEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	return lookupVecs(e.docs, texts)
}

func (e vecEmbedder) EmbedQueries(_ context.Context, texts []string) ([][]float32, error) {
	return lookupVecs(e.queries, texts)
}

func lookupVecs(vecs map[string][]float32, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v, ok := vecs[text]
		if !ok {
			return nil, fmt.Errorf("no fixed vector for %q", text)
		}
		out[i] = v
	}
	return out, nil
}

// setupSeededStore resets the schema, ingests the repo seed claims with one
// deterministic unit vector per claim, and returns the store plus the
// claim-text-to-vector assignment. It skips when TEST_DATABASE_URL is unset,
// like the store integration tests.
func setupSeededStore(t *testing.T) (*postgres.Store, []ingest.SeedClaim, map[string][]float32) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping pgvector integration test")
	}

	ctx := t.Context()
	lockSchema(ctx, t, dsn)
	resetSchema(ctx, t, dsn)

	seedFile, err := os.Open(filepath.Join("..", "..", "seed", "claims.json"))
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	defer func() { _ = seedFile.Close() }()
	seeds, err := ingest.LoadSeed(seedFile)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}

	docs := make(map[string][]float32, len(seeds))
	for i, s := range seeds {
		docs[s.Text] = hotVec(t, map[int]float64{i: 1})
	}

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	if _, err := ingest.Run(ctx, store, vecEmbedder{docs: docs}, seeds, 0); err != nil {
		t.Fatalf("ingest.Run: %v", err)
	}
	return store, seeds, docs
}

// resetSchema mirrors the store integration tests: drop the known tables and
// replay every up migration in order.
func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	// Hold the shared schema-reset lock for the whole test, not just the
	// reset: the integration packages share one database, so releasing after
	// the reset would let another package drop these tables mid-test. Cleanup
	// runs at test end, serializing every DB-touching test across packages.
	release, err := pgtest.AcquireSchemaLock(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(release)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claim_checks, claims, documents, document_sentences, document_claims, segment_results, processed_videos, video_analyses, videos, tv_channels, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, evidence_chunks, evidence_chunks_staging, evidence_chunks_old, evidence_sync_state, political_claims, voting_records"); err != nil {
		t.Fatalf("reset: drop tables: %v", err)
	}

	ups, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("reset: glob migrations: %v", err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		sql, err := os.ReadFile(up)
		if err != nil {
			t.Fatalf("reset: read %s: %v", up, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("reset: apply %s: %v", up, err)
		}
	}
}

// hotVec builds a unit-norm EmbeddingDim vector with the given components and
// the remaining weight on a filler dimension no claim vector uses, so the
// cosine similarity to claim i is exactly components[i]. The squared
// components must not exceed 1 or no unit vector exists.
func hotVec(t *testing.T, components map[int]float64) []float32 {
	t.Helper()
	// fillerIndex must stay above every seed's hot index, so the filler
	// dimension never collides with a claim dimension. Safe as long as the
	// seed set has at most fillerIndex entries.
	const fillerIndex = domain.EmbeddingDim - 1
	v := make([]float32, domain.EmbeddingDim)
	rest := 1.0
	for hot, weight := range components {
		v[hot] = float32(weight)
		rest -= weight * weight
	}
	if rest < 0 {
		t.Fatalf("components %v have squared norm above 1; similarities would be rescaled", components)
	}
	v[fillerIndex] = float32(math.Sqrt(rest))
	return v
}

func seedIndex(t *testing.T, seeds []ingest.SeedClaim, id string) int {
	t.Helper()
	for i, s := range seeds {
		if s.ID == id {
			return i
		}
	}
	t.Fatalf("seed claim %q not found", id)
	return -1
}

func seedByID(t *testing.T, seeds []ingest.SeedClaim, id string) ingest.SeedClaim {
	t.Helper()
	return seeds[seedIndex(t, seeds, id)]
}

func TestMatcherAgainstSeededStore(t *testing.T) {
	store, seeds, docs := setupSeededStore(t)

	seedMatch := func(id string, score float64) Match {
		s := seedByID(t, seeds, id)
		return Match{Kind: domain.MatchKindClaim, ClaimID: s.ID, Text: s.Text, Verdict: s.Verdict, Sources: s.Sources, EvidenceID: domain.ComposeEvidenceID(domain.MatchKindClaim, s.ID, 0), Score: score}
	}

	tests := []struct {
		name       string
		segment    string
		similarity map[string]float64
		topK       int
		threshold  float64
		want       []Match
	}{
		{
			name:       "paraphrased claim surfaces with verdict and sources",
			segment:    "Apparently you can see the Great Wall of China from orbit with your bare eyes.",
			similarity: map[string]float64{"great-wall-from-space": 0.9},
			topK:       5,
			threshold:  0.5,
			want:       []Match{seedMatch("great-wall-from-space", 0.9)},
		},
		{
			name:    "multiple matches ranked by similarity",
			segment: "Water boils at one hundred Celsius, just like Everest is the tallest mountain.",
			similarity: map[string]float64{
				"water-boiling-point": 0.8,
				"everest-highest":     0.55,
			},
			topK:      5,
			threshold: 0.5,
			want: []Match{
				seedMatch("water-boiling-point", 0.8),
				seedMatch("everest-highest", 0.55),
			},
		},
		{
			name:    "weak matches stay below the threshold",
			segment: "Completely unrelated gaming commentary about a speedrun strategy.",
			similarity: map[string]float64{
				"vaccines-autism": 0.3,
			},
			topK:      5,
			threshold: 0.5,
			want:      []Match{},
		},
		{
			name:    "top k caps the result set",
			segment: "A segment brushing several myths at once.",
			similarity: map[string]float64{
				"ten-percent-brain":       0.7,
				"lightning-strikes-twice": 0.5,
				"amazon-oxygen-share":     0.45,
			},
			topK:      2,
			threshold: 0.4,
			want: []Match{
				seedMatch("ten-percent-brain", 0.7),
				seedMatch("lightning-strikes-twice", 0.5),
			},
		},
	}

	queries := make(map[string][]float32, len(tests))
	for _, tc := range tests {
		components := make(map[int]float64, len(tc.similarity))
		for id, sim := range tc.similarity {
			components[seedIndex(t, seeds, id)] = sim
		}
		queries[tc.segment] = hotVec(t, components)
	}
	embedder := vecEmbedder{docs: docs, queries: queries}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewMatcher(embedder, store, store, MatcherConfig{
				TopK:                  tc.topK,
				ScoreThreshold:        tc.threshold,
				EvidenceTopK:          0,
				MaxResults:            10,
				EmbedConcurrency:      2,
				Timeout:               30 * time.Second,
				ConfidenceClusterSize: 5,
				ConfidenceLeadWeight:  1,
				ConfidenceBodyWeight:  0.5,
			})
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}

			got, _, err := m.MatchSegment(t.Context(), tc.segment)
			if err != nil {
				t.Fatalf("MatchSegment: %v", err)
			}
			// halfvec storage is float16, so scores land within ~1e-3 of the
			// constructed similarities.
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateApprox(0, 5e-3)); diff != "" {
				t.Errorf("matches mismatch (-want +got):\n%s", diff)
			}
			for _, mt := range got {
				kind, source, chunk, err := domain.ParseEvidenceID(mt.EvidenceID)
				if err != nil {
					t.Errorf("ParseEvidenceID(%q): %v", mt.EvidenceID, err)
					continue
				}
				if kind != domain.MatchKindClaim || source != mt.ClaimID || chunk != 0 {
					t.Errorf("evidence id %q decoded to (%q, %q, %d), want (claim, %q, 0)", mt.EvidenceID, kind, source, chunk, mt.ClaimID)
				}
			}
		})
	}
}
