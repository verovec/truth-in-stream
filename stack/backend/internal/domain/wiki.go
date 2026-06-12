package domain

import "time"

// WikiChunkKind classifies a chunk by the article region it was extracted
// from. Ingestion extracts only lead sections today, so every chunk is
// WikiChunkKindLead; the type and the wiki_chunks.kind column exist so later
// body extraction and confidence weighting can tell a lead from body prose.
type WikiChunkKind string

// WikiChunkKindLead tags a chunk from an article's lead section; WikiChunkKindBody
// tags one from a later body section.
const (
	WikiChunkKindLead WikiChunkKind = "lead"
	WikiChunkKindBody WikiChunkKind = "body"
)

// Valid reports whether k is a known chunk kind. The wiki_chunks.kind column is
// plain text (no CHECK, so the runtime staging table stays byte-identical under
// LIKE INCLUDING DEFAULTS), so the store guards writes with this instead.
func (k WikiChunkKind) Valid() bool {
	switch k {
	case WikiChunkKindLead, WikiChunkKindBody:
		return true
	default:
		return false
	}
}

// WikiChunk is one embedded-or-pending chunk of a Wikipedia article's lead
// section. (PageID, ChunkIndex) within a corpus identifies it; RevisionID is
// the article revision the text came from and drives the later delta sync.
// Section is the heading the chunk's text sits under ("" for the lead, which
// has none) and Kind is its coarse classification; together they let
// downstream retrieval and confidence scoring reason about what a chunk is.
// Embedding is nil at ingest and filled by the bulk-embedding pipeline, which
// reads the stored Content, embeds it, and loads the completed chunk. ClusterID
// and Importance are nil until the offline clustering job has run over the
// embedded corpus: ClusterID is the chunk's topic cluster and Importance is a
// [0,1] score the producer maps onto embedding-job priority (absent it falls
// back to the kind heuristic).
type WikiChunk struct {
	PageID     int64
	ChunkIndex int
	Title      string
	URL        string
	RevisionID int64
	Corpus     string
	Content    string
	Section    string
	Kind       WikiChunkKind
	Embedding  []float32
	ClusterID  *int32
	Importance *float64
}

// WikiEvidence is a retrieval hit from the Wikipedia corpus: a chunk's article
// attribution and excerpt with its cosine distance to the query in [0, 2],
// lower is more similar. Wikipedia content is supporting evidence, never a
// verdict, so this type carries no verdict.
type WikiEvidence struct {
	Title    string
	URL      string
	Content  string
	Section  string
	Kind     WikiChunkKind
	Distance float32
}

// WikiTrim marks the chunks of a page that a sync run did not (re)produce:
// every chunk of the page with index >= FromIndex is stale and must go.
// FromIndex 0 removes the page entirely.
type WikiTrim struct {
	PageID    int64
	FromIndex int
}

// WikiCursor is a keyset position in (page_id, chunk_index) order over
// wiki_chunks. The zero value sorts before every real chunk - Wikipedia page
// ids are positive - so it means "from the beginning".
type WikiCursor struct {
	PageID     int64
	ChunkIndex int32
}

// WikiRemaining counts the chunks still to embed beyond a cursor: distinct
// pages, chunks, and total content characters (the token-estimate input).
type WikiRemaining struct {
	Pages  int64
	Chunks int64
	Chars  int64
}

// WikiCorpusHealth is the live corpus snapshot the reingest verifier checks: the
// total chunk count, the counts of rows that would make the corpus unservable
// (a missing or zero-vector embedding, an absent kind metadata), the embedding
// column's declared type (proving the dimension), and whether the HNSW index
// exists and is valid. A healthy corpus has Chunks > 0, the three bad-row counts
// at zero, EmbeddingType "halfvec(1024)", and HNSWPresent and HNSWValid both true.
type WikiCorpusHealth struct {
	Chunks          int64
	NullEmbeddings  int64
	ZeroVectors     int64
	MissingMetadata int64
	EmbeddingType   string
	HNSWPresent     bool
	HNSWValid       bool
}

// WikiSyncState is the per-corpus ingestion checkpoint. LastChangeTS is the
// dump's publication time after a bulk run - an upper bound on the data
// snapshot cut, which happens days earlier, so the delta sync must resume
// with an overlap window rather than from this instant. DumpVersion records
// which dump produced the corpus. Both are zero until the first bulk run
// completes.
type WikiSyncState struct {
	Corpus       string
	LastChangeTS time.Time
	DumpVersion  string
}
