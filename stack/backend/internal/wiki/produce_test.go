package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
)

// fakePublisher records every published job so a test can assert the producer
// enqueued one prioritized job per un-embedded chunk.
type fakePublisher struct {
	mu   sync.Mutex
	msgs []publishedMsg
	err  error
}

type publishedMsg struct {
	body     []byte
	priority uint8
}

func (p *fakePublisher) Publish(_ context.Context, body []byte, priority uint8) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.msgs = append(p.msgs, publishedMsg{body: append([]byte(nil), body...), priority: priority})
	return nil
}

func (p *fakePublisher) published() []publishedMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishedMsg(nil), p.msgs...)
}

// fakeProducerStore models the staging table the producer drives: it serves the
// un-embedded chunks to enqueue and a programmed sequence of remaining counts
// that the drain wait polls until it reaches zero (the fleet finished).
type fakeProducerStore struct {
	mu sync.Mutex

	staging []domain.EvidenceChunk

	// remaining is the sequence StagingRemaining returns on successive calls; the
	// last value is held once the sequence is exhausted, modeling a fleet that
	// has reached a steady state (drained at 0, or stalled at a positive count).
	remaining []int64
	remIdx    int

	finalized        bool
	finalizedCorpus  string
	finalizedVersion string

	unembedErr error
	remErr     error
	finalErr   error
}

func (f *fakeProducerStore) CountUnembeddedStaging(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remErr != nil {
		return 0, f.remErr
	}
	var n int64
	switch {
	case len(f.remaining) == 0:
		n = 0
	case f.remIdx < len(f.remaining):
		n = f.remaining[f.remIdx]
		f.remIdx++
	default:
		n = f.remaining[len(f.remaining)-1]
	}
	return n, nil
}

func (f *fakeProducerStore) UnembeddedStaging(_ context.Context, after domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unembedErr != nil {
		return nil, f.unembedErr
	}
	out := []domain.EvidenceChunk{}
	for _, c := range f.staging {
		if afterCursor(c, after) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return lessChunk(out[i], out[j])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeProducerStore) FinalizeStaging(_ context.Context, corpus, version string, _ time.Time, _ string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finalErr != nil {
		return f.finalErr
	}
	f.finalized = true
	f.finalizedCorpus = corpus
	f.finalizedVersion = version
	return nil
}

func producerChunks(pages int) []domain.EvidenceChunk {
	const perPage = 2
	out := make([]domain.EvidenceChunk, 0, pages*perPage)
	for p := 1; p <= pages; p++ {
		for i := range perPage {
			out = append(out, domain.EvidenceChunk{
				Source:     "simplewiki",
				ExternalID: strconv.Itoa(p),
				ChunkIndex: i,
				Title:      "T",
				URL:        "https://simple.wikipedia.org/wiki/T",
				Content:    contentFor(p, i),
				Kind:       domain.EvidenceKindLead,
				Metadata:   domain.WikiMetadata{RevisionID: 1}.Map(),
			})
		}
	}
	return out
}

func producerConfig() ProducerConfig {
	return ProducerConfig{
		Corpus:             "simplewiki",
		DumpVersion:        "Mon, 01 Jun 2026 00:00:00 GMT",
		MaxPriority:        10,
		EnqueueBatchSize:   3,
		DrainPollInterval:  time.Second,
		DrainStallTimeout:  5 * time.Second,
		MaintenanceWorkMem: "64MB",
		MaxParallelWorkers: 0,
	}
}

func decodeJobs(t *testing.T, msgs []publishedMsg) []embedjob.Job {
	t.Helper()
	jobs := make([]embedjob.Job, len(msgs))
	for i, m := range msgs {
		if err := json.Unmarshal(m.body, &jobs[i]); err != nil {
			t.Fatalf("published message %d is not a valid embedjob.Job: %v", i, err)
		}
	}
	return jobs
}

func TestRunBulkEnqueuePublishesOneJobPerChunk(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store := &fakeProducerStore{
			staging:   producerChunks(3),
			remaining: []int64{6, 0}, // start total, then drained on the first drain check
		}
		pub := &fakePublisher{}

		stats, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		if stats.Published != 6 {
			t.Errorf("published = %d, want 6 (one per un-embedded chunk)", stats.Published)
		}

		msgs := pub.published()
		if len(msgs) != 6 {
			t.Fatalf("publisher saw %d messages, want 6", len(msgs))
		}
		// One job per chunk, each carrying the chunk's identity and text and a
		// fresh (zero) attempt. Publishes within a page race, so match by identity
		// rather than position - the broker, not publish order, enforces priority.
		byKey := map[string]embedjob.Job{}
		for _, j := range decodeJobs(t, msgs) {
			byKey[fmt.Sprintf("%s/%d", j.ExternalID, j.ChunkIndex)] = j
		}
		for _, want := range store.staging {
			key := fmt.Sprintf("%s/%d", want.ExternalID, want.ChunkIndex)
			got, ok := byKey[key]
			if !ok {
				t.Errorf("no job published for chunk %s", key)
				continue
			}
			if got.Content != want.Content {
				t.Errorf("job %s content = %q, want %q", key, got.Content, want.Content)
			}
			if got.Attempt != 0 {
				t.Errorf("job %s attempt = %d, want 0 (producer enqueues a fresh job)", key, got.Attempt)
			}
		}
		if !store.finalized || store.finalizedCorpus != "simplewiki" {
			t.Errorf("finalized = %v corpus %q, want true simplewiki", store.finalized, store.finalizedCorpus)
		}
		if store.finalizedVersion != producerConfig().DumpVersion {
			t.Errorf("finalized version = %q, want %q", store.finalizedVersion, producerConfig().DumpVersion)
		}
	})
}

