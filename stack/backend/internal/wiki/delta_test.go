package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
)

// --- test helpers for the generalized evidence_chunks shape ---

// parseID reverses the stringified external id back to the numeric page id the
// fakes key their maps by.
func parseID(s string) int64 {
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// chunkRevision reads the wiki revision id out of a chunk's metadata, the field
// that used to be domain.EvidenceChunk.RevisionID before provenance moved into
// the generic metadata map.
func chunkRevision(c domain.EvidenceChunk) int64 {
	wm, _ := domain.ParseWikiMetadata(c.Metadata)
	return wm.RevisionID
}

// chunkSection reads the section heading out of a chunk's metadata.
func chunkSection(c domain.EvidenceChunk) string {
	wm, _ := domain.ParseWikiMetadata(c.Metadata)
	return wm.Section
}

// afterCursor reports whether c sorts strictly after cur in (source, external
// id, chunk index) keyset order, the order evidence_chunks pages in.
func afterCursor(c domain.EvidenceChunk, cur domain.EvidenceCursor) bool {
	if c.Source != cur.Source {
		return c.Source > cur.Source
	}
	if c.ExternalID != cur.ExternalID {
		return c.ExternalID > cur.ExternalID
	}
	return int32(c.ChunkIndex) > cur.ChunkIndex
}

// lessChunk orders two chunks in that same keyset order.
func lessChunk(a, b domain.EvidenceChunk) bool {
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	if a.ExternalID != b.ExternalID {
		return a.ExternalID < b.ExternalID
	}
	return a.ChunkIndex < b.ChunkIndex
}

// --- fakes ---

type storedChunk struct {
	chunk    domain.EvidenceChunk
	embedded bool
}

type fakeDeltaStore struct {
	mu              sync.Mutex
	chunks          map[[2]int64]storedChunk
	state           domain.EvidenceSyncState
	hasState        bool
	syncStates      []domain.EvidenceSyncState
	embedInProgress bool
	upsertErr       error
	setStateErr     error
}

func newFakeDeltaStore() *fakeDeltaStore {
	return &fakeDeltaStore{chunks: make(map[[2]int64]storedChunk)}
}

func (f *fakeDeltaStore) seed(c domain.EvidenceChunk, embedded bool) {
	f.chunks[[2]int64{parseID(c.ExternalID), int64(c.ChunkIndex)}] = storedChunk{chunk: c, embedded: embedded}
}

// markEmbedded simulates the worker fleet embedding a published chunk in place,
// so a resume test can prove the next run never re-publishes it.
func (f *fakeDeltaStore) markEmbedded(externalID string, chunkIndex int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [2]int64{parseID(externalID), int64(chunkIndex)}
	if sc, ok := f.chunks[key]; ok {
		sc.embedded = true
		f.chunks[key] = sc
	}
}

func (f *fakeDeltaStore) GetSyncState(_ context.Context, _ string) (domain.EvidenceSyncState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.hasState, nil
}

func (f *fakeDeltaStore) EmbedInProgress(_ context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.embedInProgress, nil
}

func (f *fakeDeltaStore) CountDocuments(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pages := map[int64]struct{}{}
	for key := range f.chunks {
		pages[key[0]] = struct{}{}
	}
	return int64(len(pages)), nil
}

func (f *fakeDeltaStore) StoredRevisions(_ context.Context, _ string, pageIDs []int64) (map[int64]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[int64]struct{}{}
	for _, id := range pageIDs {
		want[id] = struct{}{}
	}
	out := map[int64]int64{}
	for key, sc := range f.chunks {
		if _, ok := want[key[0]]; ok {
			if rev := chunkRevision(sc.chunk); rev > out[key[0]] {
				out[key[0]] = rev
			}
		}
	}
	return out, nil
}

func (f *fakeDeltaStore) UnembeddedChunks(_ context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var pending []domain.EvidenceChunk
	for _, sc := range f.chunks {
		if sc.embedded {
			continue
		}
		if afterCursor(sc.chunk, cur) {
			pending = append(pending, sc.chunk)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return lessChunk(pending[i], pending[j])
	})
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func (f *fakeDeltaStore) UpsertChunks(_ context.Context, chunks []domain.EvidenceChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return f.upsertErr
	}
	for _, c := range chunks {
		key := [2]int64{parseID(c.ExternalID), int64(c.ChunkIndex)}
		embedded := false
		if old, ok := f.chunks[key]; ok && old.chunk.Content == c.Content {
			embedded = old.embedded
		}
		f.chunks[key] = storedChunk{chunk: c, embedded: embedded}
	}
	return nil
}

func (f *fakeDeltaStore) TrimDocuments(_ context.Context, trims []domain.EvidenceTrim) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tr := range trims {
		id := parseID(tr.ExternalID)
		for key := range f.chunks {
			if key[0] == id && key[1] >= int64(tr.FromIndex) {
				delete(f.chunks, key)
			}
		}
	}
	return nil
}

