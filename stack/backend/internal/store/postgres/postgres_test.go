package postgres

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
)

const dims = domain.EmbeddingDim

// claimsSchemaLock is the advisory lock key serializing every integration
// test that resets the shared claims schema. `go test ./...` runs package
// test binaries in parallel against the same TEST_DATABASE_URL, so the
// service integration tests take the same key (service.claimsSchemaLock).
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

// setupStore opens a store against TEST_DATABASE_URL and resets the schema.
// It skips the test when the variable is unset so unit runs stay hermetic;
// CI and `docker compose` provide a pgvector-enabled Postgres.
func setupStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping pgvector integration test")
	}

	ctx := t.Context()
	lockSchema(ctx, t, dsn)
	resetSchema(ctx, t, dsn)

	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// resetSchema brings the database to the latest schema from a clean slate that
// does not depend on the current migration version: it drops the known tables,
// then applies every up migration in order, exactly as CI and golang-migrate
// apply them. (Down migrations are inverses valid only at their own version, so
// they are not safe to replay blindly from arbitrary state.)
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

	// evidence_chunks_staging and evidence_chunks_old are runtime tables the
	// bulk-embedding pipeline creates outside the migration schema; drop them too so
	// a test that leaves staging behind cannot leak rows into the next test.
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, document_sentences, document_claims, segment_results, processed_videos, videos, tv_channels, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, evidence_chunks, evidence_chunks_staging, evidence_chunks_old, evidence_sync_state, political_claims, voting_records"); err != nil {
		t.Fatalf("reset: drop tables: %v", err)
	}

	dir := filepath.Join("..", "..", "..", "migrations")
	ups, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		t.Fatalf("reset: glob migrations: %v", err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		execSQLFile(ctx, t, pool, up)
	}
}

func execSQLFile(ctx context.Context, t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	sql, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reset: read %s: %v", path, err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("reset: apply %s: %v", path, err)
	}
}

// unitVec returns a dims-length vector that is 1 at index hot, 0 elsewhere.
// Distinct hot indices are mutually orthogonal, so cosine distance is 1
// between any two and 0 to itself - convenient for asserting ordering.
func unitVec(hot int) []float32 {
	v := make([]float32, dims)
	v[hot] = 1
	return v
}

func TestSearchOrdersByCosineDistance(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	claims := []domain.Claim{
		{ID: "a", Text: "alpha", Verdict: domain.VerdictCorroborates, Sources: []domain.Source{{Title: "A", URL: "https://a.example"}}, Embedding: unitVec(0)},
		{ID: "b", Text: "bravo", Verdict: domain.VerdictContradicts, Sources: []domain.Source{{Title: "B", URL: "https://b.example"}}, Embedding: unitVec(1)},
		{ID: "c", Text: "charlie", Verdict: domain.VerdictUnclear, Embedding: unitVec(2)},
	}
	if err := store.Upsert(ctx, claims); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	tests := []struct {
		name        string
		query       []float32
		topK        int
		wantFirst   string
		wantVerdict domain.Verdict
		wantLen     int
	}{
		{name: "nearest is a", query: unitVec(0), topK: 3, wantFirst: "a", wantVerdict: domain.VerdictCorroborates, wantLen: 3},
		{name: "nearest is b", query: unitVec(1), topK: 3, wantFirst: "b", wantVerdict: domain.VerdictContradicts, wantLen: 3},
		{name: "topK truncates", query: unitVec(2), topK: 1, wantFirst: "c", wantVerdict: domain.VerdictUnclear, wantLen: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Search(ctx, tc.query, tc.topK, 0)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("got %d matches, want %d", len(got), tc.wantLen)
			}
			if got[0].ID != tc.wantFirst {
				t.Fatalf("nearest = %q, want %q", got[0].ID, tc.wantFirst)
			}
			if got[0].Verdict != tc.wantVerdict {
				t.Errorf("verdict = %q, want %q", got[0].Verdict, tc.wantVerdict)
			}
			if got[0].Distance > 1e-4 {
				t.Errorf("nearest distance = %v, want ~0", got[0].Distance)
			}
		})
	}
}

