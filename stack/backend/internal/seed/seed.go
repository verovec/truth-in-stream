// Package seed loads the local-development fixtures - curated claims, a small
// Wikipedia evidence subset, and the curated sample videos - into the store,
// reading embeddings from a committed cache so a full reseed needs no external
// API key. It wires stores directly (the cmd -> store layer) and holds no HTTP
// types.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// Embedder embeds fixture text for storage. The cached embedder over the
// committed cache satisfies it offline; a real Voyage client backs a refresh.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// WikiStore is the slice of the wiki corpus store the wiki seed needs: claim
// the corpus, insert the chunks, then fill their embeddings.
type WikiStore interface {
	EnsureCorpus(ctx context.Context, corpus string) error
	UpsertChunks(ctx context.Context, chunks []domain.WikiChunk) error
	SetChunkEmbeddings(ctx context.Context, chunks []domain.WikiChunk) error
}

type wikiChunkFile struct {
	PageID     int64  `json:"page_id"`
	ChunkIndex int    `json:"chunk_index"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	RevisionID int64  `json:"revision_id"`
	Corpus     string `json:"corpus"`
	Content    string `json:"content"`
}

// LoadWikiChunks decodes and validates the Wikipedia subset fixture: a JSON
// array of chunks that all share one corpus and have unique (page_id,
// chunk_index) keys. Embeddings are filled at seed time, not carried here.
func LoadWikiChunks(r io.Reader) ([]domain.WikiChunk, error) {
	var files []wikiChunkFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&files); err != nil {
		return nil, fmt.Errorf("seed: decode wiki chunks: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("seed: wiki chunks: fixture is empty")
	}

	corpus := files[0].Corpus
	seen := make(map[[2]int64]struct{}, len(files))
	chunks := make([]domain.WikiChunk, len(files))
	for i, f := range files {
		switch {
		case f.Title == "":
			return nil, fmt.Errorf("seed: wiki chunk %d: empty title", i)
		case f.URL == "":
			return nil, fmt.Errorf("seed: wiki chunk %d: empty url", i)
		case f.Content == "":
			return nil, fmt.Errorf("seed: wiki chunk %d: empty content", i)
		case f.Corpus == "":
			return nil, fmt.Errorf("seed: wiki chunk %d: empty corpus", i)
		case f.Corpus != corpus:
			return nil, fmt.Errorf("seed: wiki chunk %d: corpus %q differs from %q; the fixture is single-corpus", i, f.Corpus, corpus)
		case f.ChunkIndex < 0:
			return nil, fmt.Errorf("seed: wiki chunk %d: negative chunk index %d", i, f.ChunkIndex)
		case f.RevisionID < 1:
			return nil, fmt.Errorf("seed: wiki chunk %d: revision id must be positive", i)
		}
		key := [2]int64{f.PageID, int64(f.ChunkIndex)}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("seed: wiki chunk %d: duplicate (page %d, chunk %d)", i, f.PageID, f.ChunkIndex)
		}
		seen[key] = struct{}{}
		chunks[i] = domain.WikiChunk{
			PageID:     f.PageID,
			ChunkIndex: f.ChunkIndex,
			Title:      f.Title,
			URL:        f.URL,
			RevisionID: f.RevisionID,
			Corpus:     f.Corpus,
			Content:    f.Content,
			// Seed fixtures are lead-section chunks, like live ingestion.
			Kind: domain.WikiChunkKindLead,
		}
	}
	return chunks, nil
}

// InsertWikiChunks claims the corpus, inserts the chunks, embeds their content
// through embedder, and writes the embeddings back, leaving a searchable corpus.
// It is idempotent: chunks upsert by (page_id, chunk_index).
func InsertWikiChunks(ctx context.Context, store WikiStore, embedder Embedder, chunks []domain.WikiChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := store.EnsureCorpus(ctx, chunks[0].Corpus); err != nil {
		return fmt.Errorf("seed: wiki chunks: %w", err)
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		return fmt.Errorf("seed: wiki chunks: %w", err)
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}
	embeddings, err := embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return fmt.Errorf("seed: wiki chunks: embed: %w", err)
	}
	if len(embeddings) != len(chunks) {
		return fmt.Errorf("seed: wiki chunks: got %d embeddings, want %d", len(embeddings), len(chunks))
	}

	embedded := make([]domain.WikiChunk, len(chunks))
	for i, c := range chunks {
		c.Embedding = embeddings[i]
		embedded[i] = c
	}
	if err := store.SetChunkEmbeddings(ctx, embedded); err != nil {
		return fmt.Errorf("seed: wiki chunks: set embeddings: %w", err)
	}
	return nil
}
