package wiki

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// --- fakes ---

type storedChunk struct {
	chunk    domain.WikiChunk
	embedded bool
}

type fakeDeltaStore struct {
	mu              sync.Mutex
	chunks          map[[2]int64]storedChunk
	state           domain.WikiSyncState
	hasState        bool
	syncStates      []domain.WikiSyncState
	embedInProgress bool
	upsertErr       error
	setStateErr     error
}

func newFakeDeltaStore() *fakeDeltaStore {
	return &fakeDeltaStore{chunks: make(map[[2]int64]storedChunk)}
}

func (f *fakeDeltaStore) seed(c domain.WikiChunk, embedded bool) {
	f.chunks[[2]int64{c.PageID, int64(c.ChunkIndex)}] = storedChunk{chunk: c, embedded: embedded}
}

func (f *fakeDeltaStore) GetSyncState(_ context.Context, _ string) (domain.WikiSyncState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.hasState, nil
}

func (f *fakeDeltaStore) EmbedInProgress(_ context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.embedInProgress, nil
}

func (f *fakeDeltaStore) CountPages(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pages := map[int64]struct{}{}
	for key := range f.chunks {
		pages[key[0]] = struct{}{}
	}
	return int64(len(pages)), nil
}

func (f *fakeDeltaStore) StoredRevisions(_ context.Context, pageIDs []int64) (map[int64]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[int64]struct{}{}
	for _, id := range pageIDs {
		want[id] = struct{}{}
	}
	out := map[int64]int64{}
	for key, sc := range f.chunks {
		if _, ok := want[key[0]]; ok && sc.chunk.RevisionID > out[key[0]] {
			out[key[0]] = sc.chunk.RevisionID
		}
	}
	return out, nil
}

func (f *fakeDeltaStore) UnembeddedChunks(_ context.Context, cur domain.WikiCursor, limit int) ([]domain.WikiChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var pending []domain.WikiChunk
	for _, sc := range f.chunks {
		if sc.embedded {
			continue
		}
		c := sc.chunk
		if c.PageID > cur.PageID || (c.PageID == cur.PageID && int32(c.ChunkIndex) > cur.ChunkIndex) {
			pending = append(pending, c)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].PageID != pending[j].PageID {
			return pending[i].PageID < pending[j].PageID
		}
		return pending[i].ChunkIndex < pending[j].ChunkIndex
	})
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func (f *fakeDeltaStore) UpsertChunks(_ context.Context, chunks []domain.WikiChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	for _, c := range chunks {
		key := [2]int64{c.PageID, int64(c.ChunkIndex)}
		embedded := false
		if old, ok := f.chunks[key]; ok && old.chunk.Content == c.Content {
			embedded = old.embedded
		}
		f.chunks[key] = storedChunk{chunk: c, embedded: embedded}
	}
	return nil
}

func (f *fakeDeltaStore) TrimPages(_ context.Context, trims []domain.WikiTrim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tr := range trims {
		for key := range f.chunks {
			if key[0] == tr.PageID && key[1] >= int64(tr.FromIndex) {
				delete(f.chunks, key)
			}
		}
	}
	return nil
}

func (f *fakeDeltaStore) DeletePagesByTitle(_ context.Context, titles []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	drop := map[string]struct{}{}
	for _, t := range titles {
		drop[t] = struct{}{}
	}
	for key, sc := range f.chunks {
		if _, ok := drop[sc.chunk.Title]; ok {
			delete(f.chunks, key)
		}
	}
	return nil
}

func (f *fakeDeltaStore) SetChunkEmbeddings(_ context.Context, chunks []domain.WikiChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range chunks {
		key := [2]int64{c.PageID, int64(c.ChunkIndex)}
		if sc, ok := f.chunks[key]; ok {
			sc.embedded = true
			sc.chunk.Embedding = c.Embedding
			f.chunks[key] = sc
		}
	}
	return nil
}

func (f *fakeDeltaStore) SetSyncState(_ context.Context, st domain.WikiSyncState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setStateErr != nil {
		return f.setStateErr
	}
	f.state = st
	f.hasState = true
	f.syncStates = append(f.syncStates, st)
	return nil
}

