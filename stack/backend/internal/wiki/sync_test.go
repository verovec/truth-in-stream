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

// fakeStore models the staging-build store the bulk ingest drives: ResetStaging
// clears the staged corpus, UpsertStagingChunks fills it, and MarkStagingReady
// records the version it was stamped ready for.
type fakeStore struct {
	mu            sync.Mutex
	corpus        string
	chunks        map[[2]int64]domain.WikiChunk
	resetVersion  string
	resets        int
	readyVersion  string
	carriedReturn int64
	upsertErr     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{chunks: make(map[[2]int64]domain.WikiChunk)}
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

func (f *fakeStore) ResetStaging(_ context.Context, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = make(map[[2]int64]domain.WikiChunk)
	f.resetVersion = version
	f.resets++
	return nil
}

func (f *fakeStore) UpsertStagingChunks(_ context.Context, chunks []domain.WikiChunk) error {
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

func (f *fakeStore) CarryForwardEmbeddings(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.carriedReturn, nil
}

func (f *fakeStore) MarkStagingReady(_ context.Context, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readyVersion = version
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
	store.carriedReturn = 7
	stats, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki")
	if err != nil {
		t.Fatalf("RunBulk: %v", err)
	}

	// Fixture: Paris (article), The City of Light (redirect), Mercury
	// (disambiguation), Talk:Paris (ns 1). Only Paris is staged.
	if len(store.chunks) == 0 {
		t.Fatal("no chunks staged")
	}
	for key, c := range store.chunks {
		if key[0] != 1 {
			t.Errorf("staged chunk for page %d (%q); only Paris (1) should be staged", key[0], c.Title)
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
	// Ingestion extracts only lead sections, so every staged chunk is tagged
	// as the lead: the section heading is empty (the lead has none) and the
	// kind is WikiChunkKindLead.
	for key, c := range store.chunks {
		if c.Section != "" {
			t.Errorf("chunk (%d, %d) section = %q, want empty for a lead chunk", key[0], key[1], c.Section)
		}
		if c.Kind != domain.WikiChunkKindLead {
			t.Errorf("chunk (%d, %d) kind = %q, want %q", key[0], key[1], c.Kind, domain.WikiChunkKindLead)
		}
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
	if stats.Carried != 7 {
		t.Errorf("stats.Carried = %d, want 7", stats.Carried)
	}

	// Staging is reset for the dump version and stamped ready once built.
	if store.resetVersion != "Mon, 01 Jun 2026 03:14:00 GMT" {
		t.Errorf("reset version = %q", store.resetVersion)
	}
	if store.readyVersion != "Mon, 01 Jun 2026 03:14:00 GMT" {
		t.Errorf("ready version = %q, want the dump version", store.readyVersion)
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
	if store.resets != 2 {
		t.Errorf("staging reset %d times across two runs, want 2", store.resets)
	}
}

func TestRunBulkResetClearsPriorStaging(t *testing.T) {
	t.Parallel()

	// Page 2 is a redirect in the fixture. A stale chunk left in staging by an
	// earlier run (where it was an article) must not survive: ResetStaging wipes
	// staging before the rebuild, so a page absent from the dump leaves no rows.
	store := newFakeStore()
	store.chunks[[2]int64{2, 0}] = domain.WikiChunk{PageID: 2, ChunkIndex: 0, Content: "stale"}
	store.chunks[[2]int64{2, 1}] = domain.WikiChunk{PageID: 2, ChunkIndex: 1, Content: "stale"}

	if _, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki"); err != nil {
		t.Fatalf("RunBulk: %v", err)
	}

	for key := range store.chunks {
		if key[0] == 2 {
			t.Errorf("stale chunk (%d, %d) of redirect page survived the rebuild", key[0], key[1])
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
		t.Errorf("chunks were staged despite the corpus guard")
	}
}

func TestRunBulkEmptyIndexDoesNotMarkReady(t *testing.T) {
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
	if store.readyVersion != "" {
		t.Errorf("staging marked ready despite an empty run")
	}
}

func TestRunBulkPropagatesStoreError(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.upsertErr = errors.New("disk full")

	if _, err := RunBulk(t.Context(), store, fixtureFiles(), "simplewiki"); !errors.Is(err, store.upsertErr) {
		t.Fatalf("RunBulk error = %v, want wrapped %v", err, store.upsertErr)
	}
	if store.readyVersion != "" {
		t.Errorf("staging marked ready despite a failed run")
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

// fakeLiveStore models the live wiki_chunks table the bulk-into-live ingest
// upserts into and trims, recording chunks by identity and the trims requested.
type fakeLiveStore struct {
	mu        sync.Mutex
	corpus    string
	chunks    map[[2]int64]domain.WikiChunk
	trims     map[int64]int
	upsertErr error
}

func newFakeLiveStore() *fakeLiveStore {
	return &fakeLiveStore{chunks: make(map[[2]int64]domain.WikiChunk), trims: make(map[int64]int)}
}

func (f *fakeLiveStore) EnsureCorpus(_ context.Context, corpus string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.corpus != "" && f.corpus != corpus {
		return fmt.Errorf("store already holds corpus %q", f.corpus)
	}
	f.corpus = corpus
	return nil
}

func (f *fakeLiveStore) UpsertChunks(_ context.Context, chunks []domain.WikiChunk) error {
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

func (f *fakeLiveStore) TrimPages(_ context.Context, trims []domain.WikiTrim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tr := range trims {
		f.trims[tr.PageID] = tr.FromIndex
	}
	return nil
}

func TestRunBulkLive(t *testing.T) {
	t.Parallel()
	store := newFakeLiveStore()
	stats, err := RunBulkLive(t.Context(), store, fixtureFiles(), "simplewiki")
	if err != nil {
		t.Fatalf("RunBulkLive: %v", err)
	}

	// Fixture: only Paris (page 1) is an ingestable article; the rest are skipped.
	if stats.PagesStored != 1 || stats.PagesSkipped != 3 {
		t.Errorf("stats = %+v, want 1 stored, 3 skipped", stats)
	}
	if len(store.chunks) == 0 {
		t.Fatal("no chunks upserted into the live corpus")
	}
	for key, c := range store.chunks {
		if key[0] != 1 {
			t.Errorf("upserted chunk for page %d (%q); only Paris (1) should be ingested", key[0], c.Title)
		}
	}
	first, ok := store.chunks[[2]int64{1, 0}]
	if !ok || first.Title != "Paris" || first.Corpus != "simplewiki" {
		t.Fatalf("missing or wrong chunk (1,0): %+v", first)
	}
	// The page's stale tail is trimmed from its new chunk count, so a shrunk lead
	// cannot leave orphaned higher-index chunks in the live table.
	if got := store.trims[1]; got != stats.Chunks {
		t.Errorf("trim from-index for Paris = %d, want its chunk count %d", got, stats.Chunks)
	}
}

func TestRunBulkLivePropagatesUpsertError(t *testing.T) {
	t.Parallel()
	store := newFakeLiveStore()
	store.upsertErr = errors.New("db down")
	if _, err := RunBulkLive(t.Context(), store, fixtureFiles(), "simplewiki"); !errors.Is(err, store.upsertErr) {
		t.Fatalf("RunBulkLive error = %v, want wrapped %v", err, store.upsertErr)
	}
}

func TestRunBulkLiveRefusesForeignCorpus(t *testing.T) {
	t.Parallel()
	store := newFakeLiveStore()
	store.corpus = "simplewiki"
	if _, err := RunBulkLive(t.Context(), store, fixtureFiles(), "enwiki"); err == nil {
		t.Fatal("RunBulkLive ingested a second corpus into a single-corpus store, want error")
	}
}
