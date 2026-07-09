package postgres

import (
	"strconv"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func ptrInt32(v int32) *int32       { return &v }
func ptrFloat64(v float64) *float64 { return &v }

// clusteringChunk builds a clustering write for a page: the cluster id and
// importance ride in the chunk metadata (WikiMetadata's cluster_id/importance
// keys), which is what SetChunkClustering reads.
func clusteringChunk(pageID int64, clusterID *int32, importance *float64) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     "simplewiki",
		ExternalID: strconv.FormatInt(pageID, 10),
		ChunkIndex: 0,
		Metadata:   domain.WikiMetadata{ClusterID: clusterID, Importance: importance}.Map(),
	}
}

// chunkImportance reads the importance a scan carried on a chunk's metadata, nil
// when none was carried.
func chunkImportance(t *testing.T, c domain.EvidenceChunk) *float64 {
	t.Helper()
	wm, err := domain.ParseWikiMetadata(c.Metadata)
	if err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	return wm.Importance
}

// readClustering reads chunk 0 of a page's stored cluster id and importance back
// from the metadata jsonb, nil when the key is absent.
func readClustering(t *testing.T, store *Store, pageID int64) (clusterID *int32, importance *float64) {
	t.Helper()
	if err := store.pool.QueryRow(
		t.Context(),
		"SELECT (metadata->>'cluster_id')::int, (metadata->>'importance')::float8 FROM evidence_chunks WHERE source = 'simplewiki' AND external_id = $1 AND chunk_index = 0",
		strconv.FormatInt(pageID, 10),
	).Scan(&clusterID, &importance); err != nil {
		t.Fatalf("read clustering for page %d: %v", pageID, err)
	}
	return clusterID, importance
}

func TestSetChunkClusteringRoundTrips(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(2, 0, "v2"), unitVec(2)),
	})

	// A fresh embed leaves cluster_id and importance absent until the job runs.
	if cid, imp := readClustering(t, store, 1); cid != nil || imp != nil {
		t.Fatalf("page 1 clustering before the job = (%v, %v), want (nil, nil)", cid, imp)
	}

	if err := store.SetChunkClustering(ctx, []domain.EvidenceChunk{
		clusteringChunk(1, ptrInt32(3), ptrFloat64(0.75)),
		clusteringChunk(2, ptrInt32(7), ptrFloat64(0.20)),
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
	if err := store.SetChunkClustering(ctx, []domain.EvidenceChunk{
		clusteringChunk(1, ptrInt32(3), ptrFloat64(0.75)),
	}); err != nil {
		t.Fatalf("SetChunkClustering (rewrite): %v", err)
	}
	if cid, imp := readClustering(t, store, 1); *cid != 3 || *imp != 0.75 {
		t.Errorf("page 1 after idempotent rewrite = (%d, %v), want (3, 0.75)", *cid, *imp)
	}
}

func TestSetChunkClusteringRequiresBothScores(t *testing.T) {
	store := setupStore(t)
	seedChunks(t, store, []domain.EvidenceChunk{withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1))})
	err := store.SetChunkClustering(t.Context(), []domain.EvidenceChunk{clusteringChunk(1, ptrInt32(1), nil)})
	if err == nil {
		t.Fatal("want error when importance is missing")
	}
}

func TestEmbeddedChunksReadsVectorsInKeysetOrder(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(2, 0, "v2"), unitVec(2)),
		wikiChunk(3, 0, "v3"), // no embedding: must be excluded
	})

	got, err := store.EmbeddedChunks(ctx, domain.EvidenceCursor{}, 10)
	if err != nil {
		t.Fatalf("EmbeddedChunks: %v", err)
	}
	if len(got) != 2 || got[0].ExternalID != "1" || got[1].ExternalID != "2" {
		t.Fatalf("got %+v, want pages 1 and 2 (the embedded ones) in order", got)
	}
	for _, c := range got {
		if len(c.Embedding) != domain.EmbeddingDim {
			t.Errorf("page %s embedding dim = %d, want %d", c.ExternalID, len(c.Embedding), domain.EmbeddingDim)
		}
	}

	// The keyset cursor pages past page 1.
	rest, err := store.EmbeddedChunks(ctx, domain.EvidenceCursor{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0}, 10)
	if err != nil || len(rest) != 1 || rest[0].ExternalID != "2" {
		t.Fatalf("rest = %+v, %v; want only page 2", rest, err)
	}
}

func TestUnembeddedStagingCarriesForwardLiveImportance(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()
	// Live corpus: page 1 ("v1") and page 3 ("v3") embedded and clustered.
	seedChunks(t, store, []domain.EvidenceChunk{
		withEmbedding(wikiChunk(1, 0, "v1"), unitVec(1)),
		withEmbedding(wikiChunk(3, 0, "v3"), unitVec(3)),
	})
	if err := store.SetChunkClustering(ctx, []domain.EvidenceChunk{
		clusteringChunk(1, ptrInt32(2), ptrFloat64(0.8)),
		clusteringChunk(3, ptrInt32(2), ptrFloat64(0.5)),
	}); err != nil {
		t.Fatalf("SetChunkClustering: %v", err)
	}

	// A re-ingest stages: page 1 unchanged ("v1"), a brand-new page 2 with no live
	// counterpart, and page 3 with rewritten content ("v3-edited").
	stageChunks(t, store, "v2", []domain.EvidenceChunk{
		wikiChunk(1, 0, "v1"),
		wikiChunk(2, 0, "v2"),
		wikiChunk(3, 0, "v3-edited"),
	})

	got, err := store.UnembeddedStaging(ctx, domain.EvidenceCursor{}, 10)
	if err != nil {
		t.Fatalf("UnembeddedStaging: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d staging chunks, want 3", len(got))
	}
	byPage := map[string]domain.EvidenceChunk{}
	for _, c := range got {
		byPage[c.ExternalID] = c
	}
	if imp := chunkImportance(t, byPage["1"]); imp == nil || *imp != 0.8 {
		t.Errorf("page 1 staging importance = %v, want 0.8 carried forward (content unchanged)", imp)
	}
	if imp := chunkImportance(t, byPage["2"]); imp != nil {
		t.Errorf("page 2 staging importance = %v, want nil (new chunk, no prior clustering)", imp)
	}
	if imp := chunkImportance(t, byPage["3"]); imp != nil {
		t.Errorf("page 3 staging importance = %v, want nil (content changed, stale importance must not carry)", imp)
	}
}
