package domain

import "time"

// WikiChunk is one embedded-or-pending chunk of a Wikipedia article's lead
// section. (PageID, ChunkIndex) within a corpus identifies it; RevisionID is
// the article revision the text came from and drives the later delta sync.
// The embedding is filled by the bulk-embedding pipeline, not at ingest.
type WikiChunk struct {
	PageID     int64
	ChunkIndex int
	Title      string
	URL        string
	RevisionID int64
	Corpus     string
	Content    string
}

// WikiTrim marks the chunks of a page that a sync run did not (re)produce:
// every chunk of the page with index >= FromIndex is stale and must go.
// FromIndex 0 removes the page entirely.
type WikiTrim struct {
	PageID    int64
	FromIndex int
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
