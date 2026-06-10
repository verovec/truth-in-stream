package wiki

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeStore struct {
	mu        sync.Mutex
	corpus    string
	chunks    map[[2]int64]domain.WikiChunk
	trims     map[int64]int
	states    []domain.WikiSyncState
	upsertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{chunks: make(map[[2]int64]domain.WikiChunk), trims: make(map[int64]int)}
}

func (f *fakeStore) EnsureCorpus(_ context.Context, corpus string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.corpus != "" && f.corpus != corpus {
		return fmt.Errorf("store already holds corpus %q", f.corpus)
	}
	f.corpus = corpus
	return nil
}

func (f *fakeStore) UpsertChunks(_ context.Context, chunks []domain.WikiChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	for _, c := range chunks {
		f.chunks[[2]int64{c.PageID, int64(c.ChunkIndex)}] = c
	}
	return nil
}

func (f *fakeStore) TrimPages(_ context.Context, trims []domain.WikiTrim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tr := range trims {
		f.trims[tr.PageID] = tr.FromIndex
		for key := range f.chunks {
			if key[0] == tr.PageID && key[1] >= int64(tr.FromIndex) {
				delete(f.chunks, key)
			}
		}
	}
	return nil
}

func (f *fakeStore) SetSyncState(_ context.Context, st domain.WikiSyncState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = append(f.states, st)
	return nil
}

func fixtureFiles() DumpFiles {
	return DumpFiles{
		DumpPath:  "testdata/fixture-multistream.xml.bz2",
		IndexPath: "testdata/fixture-multistream-index.txt.bz2",
		Version:   "Mon, 01 Jun 2026 03:14:00 GMT",
	}
}

func TestRunBulk(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	stats, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki")
	if err != nil {
		t.Fatalf("RunBulk: %v", err)
	}

	// Fixture: Paris (article), The City of Light (redirect), Mercury
	// (disambiguation), Talk:Paris (ns 1). Only Paris is stored.
	if len(store.chunks) == 0 {
		t.Fatal("no chunks stored")
	}
	for key, c := range store.chunks {
		if key[0] != 1 {
			t.Errorf("stored chunk for page %d (%q); only Paris (1) should be stored", key[0], c.Title)
		}
	}

	first, ok := store.chunks[[2]int64{1, 0}]
	if !ok {
		t.Fatal("missing chunk (1, 0) for Paris")
	}
	if first.Title != "Paris" || first.Corpus != "simplewiki" || first.RevisionID != 100 {
		t.Errorf("chunk metadata wrong: %+v", first)
	}
	if first.URL != "https://simple.wikipedia.org/wiki/Paris" {
		t.Errorf("chunk URL = %q", first.URL)
	}
	if !strings.HasPrefix(first.Content, "Paris\n\n") {
		t.Errorf("chunk content missing title prefix: %q", first.Content)
	}
	for _, frag := range []string{"[[", "]]", "'''", "<ref", "== History ==", "must not reach"} {
		if strings.Contains(first.Content, frag) {
			t.Errorf("chunk content contains %q: %q", frag, first.Content)
		}
	}
	if !strings.Contains(first.Content, "capital of France") {
		t.Errorf("chunk content lost the lead text: %q", first.Content)
	}

	if stats.PagesStored != 1 || stats.PagesSkipped != 3 {
		t.Errorf("stats = %+v, want 1 stored, 3 skipped", stats)
	}
	if stats.Chunks != len(store.chunks) {
		t.Errorf("stats.Chunks = %d, store has %d", stats.Chunks, len(store.chunks))
	}

	// Every page seen is trimmed so a re-run converges: stored pages from
	// their new chunk count, skipped pages from 0.
	wantTrims := map[int64]int{1: stats.Chunks, 2: 0, 3: 0, 4: 0}
	for pageID, want := range wantTrims {
		got, ok := store.trims[pageID]
		if !ok {
			t.Errorf("page %d never trimmed", pageID)
			continue
		}
		if got != want {
			t.Errorf("page %d trimmed from %d, want %d", pageID, got, want)
		}
	}

	if len(store.states) != 1 {
		t.Fatalf("sync state written %d times, want 1", len(store.states))
	}
	st := store.states[0]
	if st.Corpus != "simplewiki" || st.DumpVersion != "Mon, 01 Jun 2026 03:14:00 GMT" {
		t.Errorf("sync state = %+v", st)
	}
	if st.LastChangeTS.IsZero() {
		t.Errorf("sync state LastChangeTS not derived from the dump version")
	}
}

