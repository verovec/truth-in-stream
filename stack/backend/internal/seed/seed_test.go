package seed

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

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
	want := []domain.WikiChunk{
		{PageID: 1, ChunkIndex: 0, Title: "Earth", URL: "https://simple.wikipedia.org/wiki/Earth", RevisionID: 100, Corpus: "simplewiki", Content: "The Earth is the third planet from the Sun."},
		{PageID: 1, ChunkIndex: 1, Title: "Earth", URL: "https://simple.wikipedia.org/wiki/Earth", RevisionID: 100, Corpus: "simplewiki", Content: "It is the only known planet with life."},
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

const validDemoJSON = `{
  "source": "common-myths.mp4",
  "segments": [
    {"start_ms": 0, "end_ms": 4000, "content": "Lightning never strikes twice.", "skip_reason": "", "matches": [
      {"kind": "claim", "claim": "Lightning can strike the same place repeatedly.", "verdict": "contradicts", "sources": [{"title": "NWS", "url": "https://weather.gov"}], "similarity": 0.82}
    ]},
    {"start_ms": 4000, "end_ms": 7000, "content": "Anyway, moving on.", "skip_reason": "not_a_claim", "matches": []}
  ]
}`

func TestLoadDemoResultsValid(t *testing.T) {
	t.Parallel()
	got, err := LoadDemoResults(strings.NewReader(validDemoJSON))
	if err != nil {
		t.Fatalf("LoadDemoResults: %v", err)
	}
	if got.Source != "common-myths.mp4" {
		t.Errorf("source = %q, want common-myths.mp4", got.Source)
	}
	want := []domain.SegmentResult{
		{
			Segment: domain.Segment{Start: 0, End: 4 * time.Second, Text: "Lightning never strikes twice."},
			Matches: []domain.SegmentMatch{{
				Kind:       domain.MatchKindClaim,
				Claim:      "Lightning can strike the same place repeatedly.",
				Verdict:    domain.VerdictContradicts,
				Sources:    []domain.Source{{Title: "NWS", URL: "https://weather.gov"}},
				Similarity: 0.82,
			}},
			SkipReason: domain.SkipReasonNone,
		},
		{
			Segment:    domain.Segment{Start: 4 * time.Second, End: 7 * time.Second, Text: "Anyway, moving on."},
			Matches:    []domain.SegmentMatch{},
			SkipReason: domain.SkipReasonNotAClaim,
		},
	}
	if diff := cmp.Diff(want, got.Segments); diff != "" {
		t.Errorf("segments mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadDemoResultsRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty source", in: `{"source":"","segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"","matches":[]}]}`},
		{name: "no segments", in: `{"source":"x.mp4","segments":[]}`},
		{name: "unknown field", in: `{"source":"x.mp4","bogus":1,"segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"","matches":[]}]}`},
		{name: "end before start", in: `{"source":"x.mp4","segments":[{"start_ms":5,"end_ms":1,"content":"c","skip_reason":"","matches":[]}]}`},
		{name: "negative start", in: `{"source":"x.mp4","segments":[{"start_ms":-1,"end_ms":1,"content":"c","skip_reason":"","matches":[]}]}`},
		{name: "empty content", in: `{"source":"x.mp4","segments":[{"start_ms":0,"end_ms":1,"content":"","skip_reason":"","matches":[]}]}`},
		{name: "invalid skip reason", in: `{"source":"x.mp4","segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"nope","matches":[]}]}`},
		{name: "skip with matches", in: `{"source":"x.mp4","segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"not_a_claim","matches":[{"kind":"claim","claim":"x","verdict":"unclear","sources":[],"similarity":0.5}]}]}`},
		{name: "invalid match kind", in: `{"source":"x.mp4","segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"","matches":[{"kind":"bogus","claim":"x","sources":[],"similarity":0.5}]}]}`},
		{name: "claim without verdict", in: `{"source":"x.mp4","segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"","matches":[{"kind":"claim","claim":"x","sources":[],"similarity":0.5}]}]}`},
		{name: "evidence without article", in: `{"source":"x.mp4","segments":[{"start_ms":0,"end_ms":1,"content":"c","skip_reason":"","matches":[{"kind":"evidence","claim":"x","sources":[],"similarity":0.5}]}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := LoadDemoResults(strings.NewReader(tc.in)); err == nil {
				t.Errorf("LoadDemoResults(%s): want error, got nil", tc.name)
			}
		})
	}
}
