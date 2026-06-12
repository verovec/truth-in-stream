package postgres

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/store/db"
)

func fullVec(seed float32) []float32 {
	v := make([]float32, domain.EmbeddingDim)
	for i := range v {
		v[i] = seed
	}
	return v
}

func TestStoredRevisions(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, Title: "Paris", URL: "u", RevisionID: 100, Corpus: "simplewiki", Content: "a", Kind: domain.WikiChunkKindLead},
		{PageID: 1, ChunkIndex: 1, Title: "Paris", URL: "u", RevisionID: 100, Corpus: "simplewiki", Content: "b", Kind: domain.WikiChunkKindLead},
		{PageID: 2, ChunkIndex: 0, Title: "Lyon", URL: "u", RevisionID: 200, Corpus: "simplewiki", Content: "c", Kind: domain.WikiChunkKindLead},
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	got, err := store.StoredRevisions(ctx, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("StoredRevisions: %v", err)
	}
	want := map[int64]int64{1: 100, 2: 200}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("page %d revision = %d, want %d", k, got[k], v)
		}
	}
	if _, ok := got[3]; ok {
		t.Errorf("unstored page 3 present in result: %v", got)
	}

	empty, err := store.StoredRevisions(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("StoredRevisions(nil) = %v, %v", empty, err)
	}
}

func TestCountPages(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if n, err := store.CountPages(ctx); err != nil || n != 0 {
		t.Fatalf("CountPages empty = %d, %v", n, err)
	}
	chunks := []domain.WikiChunk{
		wikiChunk(1, 0, "a"), wikiChunk(1, 1, "b"), wikiChunk(2, 0, "c"),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	if n, err := store.CountPages(ctx); err != nil || n != 2 {
		t.Fatalf("CountPages = %d, %v; want 2 distinct pages", n, err)
	}
}

func TestDeletePagesByTitle(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, Title: "Paris", URL: "u", RevisionID: 1, Corpus: "simplewiki", Content: "a", Kind: domain.WikiChunkKindLead},
		{PageID: 1, ChunkIndex: 1, Title: "Paris", URL: "u", RevisionID: 1, Corpus: "simplewiki", Content: "b", Kind: domain.WikiChunkKindLead},
		{PageID: 2, ChunkIndex: 0, Title: "Lyon", URL: "u", RevisionID: 1, Corpus: "simplewiki", Content: "c", Kind: domain.WikiChunkKindLead},
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	if err := store.DeletePagesByTitle(ctx, []string{"Paris"}); err != nil {
		t.Fatalf("DeletePagesByTitle: %v", err)
	}
	if n, err := store.queries.CountWikiChunksForPage(ctx, 1); err != nil || n != 0 {
		t.Errorf("page 1 (Paris) chunks after delete = %d, %v; want 0", n, err)
	}
	if n, err := store.queries.CountWikiChunksForPage(ctx, 2); err != nil || n != 1 {
		t.Errorf("page 2 (Lyon) chunks = %d, %v; want 1 (untouched)", n, err)
	}

	if err := store.DeletePagesByTitle(ctx, nil); err != nil {
		t.Errorf("DeletePagesByTitle(nil) = %v", err)
	}
}

func TestSetChunkEmbeddings(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks := []domain.WikiChunk{
		wikiChunk(1, 0, "Paris\n\nlead"),
		wikiChunk(1, 1, "Paris\n\nmore"),
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	chunks[0].Embedding = fullVec(0.1)
	chunks[1].Embedding = fullVec(0.2)
	if err := store.SetChunkEmbeddings(ctx, chunks); err != nil {
		t.Fatalf("SetChunkEmbeddings: %v", err)
	}

	got, err := store.queries.GetWikiChunk(ctx, db.GetWikiChunkParams{PageID: 1, ChunkIndex: 0})
	if err != nil {
		t.Fatalf("GetWikiChunk: %v", err)
	}
	if got.EmbeddingIsNull {
		t.Error("embedding still NULL after SetChunkEmbeddings")
	}

	// Every chunk is now embedded, so nothing remains for the embed pass to pick up.
	pending, err := store.UnembeddedChunks(ctx, domain.WikiCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedChunks: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("UnembeddedChunks returned %d chunks after embedding all", len(pending))
	}
}

func TestSetChunkEmbeddingsRejectsWrongDim(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	if err := store.UpsertChunks(ctx, []domain.WikiChunk{wikiChunk(1, 0, "x")}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	bad := wikiChunk(1, 0, "x")
	bad.Embedding = []float32{1, 2, 3}
	if err := store.SetChunkEmbeddings(ctx, []domain.WikiChunk{bad}); err == nil {
		t.Fatal("SetChunkEmbeddings accepted a short embedding, want error")
	}
}