// TestRunBulkPublishPublishesWithoutDrainOrSwap proves the cloud producer path:
// it enqueues one self-contained job per un-embedded chunk and returns without
// waiting for the fleet to drain or finalizing staging - the consumer owns the
// drain and swap. Two assertions guard that: store.finalized must stay false (a
// re-introduced FinalizeStaging would flip it), and remaining is pinned at a
// positive count that never drains (a re-introduced drain wait would loop on it
// until DrainStallTimeout and return an error, failing the test).
func TestRunBulkPublishPublishesWithoutDrainOrSwap(t *testing.T) {
	t.Parallel()
	store := &fakeProducerStore{staging: producerChunks(3), remaining: []int64{6}}
	pub := &fakePublisher{}

	stats, err := RunBulkPublish(t.Context(), discardLogger(), store, pub, producerConfig())
	if err != nil {
		t.Fatalf("RunBulkPublish: %v", err)
	}
	if stats.Published != 6 {
		t.Errorf("published = %d, want 6 (one per un-embedded chunk)", stats.Published)
	}
	if len(pub.published()) != 6 {
		t.Fatalf("publisher saw %d messages, want 6", len(pub.published()))
	}
	// Every job carries its chunk's content, so the worker needs no database to
	// embed it.
	for _, j := range decodeJobs(t, pub.published()) {
		if j.Content == "" {
			t.Fatalf("published job for %s/%d has empty content; jobs must be self-contained", j.ExternalID, j.ChunkIndex)
		}
	}
	if store.finalized {
		t.Error("RunBulkPublish finalized staging; the consumer, not the producer, owns the swap")
	}
}

// TestRunBulkPublishValidatesConfig rejects a config the publish path cannot use
// even though it does not need the drain settings.
func TestRunBulkPublishValidatesConfig(t *testing.T) {
	t.Parallel()
	cfg := producerConfig()
	cfg.EnqueueBatchSize = 0
	if _, err := RunBulkPublish(t.Context(), discardLogger(), &fakeProducerStore{}, &fakePublisher{}, cfg); err == nil {
		t.Fatal("RunBulkPublish with a zero batch size = nil error, want validation error")
	}
}

