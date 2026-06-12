package postgres

import (
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func ptrInt32(v int32) *int32       { return &v }
func ptrFloat64(v float64) *float64 { return &v }

// readClustering reads chunk 0 of a page's stored cluster id and importance back,
// nil when the column is NULL.
func readClustering(t *testing.T, store *Store, pageID int64) (clusterID *int32, importance *float64) {
	t.Helper()
	if err := store.pool.QueryRow(
		t.Context(),
		"SELECT cluster_id, importance FROM wiki_chunks WHERE page_id = $1 AND chunk_index = 0", pageID,
	).Scan(&clusterID, &importance); err != nil {
		t.Fatalf("read clustering for page %d: %v", pageID, err)
	}
	return clusterID, importance
}

func TestSetChunkClusteringRoundTrips(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(2, 0, "v2"), unitVec(2)),
	})

	// A fresh embed leaves cluster_id and importance NULL until the job runs.
	if cid, imp := readClustering(t, store, 1); cid != nil || imp != nil {
		t.Fatalf("page 1 clustering before the job = (%v, %v), want (nil, nil)", cid, imp)
	}

	if err := store.SetChunkClustering(ctx, []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, ClusterID: ptrInt32(3), Importance: ptrFloat64(0.75)},
		{PageID: 2, ChunkIndex: 0, ClusterID: ptrInt32(7), Importance: ptrFloat64(0.20)},
	}); err != nil {
		t.Fatalf("SetChunkClustering: %v", err)
	}

	cid, imp := readClustering(t, store, 1)
	if cid == nil || *cid != 3 || imp == nil || *imp != 0.75 {
		t.Errorf("page 1 clustering = (%v, %v), want (3, 0.75)", cid, imp)
	}
	cid, imp = readClustering(t, store, 2)
	if cid == nil || *cid != 7 || imp == nil || *imp != 0.20 {
		t.Errorf("page 2 clustering = (%v, %v), want (7, 0.20)", cid, imp)
	}

	// Idempotent rewrite: re-running with the same scores leaves the same values.
	if err := store.SetChunkClustering(ctx, []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, ClusterID: ptrInt32(3), Importance: ptrFloat64(0.75)},
	}); err != nil {
		t.Fatalf("SetChunkClustering (rewrite): %v", err)
	}
	if cid, imp := readClustering(t, store, 1); *cid != 3 || *imp != 0.75 {
		t.Errorf("page 1 after idempotent rewrite = (%d, %v), want (3, 0.75)", *cid, *imp)
	}
}

func TestSetChunkClusteringRequiresBothScores(t *testing.T) {
	store := setupStore(t)
	seedChunks(t, store, []domain.WikiChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	err := store.SetChunkClustering(t.Context(), []domain.WikiChunk{{PageID: 1, ChunkIndex: 0, ClusterID: ptrInt32(1)}})
	if err == nil {
		t.Fatal("want error when importance is missing")
	}
}

func TestEmbeddedChunksReadsVectorsInKeysetOrder(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(2, 0, "v2"), unitVec(2)),
		wikiChunk(3, 0, "v3"), // no embedding: must be excluded
	})

	got, err := store.EmbeddedChunks(ctx, domain.WikiCursor{}, 10)
	if err != nil {
		t.Fatalf("EmbeddedChunks: %v", err)
	}
	if len(got) != 2 || got[0].PageID != 1 || got[1].PageID != 2 {
		t.Fatalf("got %+v, want pages 1 and 2 (the embedded ones) in order", got)
	}
	for _, c := range got {
		if len(c.Embedding) != domain.EmbeddingDim {
			t.Errorf("page %d embedding dim = %d, want %d", c.PageID, len(c.Embedding), domain.EmbeddingDim)
		}
	}

	// The keyset cursor pages past page 1.
	rest, err := store.EmbeddedChunks(ctx, domain.WikiCursor{PageID: 1, ChunkIndex: 0}, 10)
	if err != nil || len(rest) != 1 || rest[0].PageID != 2 {
		t.Fatalf("rest = %+v, %v; want only page 2", rest, err)
	}
}

func TestUnembeddedStagingCarriesForwardLiveImportance(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Live corpus: page 1 ("v1") and page 3 ("v3") embedded and clustered.
	seedChunks(t, store, []domain.WikiChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(3, 0, "v3"), unitVec(3)),
	})
	if err := store.SetChunkClustering(ctx, []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, ClusterID: ptrInt32(2), Importance: ptrFloat64(0.8)},
		{PageID: 3, ChunkIndex: 0, ClusterID: ptrInt32(2), Importance: ptrFloat64(0.5)},
	}); err != nil {
		t.Fatalf("SetChunkClustering: %v", err)
	}

	// A re-ingest stages: page 1 unchanged ("v1"), a brand-new page 2 with no live
	// counterpart, and page 3 with rewritten content ("v3-edited").
	stageChunks(t, store, "v2", []domain.WikiChunk{
		wikiChunk(1, 0, "v1"),
		wikiChunk(2, 0, "v2"),
		wikiChunk(3, 0, "v3-edited"),
	})

	got, err := store.UnembeddedStaging(ctx, domain.WikiCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedStaging: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d staging chunks, want 3", len(got))
	}
	byPage := map[int64]domain.WikiChunk{}
	for _, c := range got {
		byPage[c.PageID] = c
	}
	if imp := byPage[1].Importance; imp == nil || *imp != 0.8 {
		t.Errorf("page 1 staging importance = %v, want 0.8 carried forward (content unchanged)", imp)
	}
	if imp := byPage[2].Importance; imp != nil {
		t.Errorf("page 2 staging importance = %v, want nil (new chunk, no prior clustering)", imp)
	}
	if imp := byPage[3].Importance; imp != nil {
		t.Errorf("page 3 staging importance = %v, want nil (content changed, stale importance must not carry)", imp)
	}
}
