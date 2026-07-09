package main

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeStore serves a fixed embedded corpus in keyset pages and records every
// clustering write so a test can assert the orchestration scored and batched
// every chunk.
type fakeStore struct {
	chunks  []domain.EvidenceChunk
	written []domain.EvidenceChunk
	batches int
}

func (f *fakeStore) EmbeddedChunks(_ context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	var out []domain.EvidenceChunk
	for _, c := range f.chunks {
		if afterCursor(c, cur) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].ExternalID != out[j].ExternalID {
			return out[i].ExternalID < out[j].ExternalID
		}
		return out[i].ChunkIndex < out[j].ChunkIndex
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// afterCursor reports whether c sorts strictly after cur in the store's keyset
// order (source, external_id, chunk_index).
func afterCursor(c domain.EvidenceChunk, cur domain.EvidenceCursor) bool {
	if c.Source != cur.Source {
		return c.Source > cur.Source
	}
	if c.ExternalID != cur.ExternalID {
		return c.ExternalID > cur.ExternalID
	}
	return int32(c.ChunkIndex) > cur.ChunkIndex
}

func (f *fakeStore) SetChunkClustering(_ context.Context, chunks []domain.EvidenceChunk) error {
	f.batches++
	f.written = append(f.written, chunks...)
	return nil
}

func embeddedChunk(pageID int64, vec []float32) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     "simplewiki",
		ExternalID: strconv.FormatInt(pageID, 10),
		ChunkIndex: 0,
		Embedding:  vec,
	}
}

func testClusterConfig() config.WikiCluster {
	return config.WikiCluster{K: 2, MaxIters: 20, Seed: 1, ReadBatch: 3, WriteBatch: 4}
}

func TestClusterCorpusScoresAndWritesEveryChunk(t *testing.T) {
	t.Parallel()
	chunks := make([]domain.EvidenceChunk, 0, 10)
	for i := range 6 {
		chunks = append(chunks, embeddedChunk(int64(i+1), []float32{10, 0}))
	}
	for i := range 4 {
		chunks = append(chunks, embeddedChunk(int64(i+7), []float32{0, 10}))
	}
	store := &fakeStore{chunks: chunks}

	st, err := clusterCorpus(t.Context(), discardLogger(), store, testClusterConfig())
	if err != nil {
		t.Fatalf("clusterCorpus: %v", err)
	}
	if len(store.written) != 10 {
		t.Fatalf("wrote %d chunks, want 10 (every chunk scored)", len(store.written))
	}
	for _, c := range store.written {
		wm, err := domain.ParseWikiMetadata(c.Metadata)
		if err != nil {
			t.Fatalf("chunk %s metadata parse: %v", c.ExternalID, err)
		}
		if wm.ClusterID == nil || wm.Importance == nil {
			t.Errorf("chunk %s written without cluster id / importance", c.ExternalID)
		}
		if wm.Importance != nil && (*wm.Importance < 0 || *wm.Importance > 1) {
			t.Errorf("chunk %s importance %v out of [0,1]", c.ExternalID, *wm.Importance)
		}
	}
	// WriteBatch is 4, so 10 chunks write in 3 batches (4 + 4 + 2).
	if store.batches != 3 {
		t.Errorf("wrote in %d batches, want 3 (WriteBatch 4 over 10 chunks)", store.batches)
	}
	if st.Chunks != 10 {
		t.Errorf("stats chunks = %d, want 10", st.Chunks)
	}
	if st.Clusters != 2 {
		t.Errorf("stats clusters = %d, want 2 (two well-separated blobs)", st.Clusters)
	}
}

func TestClusterCorpusEmptyCorpusWritesNothing(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	st, err := clusterCorpus(t.Context(), discardLogger(), store, testClusterConfig())
	if err != nil {
		t.Fatalf("clusterCorpus: %v", err)
	}
	if len(store.written) != 0 || store.batches != 0 {
		t.Errorf("empty corpus wrote %d chunks in %d batches, want 0/0", len(store.written), store.batches)
	}
	if st.Chunks != 0 {
		t.Errorf("stats chunks = %d, want 0", st.Chunks)
	}
}

func TestClusterCorpusIsDeterministicAcrossRuns(t *testing.T) {
	t.Parallel()
	build := func() *fakeStore {
		chunks := make([]domain.EvidenceChunk, 0, 10)
		for i := range 5 {
			chunks = append(chunks, embeddedChunk(int64(i+1), []float32{5, 1}))
		}
		for i := range 5 {
			chunks = append(chunks, embeddedChunk(int64(i+6), []float32{1, 5}))
		}
		return &fakeStore{chunks: chunks}
	}

	first := build()
	if _, err := clusterCorpus(t.Context(), discardLogger(), first, testClusterConfig()); err != nil {
		t.Fatalf("clusterCorpus (first): %v", err)
	}
	second := build()
	if _, err := clusterCorpus(t.Context(), discardLogger(), second, testClusterConfig()); err != nil {
		t.Fatalf("clusterCorpus (second): %v", err)
	}
	byPage := func(s *fakeStore) map[string]domain.WikiMetadata {
		m := map[string]domain.WikiMetadata{}
		for _, c := range s.written {
			wm, err := domain.ParseWikiMetadata(c.Metadata)
			if err != nil {
				t.Fatalf("chunk %s metadata parse: %v", c.ExternalID, err)
			}
			m[c.ExternalID] = wm
		}
		return m
	}
	a, b := byPage(first), byPage(second)
	for page, ca := range a {
		cb := b[page]
		if *ca.ClusterID != *cb.ClusterID || *ca.Importance != *cb.Importance {
			t.Fatalf("page %s non-deterministic: (%d,%v) vs (%d,%v)", page, *ca.ClusterID, *ca.Importance, *cb.ClusterID, *cb.Importance)
		}
	}
}