func (f *fakeDeltaStore) pageChunks(pageID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for key := range f.chunks {
		if key[0] == pageID {
			n++
		}
	}
	return n
}

type fakeAPI struct {
	changes   []Change
	extracts  map[string]Extract
	requested []string
	rcErr     error
	exErr     error
}

func (f *fakeAPI) RecentChanges(_ context.Context, _ time.Time) ([]Change, error) {
	if f.rcErr != nil {
		return nil, f.rcErr
	}
	return f.changes, nil
}

func (f *fakeAPI) Extracts(_ context.Context, titles []string) ([]Extract, error) {
	f.requested = append(f.requested, titles...)
	if f.exErr != nil {
		return nil, f.exErr
	}
	out := make([]Extract, 0, len(titles))
	for _, t := range titles {
		if ex, ok := f.extracts[t]; ok {
			out = append(out, ex)
		}
	}
	return out, nil
}

type deltaEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (e *deltaEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls += len(texts)
	e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, domain.EmbeddingDim)
	}
	return out, nil
}

// --- fixtures ---

var (
	baseTS = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	nowTS  = baseTS.Add(24 * time.Hour)
)

func deltaCfg() DeltaConfig {
	return DeltaConfig{Corpus: "simplewiki", RetentionDays: 30, BulkFraction: 0.9, BatchSize: 10, Concurrency: 2}
}

func baselineStore(chunks ...domain.WikiChunk) *fakeDeltaStore {
	s := newFakeDeltaStore()
	s.state = domain.WikiSyncState{Corpus: "simplewiki", LastChangeTS: baseTS, DumpVersion: "v1"}
	s.hasState = true
	for _, c := range chunks {
		s.seed(c, true)
	}
	return s
}

// chunk builds a baseline chunk at revision 100; delta runs move pages to a
// higher revision via the extract fixtures.
func chunk(pageID int64, idx int, title, content string) domain.WikiChunk {
	return domain.WikiChunk{PageID: pageID, ChunkIndex: idx, Title: title, Corpus: "simplewiki", RevisionID: 100, Content: content}
}

// --- tests ---

func TestRunDeltaRefetchesEditsAndEmbeds(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nold lead"))
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {PageID: 1, Title: "Paris", RevisionID: 200, Text: "Paris is the capital of France and sits on the Seine."}},
	}
	emb := &deltaEmbedder{}

	stats, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Changed != 1 || stats.Skipped != 0 || stats.Deleted != 0 {
		t.Errorf("stats = %+v, want Changed 1", stats)
	}
	if stats.Embedded == 0 || emb.calls == 0 {
		t.Errorf("expected chunks embedded, got Embedded=%d calls=%d", stats.Embedded, emb.calls)
	}
	got := store.chunks[[2]int64{1, 0}]
	if got.chunk.RevisionID != 200 || !got.embedded {
		t.Errorf("chunk (1,0) = %+v, want revid 200 embedded", got)
	}
	if len(store.syncStates) != 1 || !store.syncStates[0].LastChangeTS.Equal(baseTS.Add(time.Hour)) {
		t.Errorf("checkpoint = %+v, want advanced to the latest change", store.syncStates)
	}
	if store.syncStates[0].DumpVersion != "v1" {
		t.Errorf("delta clobbered dump version: %q", store.syncStates[0].DumpVersion)
	}
}

func TestRunDeltaSkipsUnchangedRevision(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	api := &fakeAPI{
		// Same revision resurfaces in the overlap window.
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 100, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {PageID: 1, Title: "Paris", RevisionID: 100, Text: "changed"}},
	}
	emb := &deltaEmbedder{}

	stats, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Skipped != 1 || stats.Changed != 0 {
		t.Errorf("stats = %+v, want Skipped 1 Changed 0", stats)
	}
	if len(api.requested) != 0 {
		t.Errorf("refetched %v despite an unchanged revision", api.requested)
	}
	if emb.calls != 0 {
		t.Errorf("embedded despite no content change: %d calls", emb.calls)
	}
	// The window is still processed, so the checkpoint advances.
	if len(store.syncStates) != 1 {
		t.Errorf("checkpoint written %d times, want 1", len(store.syncStates))
	}
}