func TestRunBulkEnqueuePrioritizesLeadOverBody(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		lead := domain.EvidenceChunk{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0, Content: "lead text", Kind: domain.EvidenceKindLead}
		body := domain.EvidenceChunk{Source: "simplewiki", ExternalID: "2", ChunkIndex: 0, Content: "body text", Kind: domain.EvidenceKindBody}
		store := &fakeProducerStore{staging: []domain.EvidenceChunk{lead, body}, remaining: []int64{2, 0}}
		pub := &fakePublisher{}

		if _, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig()); err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		msgs := pub.published()
		if len(msgs) != 2 {
			t.Fatalf("published %d messages, want 2", len(msgs))
		}
		// Match priority to the chunk by content, since publishes within a page race.
		// With no clustering score, the static heuristic puts a lead in the upper
		// band and a body in the lower, so the lead embeds first.
		prio := map[string]uint8{}
		for _, m := range msgs {
			var j embedjob.Job
			if err := json.Unmarshal(m.body, &j); err != nil {
				t.Fatalf("decode published job: %v", err)
			}
			prio[j.Content] = m.priority
		}
		if prio["lead text"] <= prio["body text"] {
			t.Errorf("lead priority %d not above body priority %d", prio["lead text"], prio["body text"])
		}
	})
}

func TestRunBulkEnqueueWaitsForDrainBeforeSwap(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The fleet works the queue down over several polls; the swap must not fire
		// until staging is fully drained.
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4, 4, 2, 0}}
		pub := &fakePublisher{}

		done := make(chan struct{})
		var stats EnqueueStats
		var runErr error
		go func() {
			stats, runErr = RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
			close(done)
		}()

		// Let the publish loop run and the first drain check observe a positive
		// remaining count; the swap must still be pending.
		synctest.Wait()
		store.mu.Lock()
		finalizedEarly := store.finalized
		store.mu.Unlock()
		if finalizedEarly {
			t.Fatal("staging swapped before the fleet drained the queue")
		}

		<-done
		if runErr != nil {
			t.Fatalf("RunBulkEnqueue: %v", runErr)
		}
		if stats.Published != 4 {
			t.Errorf("published = %d, want 4", stats.Published)
		}
		if !store.finalized {
			t.Error("staging must swap once the fleet has drained")
		}
	})
}

func TestRunBulkEnqueueResumeEnqueuesOnlyRemaining(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// A prior run already embedded page 1 (both chunks), so staging only offers
		// the rest as un-embedded; the producer must enqueue only those.
		staging := producerChunks(3)
		rest := staging[2:] // drop page 1's two chunks
		store := &fakeProducerStore{staging: rest, remaining: []int64{4, 0}}
		pub := &fakePublisher{}

		stats, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err != nil {
			t.Fatalf("RunBulkEnqueue (resume): %v", err)
		}
		if stats.Published != 4 {
			t.Errorf("published = %d, want 4 (resume enqueues only the remainder)", stats.Published)
		}
		pages := map[string]bool{}
		for _, j := range decodeJobs(t, pub.published()) {
			pages[j.ExternalID] = true
		}
		if pages["1"] {
			t.Error("resume re-enqueued an already-embedded page 1 chunk")
		}
		if !pages["2"] || !pages["3"] {
			t.Errorf("resume published pages %v, want only the remainder pages 2 and 3", pages)
		}
		if !store.finalized {
			t.Error("resume must still drain and swap")
		}
	})
}

func TestRunBulkEnqueueFullyEmbeddedStillFinalizes(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// Nothing left to embed (a prior run died at finalize); the producer
		// publishes nothing but must still swap.
		store := &fakeProducerStore{staging: nil, remaining: []int64{0}}
		pub := &fakePublisher{}

		stats, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		if stats.Published != 0 {
			t.Errorf("published = %d, want 0", stats.Published)
		}
		if len(pub.published()) != 0 {
			t.Errorf("publisher saw %d messages, want 0", len(pub.published()))
		}
		if !store.finalized {
			t.Error("a fully-embedded resume must still finalize and swap")
		}
	})
}