func (f *fakeDeltaStore) DeleteByTitle(_ context.Context, _ string, titles []string) error {
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

func (f *fakeDeltaStore) SetSyncState(_ context.Context, st domain.EvidenceSyncState) error {
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

// publishedJob is one enqueued embedding job as the fake publisher decoded it.
type publishedJob struct {
	externalID string
	chunkIndex int
	staging    bool
	priority   uint8
}

// deltaPublisher records the embedding jobs the delta run enqueues for the fleet.
// failAfter > 0 makes the (failAfter+1)th publish fail, so a test can cut a
// window short mid-publish and prove the resume behavior.
type deltaPublisher struct {
	mu        sync.Mutex
	published []publishedJob
	failAfter int
	calls     int
}

func (p *deltaPublisher) Publish(_ context.Context, body []byte, priority uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failAfter > 0 && p.calls > p.failAfter {
		return errors.New("broker down")
	}
	var job embedjob.Job
	if err := json.Unmarshal(body, &job); err != nil {
		return err
	}
	p.published = append(p.published, publishedJob{
		externalID: job.ExternalID,
		chunkIndex: job.ChunkIndex,
		staging:    job.Staging,
		priority:   priority,
	})
	return nil
}

func (p *deltaPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

func (p *deltaPublisher) has(externalID string, chunkIndex int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, j := range p.published {
		if j.externalID == externalID && j.chunkIndex == chunkIndex {
			return true
		}
	}
	return false
}

// --- fixtures ---

var (
	baseTS = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	nowTS  = baseTS.Add(24 * time.Hour)
)

func deltaCfg() DeltaConfig {
	return DeltaConfig{Corpus: "simplewiki", RetentionDays: 30, BulkFraction: 0.9, MaxPriority: 10, EnqueueBatchSize: 10}
}

func baselineStore(chunks ...domain.EvidenceChunk) *fakeDeltaStore {
	s := newFakeDeltaStore()
	s.state = domain.EvidenceSyncState{Source: "simplewiki", LastChangeTS: baseTS, DumpVersion: "v1"}
	s.hasState = true
	for _, c := range chunks {
		s.seed(c, true)
	}
	return s
}

// chunk builds a baseline chunk at revision 100; delta runs move pages to a
// higher revision via the extract fixtures.
func chunk(pageID int64, idx int, title, content string) domain.EvidenceChunk {
	return domain.EvidenceChunk{
		Source:     "simplewiki",
		ExternalID: strconv.FormatInt(pageID, 10),
		ChunkIndex: idx,
		Title:      title,
		Content:    content,
		Metadata:   domain.WikiMetadata{RevisionID: 100}.Map(),
	}
}

// --- tests ---

func TestRunDeltaRefetchesEditsAndPublishes(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nold lead"))
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {PageID: 1, Title: "Paris", RevisionID: 200, Text: "Paris is the capital of France and sits on the Seine."}},
	}
	pub := &deltaPublisher{}

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Changed != 1 || stats.Skipped != 0 || stats.Deleted != 0 {
		t.Errorf("stats = %+v, want Changed 1", stats)
	}
	if stats.Published != 1 || pub.count() != 1 {
		t.Errorf("expected 1 chunk published, got Published=%d publishes=%d", stats.Published, pub.count())
	}
	if !pub.has("1", 0) || pub.published[0].staging {
		t.Errorf("published %+v, want the live chunk (1,0)", pub.published)
	}
	got := store.chunks[[2]int64{1, 0}]
	if chunkRevision(got.chunk) != 200 {
		t.Errorf("chunk (1,0) revid = %d, want 200", chunkRevision(got.chunk))
	}
	if got.embedded {
		t.Errorf("chunk (1,0) marked embedded inline; delta must leave embedding to the fleet")
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
	pub := &deltaPublisher{}

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Skipped != 1 || stats.Changed != 0 {
		t.Errorf("stats = %+v, want Skipped 1 Changed 0", stats)
	}
	if len(api.requested) != 0 {
		t.Errorf("refetched %v despite an unchanged revision", api.requested)
	}
	if pub.count() != 0 {
		t.Errorf("published despite no content change: %d", pub.count())
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
	pub := &deltaPublisher{}

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS)
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
	if pub.count() != 0 {
		t.Errorf("a pure-deletion window published %d jobs", pub.count())
	}
}

func TestRunDeltaMissingExtractDeletes(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	api := &fakeAPI{
		changes:  []Change{{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)}},
		extracts: map[string]Extract{"Paris": {Title: "Paris", Missing: true}},
	}

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, &deltaPublisher{}, deltaCfg(), nowTS)
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
	pub := &deltaPublisher{}

	if _, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS); err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if store.pageChunks(1) != 0 {
		t.Errorf("page turned redirect kept chunks")
	}
	if pub.count() != 0 {
		t.Errorf("redirect published %d jobs", pub.count())
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
	pub := &deltaPublisher{}

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats.Deleted != 0 {
		t.Errorf("restored page was deleted: stats=%+v", stats)
	}
	if store.pageChunks(1) == 0 {
		t.Fatal("restored page has no chunks; it was lost")
	}
	if got := store.chunks[[2]int64{1, 0}]; chunkRevision(got.chunk) != 250 {
		t.Errorf("restored chunk revid = %d, want 250", chunkRevision(got.chunk))
	}
	if !pub.has("1", 0) {
		t.Errorf("restored chunk was not published to the fleet")
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

	if _, err := RunDelta(t.Context(), discardLogger(), store, api, &deltaPublisher{}, deltaCfg(), nowTS); err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if got := store.chunks[[2]int64{1, 0}]; chunkRevision(got.chunk) != 200 {
		t.Errorf("chunk revid = %d, want 200 (fell back to the reported revision)", chunkRevision(got.chunk))
	}
}

func TestRunDeltaNoChangesIsNoop(t *testing.T) {
	t.Parallel()

	store := baselineStore(chunk(1, 0, "Paris", "Paris\n\nlead"))
	api := &fakeAPI{changes: nil}
	pub := &deltaPublisher{}

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS)
	if err != nil {
		t.Fatalf("RunDelta: %v", err)
	}
	if stats != (DeltaStats{}) {
		t.Errorf("stats = %+v, want zero", stats)
	}
	if pub.count() != 0 || len(api.requested) != 0 {
		t.Errorf("no-change run did work: published=%d extracts=%v", pub.count(), api.requested)
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
	pub := &deltaPublisher{}

	if _, err := RunDelta(t.Context(), discardLogger(), store, api, pub, deltaCfg(), nowTS); !errors.Is(err, ErrBulkEmbedInProgress) {
		t.Fatalf("err = %v, want ErrBulkEmbedInProgress", err)
	}
	if pub.count() != 0 || len(store.syncStates) != 0 {
		t.Errorf("refused run still did work: published=%d checkpoints=%d", pub.count(), len(store.syncStates))
	}
}

func TestRunDeltaNoBaseline(t *testing.T) {
	t.Parallel()

	store := newFakeDeltaStore() // never synced
	if _, err := RunDelta(t.Context(), discardLogger(), store, &fakeAPI{}, &deltaPublisher{}, deltaCfg(), nowTS); !errors.Is(err, ErrNoBaseline) {
		t.Fatalf("err = %v, want ErrNoBaseline", err)
	}
}

func TestRunDeltaWindowExceedsRetention(t *testing.T) {
	t.Parallel()

	store := baselineStore()
	store.state.LastChangeTS = nowTS.Add(-40 * 24 * time.Hour)
	api := &fakeAPI{changes: []Change{{PageID: 1, Title: "Paris", RevisionID: 1, Timestamp: nowTS}}}

	if _, err := RunDelta(t.Context(), discardLogger(), store, api, &deltaPublisher{}, deltaCfg(), nowTS); !errors.Is(err, ErrWindowExceedsRetention) {
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

	stats, err := RunDelta(t.Context(), discardLogger(), store, api, &deltaPublisher{}, cfg, nowTS)
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

	if _, err := RunDelta(t.Context(), discardLogger(), store, api, &deltaPublisher{}, deltaCfg(), nowTS); !errors.Is(err, store.upsertErr) {
		t.Fatalf("err = %v, want wrapped upsert error", err)
	}
	if len(store.syncStates) != 0 {
		t.Errorf("checkpoint advanced despite a failed run: %+v", store.syncStates)
	}
}

// TestRunDeltaResumesWithoutReEmbeddingConfirmed proves the fleet-publish resume
// guarantee: a run cut short mid-window holds its checkpoint, and the rerun
// republishes only the chunks the fleet has not yet embedded - never a chunk a
// confirmed batch already embedded.
func TestRunDeltaResumesWithoutReEmbeddingConfirmed(t *testing.T) {
	t.Parallel()

	store := baselineStore(
		chunk(1, 0, "Paris", "Paris\n\nold"),
		chunk(2, 0, "Lyon", "Lyon\n\nold"),
	)
	api := &fakeAPI{
		changes: []Change{
			{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: baseTS.Add(time.Hour)},
			{PageID: 2, Title: "Lyon", RevisionID: 200, Timestamp: baseTS.Add(2 * time.Hour)},
		},
		extracts: map[string]Extract{
			"Paris": {PageID: 1, Title: "Paris", RevisionID: 200, Text: "Paris new lead text."},
			"Lyon":  {PageID: 2, Title: "Lyon", RevisionID: 200, Text: "Lyon new lead text."},
		},
	}
	cfg := deltaCfg()
	cfg.EnqueueBatchSize = 1 // one chunk per confirmed page

	// Run 1: the publisher confirms the first chunk (Paris, external id "1" sorts
	// before "2"), then fails, cutting the window short.
	pub1 := &deltaPublisher{failAfter: 1}
	if _, err := RunDelta(t.Context(), discardLogger(), store, api, pub1, cfg, nowTS); err == nil {
		t.Fatal("run 1 expected to fail mid-window")
	}
	if len(store.syncStates) != 0 {
		t.Fatalf("checkpoint advanced despite a mid-window failure")
	}
	if pub1.count() != 1 {
		t.Fatalf("run 1 published %d, want 1 before the failure", pub1.count())
	}
	confirmed := pub1.published[0]

	// The fleet embeds the one confirmed chunk in place.
	store.markEmbedded(confirmed.externalID, confirmed.chunkIndex)

	// Run 2: the publisher is healthy. It must skip the already-embedded chunk and
	// publish only the remaining one, then advance the checkpoint.
	pub2 := &deltaPublisher{}
	if _, err := RunDelta(t.Context(), discardLogger(), store, api, pub2, cfg, nowTS); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if pub2.has(confirmed.externalID, confirmed.chunkIndex) {
		t.Errorf("run 2 re-published already-embedded chunk %s#%d", confirmed.externalID, confirmed.chunkIndex)
	}
	if pub2.count() != 1 {
		t.Errorf("run 2 published %d, want 1 (only the still-un-embedded chunk)", pub2.count())
	}
	if len(store.syncStates) != 1 {
		t.Errorf("run 2 did not advance the checkpoint")
	}
}