func TestRunDeltaDeletesPages(t *testing.T) {
	t.Parallel()

	store := baselineStore(
		chunk(1, 0, "Paris", "Paris\n\nlead"),
		chunk(2, 0, "Lyon", "Lyon\n\nlead"),
	)
	api := &fakeAPI{changes: []Change{{Title: "Lyon", Timestamp: baseTS.Add(time.Hour), Deleted: true}}}
	emb := &deltaEmbedder{}

	stats, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Deleted != 1 {
		t.Errorf("stats.Deleted = %d, want 1", stats.Deleted)
	}
	if store.pageChunks(2) != 0 {
		t.Errorf("deleted page 2 (Lyon) still has chunks")
	}
	if store.pageChunks(1) != 1 {
		t.Errorf("untouched page 1 (Paris) lost chunks")
	}
	if emb.calls != 0 {
		t.Errorf("a pure-deletion window made %d embed calls", emb.calls)
	}
}

func TestRunDeltaMissingExtractDeletes(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {Title: "Paris", Missing: true}},
	}

	stats, err := RunDelta(t.Context(), store, api, &deltaEmbedder{}, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Deleted != 1 || stats.Changed != 0 {
		t.Errorf("stats = %+v, want Deleted 1 Changed 0", stats)
	}
	if store.pageChunks(1) != 0 {
		t.Errorf("page missing on refetch still has chunks")
	}
}

func TestRunDeltaRedirectTrimsAllChunks(t *testing.T) {
	t.Parallel()

	store := baselineStore(
		chunk(1, 0, "Paris", "Paris\n\nlead one"),
		chunk(1, 1, "Paris", "Paris\n\nlead two"),
	)
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {PageID: 1, Title: "Paris", RevisionID: 200, Text: ""}},
	}
	emb := &deltaEmbedder{}

	if _, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS); err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if store.pageChunks(1) != 0 {
		t.Errorf("page turned redirect kept chunks")
	}
	if emb.calls != 0 {
		t.Errorf("redirect made %d embed calls", emb.calls)
	}
}

func TestRunDeltaRestoreRefetchesNotDeletes(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Phoenix", "Phoenix\n\nold lead"))
	// Deleted then restored within one window: the restore is the latest event,
	// so the live page must be refetched, not left deleted.
	api := &fakeAPI{
		changes: []Change{
			{Title: "Phoenix", Timestamp: baseTS.Add(time.Hour), Deleted: true},
			{PageID: 1, Title: "Phoenix", RevisionID: 0, Timestamp: baseTS.Add(2 * time.Hour)},
		},
		extracts: map[string]Extract{"Phoenix": {PageID: 1, Title: "Phoenix", RevisionID: 250, Text: "Phoenix is a mythical bird that rises from its ashes."}},
	}
	emb := &deltaEmbedder{}

	stats, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Deleted != 0 {
		t.Errorf("restored page was deleted: stats=%+v", stats)
	}
	if store.pageChunks(1) == 0 {
		t.Fatal("restored page has no chunks; it was lost")
	}
	if got := store.chunks[[2]int64{1, 0}]; got.chunk.RevisionID != 250 || !got.embedded {
		t.Errorf("restored chunk = %+v, want revid 250 embedded", got)
	}
}

func TestRunDeltaFallsBackToReportedRevision(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nold lead"))
	// The extract omits a revision id; the runner must fall back to the revision
	// RecentChanges reported (200) rather than storing 0.
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {PageID: 1, Title: "Paris", RevisionID: 0, Text: "Paris is the capital of France."}},
	}

	if _, err := RunDelta(t.Context(), store, api, &deltaEmbedder{}, deltaCfg(), nowTS); err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if got := store.chunks[[2]int64{1, 0}]; got.chunk.RevisionID != 200 {
		t.Errorf("chunk revid = %d, want 200 (fell back to the reported revision)", got.chunk.RevisionID)
	}
}