func TestRunBulkEnqueueAbortsOnStalledFleet(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// The fleet never makes progress: remaining holds at 4 forever. With a 5s
		// stall timeout and 1s poll, the producer aborts rather than hang, and never
		// swaps a partially-embedded corpus.
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4}}
		pub := &fakePublisher{}

		_, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig())
		if err == nil {
			t.Fatal("RunBulkEnqueue returned nil, want a stalled-fleet error")
		}
		if !errors.Is(err, ErrDrainStalled) {
			t.Errorf("error = %v, want ErrDrainStalled", err)
		}
		if store.finalized {
			t.Error("a stalled run must not swap a partially-embedded corpus")
		}
	})
}

func TestRunBulkEnqueueStopsOnCanceledContext(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		// A canceled context (a -max-duration budget or SIGTERM) during the drain
		// wait is a clean resumable stop: it surfaces ctx.Err and never swaps.
		ctx, cancel := context.WithCancel(t.Context())
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4}}
		pub := &fakePublisher{}

		done := make(chan error, 1)
		go func() {
			_, err := RunBulkEnqueue(ctx, discardLogger(), store, pub, producerConfig())
			done <- err
		}()
		synctest.Wait()
		cancel()

		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if store.finalized {
			t.Error("a canceled run must not swap")
		}
	})
}

func TestStaticImportance(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 10000)
	short := "x"
	// A lead always outranks a body; within a kind, longer content outranks
	// shorter; an unknown kind floors to zero.
	leadLong := staticImportance(domain.EvidenceChunk{Kind: domain.EvidenceKindLead, Content: long})
	leadShort := staticImportance(domain.EvidenceChunk{Kind: domain.EvidenceKindLead, Content: short})
	bodyLong := staticImportance(domain.EvidenceChunk{Kind: domain.EvidenceKindBody, Content: long})
	unknown := staticImportance(domain.EvidenceChunk{Kind: domain.EvidenceChunkKind("mystery"), Content: long})

	if !(leadLong > leadShort) {
		t.Errorf("longer lead %.3f should outrank shorter lead %.3f", leadLong, leadShort)
	}
	if !(leadShort > bodyLong) {
		t.Errorf("any lead %.3f should outrank any body %.3f", leadShort, bodyLong)
	}
	if unknown != 0 {
		t.Errorf("unknown kind importance = %.3f, want 0", unknown)
	}
	for _, v := range []float64{leadLong, leadShort, bodyLong} {
		if v < 0 || v >= 1 {
			t.Errorf("importance %.3f out of [0,1)", v)
		}
	}
}

func TestPriorityFromImportance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		importance  float64
		maxPriority uint8
		want        uint8
	}{
		{"top importance maps to ceiling", 1.0, 10, 10},
		{"half importance maps to half", 0.5, 10, 5},
		{"zero importance floors", 0.0, 10, 0},
		{"rounds to nearest band", 0.46, 10, 5},
		{"above-one score clamps to ceiling", 1.4, 10, 10},
		{"negative score clamps to zero", -0.2, 10, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := priorityFromImportance(tc.importance, tc.maxPriority); got != tc.want {
				t.Errorf("priorityFromImportance(%v, %d) = %d, want %d", tc.importance, tc.maxPriority, got, tc.want)
			}
		})
	}
}

func TestPriorityForPrefersImportanceOverStatic(t *testing.T) {
	t.Parallel()
	imp := 0.9
	// A body chunk (the static heuristic would put it low) that carries a high
	// clustering score must be prioritized by the score, not the static fallback.
	scored := domain.EvidenceChunk{Kind: domain.EvidenceKindBody, Content: "x", Metadata: domain.WikiMetadata{Importance: &imp}.Map()}
	if got := priorityFor(scored, 10); got != 9 {
		t.Errorf("scored body chunk priority = %d, want 9 (importance drives it)", got)
	}
	// With no score, the same chunk falls back to the static heuristic, which keeps
	// a short body well below the importance-driven band.
	unscored := domain.EvidenceChunk{Kind: domain.EvidenceKindBody, Content: "x"}
	want := priorityFromImportance(staticImportance(unscored), 10)
	if got := priorityFor(unscored, 10); got != want {
		t.Errorf("unscored body chunk priority = %d, want %d (static fallback)", got, want)
	}
}