func TestSearchRoundTripsSources(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	want := []domain.Source{
		{Title: "First", URL: "https://first.example/path"},
		{Title: "Second", URL: "https://second.example"},
	}
	if err := store.Upsert(ctx, []domain.Claim{
		{ID: "s", Text: "sourced", Verdict: domain.VerdictCorroborates, Sources: want, Embedding: unitVec(0)},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := store.Search(ctx, unitVec(0), 1, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if diff := cmp.Diff(want, got[0].Sources); diff != "" {
		t.Errorf("sources mismatch (-want +got):\n%s", diff)
	}
}

func TestUpsertReplacesByID(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.Upsert(ctx, []domain.Claim{
		{ID: "x", Text: "first", Verdict: domain.VerdictCorroborates, Embedding: unitVec(0)},
	}); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	if err := store.Upsert(ctx, []domain.Claim{
		{ID: "x", Text: "second", Verdict: domain.VerdictContradicts, Embedding: unitVec(0)},
	}); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}

	got, err := store.Search(ctx, unitVec(0), 5, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1 (upsert must not duplicate)", len(got))
	}
	if got[0].Text != "second" || got[0].Verdict != domain.VerdictContradicts {
		t.Errorf("claim = %q/%q, want second/contradicts", got[0].Text, got[0].Verdict)
	}
}

func TestUpsertRejectsInvalidVerdict(t *testing.T) {
	store := setupStore(t)
	err := store.Upsert(t.Context(), []domain.Claim{
		{ID: "bad", Text: "no verdict", Verdict: "maybe", Embedding: unitVec(0)},
	})
	if err == nil {
		t.Fatal("Upsert with invalid verdict: want error, got nil")
	}
}

func TestUpsertRejectsWrongDimension(t *testing.T) {
	store := setupStore(t)
	err := store.Upsert(t.Context(), []domain.Claim{
		{ID: "bad", Text: "short vector", Verdict: domain.VerdictUnclear, Embedding: []float32{1, 2, 3}},
	})
	if err == nil {
		t.Fatal("Upsert with wrong dimension: want error, got nil")
	}
}

func TestSearchRejectsWrongDimension(t *testing.T) {
	store := setupStore(t)
	_, err := store.Search(t.Context(), []float32{1, 2, 3}, 5, 0)
	if err == nil {
		t.Fatal("Search with wrong dimension: want error, got nil")
	}
}

func TestUpsertEmptyIsNoop(t *testing.T) {
	store := setupStore(t)
	if err := store.Upsert(t.Context(), nil); err != nil {
		t.Fatalf("Upsert(nil): %v", err)
	}
}

// These run without a database: they exercise the jsonb encoding directly.

func TestMarshalSourcesShape(t *testing.T) {
	t.Parallel()
	raw, err := marshalSources([]domain.Source{{Title: "T", URL: "https://u.example"}})
	if err != nil {
		t.Fatalf("marshalSources: %v", err)
	}
	if got, want := string(raw), `[{"title":"T","url":"https://u.example"}]`; got != want {
		t.Errorf("encoding = %s, want %s", got, want)
	}
}

func TestSourcesEncodingRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []domain.Source
		want []domain.Source
	}{
		{name: "nil normalizes to empty", in: nil, want: []domain.Source{}},
		{name: "empty stays empty", in: []domain.Source{}, want: []domain.Source{}},
		{
			name: "multiple preserved in order",
			in:   []domain.Source{{Title: "A", URL: "https://a"}, {Title: "B", URL: "https://b"}},
			want: []domain.Source{{Title: "A", URL: "https://a"}, {Title: "B", URL: "https://b"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := marshalSources(tc.in)
			if err != nil {
				t.Fatalf("marshalSources: %v", err)
			}
			got, err := unmarshalSources(raw)
			if err != nil {
				t.Fatalf("unmarshalSources: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("round trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUnmarshalSourcesNullJSON(t *testing.T) {
	t.Parallel()
	got, err := unmarshalSources([]byte("null"))
	if err != nil {
		t.Fatalf("unmarshalSources(null): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d sources, want 0", len(got))
	}
}
