package domain

import "time"

// WikiChunk is one embedded-or-pending chunk of a Wikipedia article's lead
// section. (PageID, ChunkIndex) within a corpus identifies it; RevisionID is
// the article revision the text came from and drives the later delta sync.
// Embedding is nil at ingest and filled by the bulk-embedding pipeline, which
// reads the stored Content, embeds it, and loads the completed chunk.
type WikiChunk struct {
	PageID     int64
	ChunkIndex int
	Title      string
	URL        string
	RevisionID int64
	Corpus     string
	Content    string
	Embedding  []float32
}

// WikiEvidence is a retrieval hit from the Wikipedia corpus: a chunk's article
// attribution and excerpt with its cosine distance to the query in [0, 2],
// lower is more similar. Wikipedia content is supporting evidence, never a
// verdict, so this type carries no verdict.
type WikiEvidence struct {
	Title    string
	URL      string
	Content  string
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