func TestRunBulkEnqueueUsesImportanceForPriority(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		high := 1.0
		low := 0.2
		// Both chunks are lead (kind heuristic would tie them at the ceiling), but
		// their importance scores must produce distinct priorities.
		c1 := domain.EvidenceChunk{Source: "simplewiki", ExternalID: "1", ChunkIndex: 0, Content: "important", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{Importance: &high}.Map()}
		c2 := domain.EvidenceChunk{Source: "simplewiki", ExternalID: "2", ChunkIndex: 0, Content: "minor", Kind: domain.EvidenceKindLead, Metadata: domain.WikiMetadata{Importance: &low}.Map()}
		store := &fakeProducerStore{staging: []domain.EvidenceChunk{c1, c2}, remaining: []int64{2, 0}}
		pub := &fakePublisher{}

		if _, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig()); err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		prio := map[string]uint8{}
		for _, m := range pub.published() {
			var j embedjob.Job
			if err := json.Unmarshal(m.body, &j); err != nil {
				t.Fatalf("decode published job: %v", err)
			}
			prio[j.Content] = m.priority
		}
		if prio["important"] != 10 {
			t.Errorf("high-importance chunk priority = %d, want 10", prio["important"])
		}
		if prio["minor"] != 2 {
			t.Errorf("low-importance chunk priority = %d, want 2", prio["minor"])
		}
	})
}

// fakeLiveProducerStore models the live corpus the bulk-into-live producer pages
// for un-embedded chunks; it has no finalize step because there is no swap.
type fakeLiveProducerStore struct {
	mu         sync.Mutex
	live       []domain.EvidenceChunk
	countErr   error
	unembedErr error
}

func (f *fakeLiveProducerStore) CountUnembeddedLive(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countErr != nil {
		return 0, f.countErr
	}
	return int64(len(f.live)), nil
}

func (f *fakeLiveProducerStore) UnembeddedLive(_ context.Context, after domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unembedErr != nil {
		return nil, f.unembedErr
	}
	out := []domain.EvidenceChunk{}
	for _, c := range f.live {
		if afterCursor(c, after) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return lessChunk(out[i], out[j])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestRunBulkLivePublishPublishesLiveJobs(t *testing.T) {
	t.Parallel()
	store := &fakeLiveProducerStore{live: producerChunks(3)}
	pub := &fakePublisher{}

	stats, err := RunBulkLivePublish(t.Context(), discardLogger(), store, pub, producerConfig())
	if err != nil {
		t.Fatalf("RunBulkLivePublish: %v", err)
	}
	msgs := pub.published()
	if stats.Published != 6 || len(msgs) != 6 {
		t.Fatalf("published %d (stats %d), want 6", len(msgs), stats.Published)
	}
	// Every live job targets the live corpus, never staging, so the fleet writes
	// the vector straight into wiki_chunks.
	for _, j := range decodeJobs(t, msgs) {
		if j.Staging {
			t.Errorf("live job for page %s chunk %d set Staging; want live", j.ExternalID, j.ChunkIndex)
		}
	}
}

func TestRunBulkLivePublishValidatesConfig(t *testing.T) {
	t.Parallel()
	cfg := producerConfig()
	cfg.MaxPriority = 0
	if _, err := RunBulkLivePublish(t.Context(), discardLogger(), &fakeLiveProducerStore{}, &fakePublisher{}, cfg); err == nil {
		t.Fatal("RunBulkLivePublish with zero max priority: want error, got nil")
	}
}

func TestRunBulkEnqueueStampsStagingJobs(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		store := &fakeProducerStore{staging: producerChunks(2), remaining: []int64{4, 0}}
		pub := &fakePublisher{}
		if _, err := RunBulkEnqueue(t.Context(), discardLogger(), store, pub, producerConfig()); err != nil {
			t.Fatalf("RunBulkEnqueue: %v", err)
		}
		// Atomic-rebuild jobs target staging, so the fleet fills the staging table
		// for the later swap rather than the live corpus.
		for _, j := range decodeJobs(t, pub.published()) {
			if !j.Staging {
				t.Errorf("atomic job for page %s chunk %d did not set Staging", j.ExternalID, j.ChunkIndex)
			}
		}
	})
}