func TestRunDeltaNoChangesIsNoop(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	api := &fakeAPI{changes: nil}
	emb := &deltaEmbedder{}

	stats, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats != (DeltaStats{}) {
		t.Errorf("stats = %+v, want zero", stats)
	}
	if emb.calls != 0 || len(api.requested) != 0 {
		t.Errorf("no-change run touched the API: embed=%d extracts=%v", emb.calls, api.requested)
	}
	if len(store.syncStates) != 0 {
		t.Errorf("no-change run advanced the checkpoint")
	}
}

func TestRunDeltaRefusesWhileBulkEmbedInProgress(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	store.embedInProgress = true
	api := &fakeAPI{changes: []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}}}
	emb := &deltaEmbedder{}

	if _, err := RunDelta(t.Context(), store, api, emb, deltaCfg(), nowTS); !errors.Is(err, ErrBulkEmbedInProgress) {
		t.Fatalf("err = %v, want ErrBulkEmbedInProgress", err)
	}
	if emb.calls != 0 || len(store.syncStates) != 0 {
		t.Errorf("refused run still did work: embed=%d checkpoints=%d", emb.calls, len(store.syncStates))
	}
}

func TestRunDeltaNoBaseline(t *testing.T) {
	t.Parallel()

	store := newFakeDeltaStore() // never synced
	if _, err := RunDelta(t.Context(), store, &fakeAPI{}, &deltaEmbedder{}, deltaCfg(), nowTS); !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("err = %v, want ErrNoBaseline", err)
	}
}

func TestRunDeltaWindowExceedsRetention(t *testing.T) {
	t.Parallel()

	store := baselineStore()
	store.state.LastChangeTS = nowTS.Add(-40 * 24 * time.Hour)
	api := &fakeAPI{changes: []Change{{PageID: 1, Title: "Paris", RevisionID: 1, Timestamp: nowTS}}}

	if _, err := RunDelta(t.Context(), store, api, &deltaEmbedder{}, deltaCfg(), nowTS); !errors.Is(err, ErrWindowExceedsRetention) {
		t.Fatalf("err = %v, want ErrWindowExceedsRetention", err)
	}
}

func TestRunDeltaRecommendsBulkOnLargeChangeSet(t *testing.T) {
	t.Parallel()

	store := baselineStore(
		chunk(1, 0, "Paris", "Paris\n\nlead"),
		chunk(2, 0, "Lyon", "Lyon\n\nlead"),
	)
	cfg := deltaCfg()
	cfg.BulkFraction = 0.4 // 2 changed of 2 pages > 0.4*2
	api := &fakeAPI{
		changes: []Change{
			{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)},
			{PageID: 2, Title: "Lyon", RevisionID: 200, Timestamp: baseTS.Add(2 * time.Hour)},
		},
		extracts: map[string]Extract{
			"Paris": {PageID: 1, Title: "Paris", RevisionID: 200, Text: "Paris new lead text here."},
			"Lyon":  {PageID: 2, Title: "Lyon", RevisionID: 200, Text: "Lyon new lead text here."},
		},
	}

	stats, err := RunDelta(t.Context(), store, api, &deltaEmbedder{}, cfg, nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if !stats.RecommendBulk {
		t.Errorf("stats.RecommendBulk = false, want true for a large change set")
	}
	// It still proceeds and advances the checkpoint.
	if len(store.syncStates) != 1 {
		t.Errorf("large change set did not advance the checkpoint")
	}
}

func TestRunDeltaCheckpointHeldOnFailure(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	store.upsertErr = errors.New("disk full")
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {PageID: 1, Title: "Paris", RevisionID: 200, Text: "new lead"}},
	}

	if _, err := RunDelta(t.Context(), store, api, &deltaEmbedder{}, deltaCfg(), nowTS); !errors.Is(err, store.upsertErr) {
		t.Fatalf("err = %v, want wrapped upsert error", err)
	}
	if len(store.syncStates) != 0 {
		t.Errorf("checkpoint advanced despite a failed run: %+v", store.syncStates)
	}
}
