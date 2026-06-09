package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

const dims = 1024

// setupStore opens a store against TEST_DATABASE_URL and resets the schema.
// It skips the test when the variable is unset so unit runs stay hermetic;
// CI and `docker compose` provide a pgvector-enabled Postgres.
func setupStore(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping pgvector integration test")
	}

	ctx := context.Background()
	resetSchema(t, ctx, dsn)

	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func resetSchema(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()

	migrations := filepath.Join("..", "..", "..", "migrations")
	down, err := os.ReadFile(filepath.Join(migrations, "0001_init.down.sql"))
	if err != nil {
		t.Fatalf("reset: read down migration: %v", err)
	}
	up, err := os.ReadFile(filepath.Join(migrations, "0001_init.up.sql"))
	if err != nil {
		t.Fatalf("reset: read up migration: %v", err)
	}
	// Run down then up so a stray object from a prior run cannot survive, and
	// so both migration directions are exercised.
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("reset: apply down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("reset: apply up migration: %v", err)
	}
}

// unitVec returns a 1024-dim vector that is 1 at index hot, 0 elsewhere.
// Distinct hot indices are mutually orthogonal, so cosine distance is 1
// between any two and 0 to itself - convenient for asserting ordering.
func unitVec(hot int) []float32 {
	v := make([]float32, dims)
	v[hot] = 1
	return v
}

func TestSearchOrdersByCosineDistance(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	docs := []domain.Document{
		{ID: "a", Content: "alpha", Metadata: map[string]any{"k": "a"}, Embedding: unitVec(0)},
		{ID: "b", Content: "bravo", Metadata: map[string]any{"k": "b"}, Embedding: unitVec(1)},
		{ID: "c", Content: "charlie", Metadata: map[string]any{"k": "c"}, Embedding: unitVec(2)},
	}
	if err := store.Upsert(ctx, docs); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	tests := []struct {
		name      string
		query     []float32
		topK      int
		wantFirst string
		wantLen   int
	}{
		{name: "nearest is a", query: unitVec(0), topK: 3, wantFirst: "a", wantLen: 3},
		{name: "nearest is b", query: unitVec(1), topK: 3, wantFirst: "b", wantLen: 3},
		{name: "topK truncates", query: unitVec(2), topK: 1, wantFirst: "c", wantLen: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Search(ctx, tc.query, tc.topK)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("got %d matches, want %d", len(got), tc.wantLen)
			}
			if got[0].ID != tc.wantFirst {
				t.Fatalf("nearest = %q, want %q", got[0].ID, tc.wantFirst)
			}
			if got[0].Distance > 1e-4 {
				t.Errorf("nearest distance = %v, want ~0", got[0].Distance)
			}
			if got[0].Metadata["k"] != tc.wantFirst {
				t.Errorf("metadata not round-tripped: got %v", got[0].Metadata)
			}
		})
	}
}

func TestUpsertReplacesByID(t *testing.T) {
	store := setupStore(t)
	ctx := context.Background()

	if err := store.Upsert(ctx, []domain.Document{
		{ID: "x", Content: "first", Embedding: unitVec(0)},
	}); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}
	if err := store.Upsert(ctx, []domain.Document{
		{ID: "x", Content: "second", Embedding: unitVec(0)},
	}); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}

	got, err := store.Search(ctx, unitVec(0), 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1 (upsert must not duplicate)", len(got))
	}
	if got[0].Content != "second" {
		t.Errorf("content = %q, want %q", got[0].Content, "second")
	}
}

func TestUpsertEmptyIsNoop(t *testing.T) {
	store := setupStore(t)
	if err := store.Upsert(context.Background(), nil); err != nil {
		t.Fatalf("Upsert(nil): %v", err)
	}
}