func TestRunBulkIdempotent(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	for range 2 {
		if _, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki"); err != nil {
			t.Fatalf("RunBulk: %v", err)
		}
	}

	for key := range store.chunks {
		if key[0] != 1 {
			t.Errorf("unexpected page %d after re-run", key[0])
		}
	}
	if len(store.states) != 2 {
		t.Errorf("sync state written %d times across two runs, want 2", len(store.states))
	}
}

func TestRunBulkRemovesStaleChunksOfSkippedPages(t *testing.T) {
	t.Parallel()

	// Page 2 is a redirect in the fixture. Chunks left over from a run
	// against an older dump (where it was an article) must be removed.
	store := newFakeStore()
	store.chunks[[2]int64{2, 0}] = domain.WikiChunk{PageID: 2, ChunkIndex: 0, Content: "stale"}
	store.chunks[[2]int64{2, 1}] = domain.WikiChunk{PageID: 2, ChunkIndex: 1, Content: "stale"}

	if _, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki"); err != nil {
		t.Fatalf("RunBulk: %v", err)
	}

	for key := range store.chunks {
		if key[0] == 2 {
			t.Errorf("stale chunk (%d, %d) of redirect page survived the re-run", key[0], key[1])
		}
	}
}

func TestRunBulkRefusesForeignCorpus(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.corpus = "simplewiki"

	if _, err := RunBulk(t.Context(), store, fixtureFiles(), "enwiki"); err == nil {
		t.Fatal("RunBulk ingested a second corpus into a single-corpus store, want error")
	}
	if len(store.chunks) != 0 {
		t.Errorf("chunks were written despite the corpus guard")
	}
}

func TestRunBulkEmptyIndexDoesNotCheckpoint(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	files := DumpFiles{
		DumpPath:  "testdata/fixture-multistream.xml.bz2",
		IndexPath: "testdata/fixture-empty-index.txt.bz2",
		Version:   "Mon, 01 Jun 2026 03:14:00 GMT",
	}

	if _, err := RunBulk(t.Context(), store, files, "simplewiki"); err == nil {
		t.Fatal("RunBulk succeeded over an empty index, want error")
	}
	if len(store.states) != 0 {
		t.Errorf("sync state checkpointed despite an empty run")
	}
}

func TestRunBulkPropagatesStoreError(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.upsertErr = errors.New("disk full")

	if _, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki"); !errors.Is(err, store.upsertErr) {
		t.Fatalf("RunBulk error = %v, want wrapped %v", err, store.upsertErr)
	}
	if len(store.states) != 0 {
		t.Errorf("sync state checkpointed despite a failed run")
	}
}

func TestRunBulkMissingFiles(t *testing.T) {
	t.Parallel()

	files := DumpFiles{DumpPath: "testdata/nope.bz2", IndexPath: "testdata/nope-index.bz2"}
	if _, err := RunBulk(t.Context(), newFakeStore(), files, "simplewiki"); err == nil {
		t.Fatal("RunBulk succeeded with missing files, want error")
	}
}

func TestPageURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		corpus, title, want string
	}{
		{"simplewiki", "Paris", "https://simple.wikipedia.org/wiki/Paris"},
		{"enwiki", "New York City", "https://en.wikipedia.org/wiki/New_York_City"},
		{"enwiki", "AC/DC", "https://en.wikipedia.org/wiki/AC%2FDC"},
	}
	for _, tc := range tests {
		if got := pageURL(tc.corpus, tc.title); got != tc.want {
			t.Errorf("pageURL(%q, %q) = %q, want %q", tc.corpus, tc.title, got, tc.want)
		}
	}
}
