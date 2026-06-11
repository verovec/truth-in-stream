// Package seed loads the local-development fixtures - curated claims, a small
// Wikipedia evidence subset, and a precomputed demo-video result set - into the
// store, reading embeddings from a committed cache so a full reseed needs no
// external API key. It wires stores directly (the cmd -> store layer) and holds
// no HTTP types.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

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

// DemoResults is the parsed demo-video fixture: the source identifier the
// frontend submits and the precomputed per-segment results.
type DemoResults struct {
	Source   string
	Segments []domain.SegmentResult
}

type demoFile struct {
	Source   string        `json:"source"`
	Segments []demoSegment `json:"segments"`
}

type demoSegment struct {
	StartMs    int64                 `json:"start_ms"`
	EndMs      int64                 `json:"end_ms"`
	Content    string                `json:"content"`
	SkipReason domain.SkipReason     `json:"skip_reason"`
	Matches    []domain.SegmentMatch `json:"matches"`
}

// LoadDemoResults decodes and validates the demo-video result fixture. Each
// segment is either checked (no skip reason, with its matches) or skipped (a
// known reason and no matches); validation mirrors the store invariants so a bad
// fixture is rejected before any insert.
func LoadDemoResults(r io.Reader) (DemoResults, error) {
	var file demoFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return DemoResults{}, fmt.Errorf("seed: decode demo results: %w", err)
	}
	if file.Source == "" {
		return DemoResults{}, fmt.Errorf("seed: demo results: empty source")
	}
	if len(file.Segments) == 0 {
		return DemoResults{}, fmt.Errorf("seed: demo results: no segments")
	}

	segments := make([]domain.SegmentResult, len(file.Segments))
	for i, s := range file.Segments {
		if err := validateDemoSegment(i, s); err != nil {
			return DemoResults{}, err
		}
		matches := s.Matches
		if matches == nil {
			matches = []domain.SegmentMatch{}
		}
		segments[i] = domain.SegmentResult{
			Segment: domain.Segment{
				Start: time.Duration(s.StartMs) * time.Millisecond,
				End:   time.Duration(s.EndMs) * time.Millisecond,
				Text:  s.Content,
			},
			Matches:    matches,
			SkipReason: s.SkipReason,
		}
	}
	return DemoResults{Source: file.Source, Segments: segments}, nil
}

func validateDemoSegment(i int, s demoSegment) error {
	switch {
	case s.StartMs < 0:
		return fmt.Errorf("seed: demo segment %d: negative start %d", i, s.StartMs)
	case s.EndMs < s.StartMs:
		return fmt.Errorf("seed: demo segment %d: end %d before start %d", i, s.EndMs, s.StartMs)
	case s.Content == "":
		return fmt.Errorf("seed: demo segment %d: empty content", i)
	case !s.SkipReason.Valid():
		return fmt.Errorf("seed: demo segment %d: invalid skip reason %q", i, s.SkipReason)
	case s.SkipReason != domain.SkipReasonNone && len(s.Matches) > 0:
		return fmt.Errorf("seed: demo segment %d: skipped segment must carry no matches", i)
	}
	for j, m := range s.Matches {
		if err := validateDemoMatch(i, j, m); err != nil {
			return err
		}
	}
	return nil
}

func validateDemoMatch(seg, idx int, m domain.SegmentMatch) error {
	if !m.Kind.Valid() {
		return fmt.Errorf("seed: demo segment %d match %d: invalid kind %q", seg, idx, m.Kind)
	}
	if m.Claim == "" {
		return fmt.Errorf("seed: demo segment %d match %d: empty claim text", seg, idx)
	}
	if m.Similarity < 0 || m.Similarity > 1 {
		return fmt.Errorf("seed: demo segment %d match %d: similarity %v outside [0,1]", seg, idx, m.Similarity)
	}
	switch m.Kind {
	case domain.MatchKindClaim:
		if !m.Verdict.Valid() {
			return fmt.Errorf("seed: demo segment %d match %d: claim match needs a valid verdict, got %q", seg, idx, m.Verdict)
		}
	case domain.MatchKindEvidence:
		if m.Article == nil || m.Article.Title == "" || m.Article.URL == "" {
			return fmt.Errorf("seed: demo segment %d match %d: evidence match needs an article title and url", seg, idx)
		}
	}
	return nil
}

// InsertDemoResults replaces any prior results for videoID, persists each
// segment, and marks the video processed, so the player and fact-check panel
// serve the demo without running the pipeline. It is idempotent.
func InsertDemoResults(ctx context.Context, store domain.SegmentResultStore, videoID string, segments []domain.SegmentResult) error {
	if videoID == "" {
		return fmt.Errorf("seed: demo results: empty video id")
	}
	if len(segments) == 0 {
		return fmt.Errorf("seed: demo results: no segments")
	}
	if err := store.DeleteSegmentResults(ctx, videoID); err != nil {
		return fmt.Errorf("seed: demo results: %w", err)
	}
	for _, seg := range segments {
		if err := store.SaveSegmentResult(ctx, videoID, seg); err != nil {
			return fmt.Errorf("seed: demo results: save segment at %s: %w", seg.Start, err)
		}
	}
	if err := store.MarkVideoProcessed(ctx, videoID, len(segments)); err != nil {
		return fmt.Errorf("seed: demo results: mark processed: %w", err)
	}
	return nil
}
