package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// conflictedWikiStore refuses the corpus claim the way a store already holding
// another encyclopedic source does.
type conflictedWikiStore struct{}

func (conflictedWikiStore) EnsureSource(context.Context, string) error {
	return fmt.Errorf("postgres: ensure evidence source: %w", domain.ErrEvidenceSourceConflict)
}
func (conflictedWikiStore) UpsertChunks(context.Context, []domain.EvidenceChunk) error {
	return errors.New("unreachable: claim was refused")
}
func (conflictedWikiStore) SetChunkEmbeddings(context.Context, []domain.EvidenceChunk) error {
	return errors.New("unreachable: claim was refused")
}

// TestInsertWikiChunksSurfacesSourceConflict pins the contract the seed command
// relies on to skip the wiki fixture instead of failing the whole seed when the
// store already holds a real corpus: the claim refusal must stay identifiable
// through the wrapping.
func TestInsertWikiChunksSurfacesSourceConflict(t *testing.T) {
	chunks, err := LoadWikiChunks(strings.NewReader(validWikiJSON))
	if err != nil {
		t.Fatalf("LoadWikiChunks: %v", err)
	}
	err = InsertWikiChunks(t.Context(), conflictedWikiStore{}, nil, chunks)
	if !errors.Is(err, domain.ErrEvidenceSourceConflict) {
		t.Fatalf("InsertWikiChunks: err = %v, want ErrEvidenceSourceConflict", err)
	}
}

const validWikiJSON = `[
  {"page_id": 1, "chunk_index": 0, "title": "Earth", "url": "https://simple.wikipedia.org/wiki/Earth", "revision_id": 100, "corpus": "simplewiki", "content": "The Earth is the third planet from the Sun."},
  {"page_id": 1, "chunk_index": 1, "title": "Earth", "url": "https://simple.wikipedia.org/wiki/Earth", "revision_id": 100, "corpus": "simplewiki", "content": "It is the only known planet with life."}
]`

func TestLoadWikiChunksValid(t *testing.T) {
	t.Parallel()
	chunks, err := LoadWikiChunks(strings.NewReader(validWikiJSON))
	if err != nil {
		t.Fatalf("LoadWikiChunks: %v", err)
	}
	want := []domain.EvidenceChunk{
		{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0, Title: "Earth", URL: "https://simple.wikipedia.org/wiki/Earth", Content: "The Earth is the third planet from the Sun.", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{RevisionID: 100}.Map()},
		{Source: "simplewiki", ExternalID: "1", ChunkIndex: 1, Title: "Earth", URL: "https://simple.wikipedia.org/wiki/Earth", Content: "It is the only known planet with life.", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{RevisionID: 100}.Map()},
	}
	if diff := cmp.Diff(want, chunks); diff != "" {
		t.Errorf("chunks mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadWikiChunksRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty array", in: `[]`},
		{name: "unknown field", in: `[{"page_id":1,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"simplewiki","content":"c","bogus":1}]`},
		{name: "empty title", in: `[{"page_id":1,"chunk_index":0,"title":"","url":"u","revision_id":1,"corpus":"simplewiki","content":"c"}]`},
		{name: "empty url", in: `[{"page_id":1,"chunk_index":0,"title":"E","url":"","revision_id":1,"corpus":"simplewiki","content":"c"}]`},
		{name: "empty content", in: `[{"page_id":1,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"simplewiki","content":""}]`},
		{name: "empty corpus", in: `[{"page_id":1,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"","content":"c"}]`},
		{name: "negative chunk index", in: `[{"page_id":1,"chunk_index":-1,"title":"E","url":"u","revision_id":1,"corpus":"simplewiki","content":"c"}]`},
		{name: "duplicate key", in: `[{"page_id":1,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"simplewiki","content":"a"},{"page_id":1,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"simplewiki","content":"b"}]`},
		{name: "mixed corpus", in: `[{"page_id":1,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"simplewiki","content":"a"},{"page_id":2,"chunk_index":0,"title":"E","url":"u","revision_id":1,"corpus":"enwiki","content":"b"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadWikiChunks(strings.NewReader(tc.in)); err == nil {
				t.Errorf("LoadWikiChunks(%s): want error, got nil", tc.name)
			}
		})
	}
}
