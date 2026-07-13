package wiki

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// ErrNoBaseline is returned when a delta run finds no completed bulk ingest to
// resume from: there is no checkpoint to window changes against.
var ErrNoBaseline = errors.New("wiki: corpus has no bulk baseline; run -mode=bulk first")

// ErrWindowExceedsRetention is returned when the checkpoint predates the
// RecentChanges retention window, so an incremental catch-up would silently miss
// changes; a bulk re-ingest is required instead.
var ErrWindowExceedsRetention = errors.New("wiki: checkpoint older than the recentchanges retention window; run -mode=bulk")

// ErrBulkEmbedInProgress is returned when a delta run finds a bulk embed still
// in flight (its staging table is present). The live corpus is not yet fully
// embedded, so a delta run would embed - and bill - the entire corpus in place
// instead of just the changed chunks; finish or restart the bulk embed first.
var ErrBulkEmbedInProgress = errors.New("wiki: a bulk embed is incomplete; finish -mode=bulk before delta")

// DeltaConfig tunes a delta run. RetentionDays bounds how stale the checkpoint
// may be; BulkFraction is the change-set share of the corpus above which a bulk
// re-run is recommended; MaxPriority is the queue's priority ceiling the
// per-chunk priority mapping is bounded by; EnqueueBatchSize is how many
// un-embedded chunks are read per keyset scan while publishing embedding jobs to
// the fleet.
type DeltaConfig struct {
	Corpus           string
	RetentionDays    int
	BulkFraction     float64
	MaxPriority      uint8
	EnqueueBatchSize int
}

// DeltaStats summarizes a completed delta run. Published is how many embedding
// jobs the run enqueued for the worker fleet. RecommendBulk is set when the
// change set was large enough that a bulk re-run would have been preferable.
type DeltaStats struct {
	Changed       int
	Skipped       int
	Deleted       int
	Published     int
	RecommendBulk bool
}

// DeltaSource is the read side of the store a delta run needs.
type DeltaSource interface {
	// GetSyncState loads the corpus checkpoint; ok is false when never synced.
	GetSyncState(ctx context.Context, corpus string) (domain.EvidenceSyncState, bool, error)
	// CountDocuments returns the number of distinct documents in the live corpus.
	CountDocuments(ctx context.Context) (int64, error)
	// StoredRevisions returns the stored revision id of each requested page of the
	// source that has chunks; absent pages are omitted.
	StoredRevisions(ctx context.Context, source string, pageIDs []int64) (map[int64]int64, error)
	// EmbedInProgress reports whether a bulk embed is mid-flight, in which case
	// the live corpus is not yet fully embedded and delta must not run.
	EmbedInProgress(ctx context.Context) (bool, error)
	// UnembeddedChunks returns up to limit live chunks lacking an embedding,
	// ordered after cur.
	UnembeddedChunks(ctx context.Context, cur domain.EvidenceCursor, limit int) ([]domain.EvidenceChunk, error)
}

// DeltaSink is the write side of the store a delta run drives.
type DeltaSink interface {
	// UpsertChunks inserts or replaces chunks by (page, chunk index), clearing
	// the embedding of any chunk whose content changed.
	UpsertChunks(ctx context.Context, chunks []domain.EvidenceChunk) error
	// TrimDocuments removes the stale tail of each document's chunks.
	TrimDocuments(ctx context.Context, trims []domain.EvidenceTrim) error
	// DeleteByTitle removes every chunk of each named document within the source.
	DeleteByTitle(ctx context.Context, source string, titles []string) error
	// SetSyncState advances the corpus checkpoint after a successful run.
	SetSyncState(ctx context.Context, st domain.EvidenceSyncState) error
}

// DeltaStore is the full store surface a delta run drives.
type DeltaStore interface {
	DeltaSource
	DeltaSink
}

// ChangeSource is the MediaWiki API surface a delta run reads from.
type ChangeSource interface {
	// RecentChanges returns main-namespace changes since the checkpoint.
	RecentChanges(ctx context.Context, since time.Time) ([]Change, error)
	// Extracts fetches the current lead and revision of each title.
	Extracts(ctx context.Context, titles []string) ([]Extract, error)
}

// RunDelta brings the corpus up to date incrementally. It reads the checkpoint,
// refuses a window the RecentChanges retention cannot cover, asks the API what
// changed, refetches only pages whose revision moved, upserts their re-chunked
// content with a NULL embedding, removes deleted pages, publishes one embedding
// job per still-un-embedded live chunk to the worker fleet, and advances the
// checkpoint last.
//
// Publishing to the fleet rather than embedding inline is what makes a mid-window
// failure cheap to resume: a re-run refetches nothing whose stored revision
// already matches (unchangedFiltered), and republishes only chunks still lacking
// an embedding (the NULL filter in the keyset scan), so no chunk a confirmed
// batch already embedded is re-embedded (or re-billed). The publish itself
// confirms each page before advancing its cursor, so an interrupted publish
// re-enqueues at most one page of idempotent jobs. A run that finds nothing
// changed touches neither the fleet nor the checkpoint.
func RunDelta(ctx context.Context, logger *slog.Logger, store DeltaStore, api ChangeSource, pub Publisher, cfg DeltaConfig, now time.Time) (DeltaStats, error) {
	if cfg.MaxPriority < 1 || cfg.EnqueueBatchSize < 1 {
		return DeltaStats{}, fmt.Errorf("wiki: delta sync needs a positive max priority and enqueue batch size, got %d and %d", cfg.MaxPriority, cfg.EnqueueBatchSize)
	}
	if logger == nil {
		logger = slog.Default()
	}

	state, ok, err := store.GetSyncState(ctx, cfg.Corpus)
	if err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: read sync state: %w", err)
	}
	if !ok || state.LastChangeTS.IsZero() {
		return DeltaStats{}, ErrNoBaseline
	}
	if now.Sub(state.LastChangeTS) > time.Duration(cfg.RetentionDays)*24*time.Hour {
		return DeltaStats{}, ErrWindowExceedsRetention
	}
	// A delta run embeds whatever live chunks lack an embedding; that is only the
	// chunks it changes once the corpus is fully embedded. While a bulk embed is
	// incomplete the live corpus is still unembedded, so refuse rather than embed
	// (and bill) all of it in place.
	inProgress, err := store.EmbedInProgress(ctx)
	if err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: check embed state: %w", err)
	}
	if inProgress {
		return DeltaStats{}, ErrBulkEmbedInProgress
	}

	changes, err := api.RecentChanges(ctx, state.LastChangeTS)
	if err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: fetch recent changes: %w", err)
	}
	if len(changes) == 0 {
		return DeltaStats{}, nil
	}
	plan := planChanges(changes)

	var stats DeltaStats
	recommend, err := overBulkThreshold(ctx, store, len(plan.edits)+len(plan.deletes), cfg.BulkFraction)
	if err != nil {
		return DeltaStats{}, err
	}
	stats.RecommendBulk = recommend

	refetch, skipped, err := unchangedFiltered(ctx, store, cfg.Corpus, plan.edits)
	if err != nil {
		return DeltaStats{}, err
	}
	stats.Skipped = skipped

	extracts, err := api.Extracts(ctx, refetch)
	if err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: fetch extracts: %w", err)
	}
	chunks, trims, missing := buildDeltaChunks(extracts, plan.edits, cfg.Corpus)
	stats.Changed = len(trims)

	if err := store.TrimDocuments(ctx, trims); err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: trim stale chunks: %w", err)
	}
	if err := store.UpsertChunks(ctx, chunks); err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: upsert chunks: %w", err)
	}

	deletes := dedupeTitles(plan.deletes, missing)
	if err := store.DeleteByTitle(ctx, cfg.Corpus, deletes); err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: delete pages: %w", err)
	}
	stats.Deleted = len(deletes)

	// Publish one embedding job per still-un-embedded live chunk (the ones this run
	// just upserted with a NULL embedding) to the worker fleet, in keyset order with
	// each page confirmed before the cursor advances. The fleet embeds them in
	// place; a re-run's NULL filter skips whatever the fleet has since filled, so no
	// confirmed chunk is re-embedded.
	published, err := publishJobs(ctx, logger, store.UnembeddedChunks, false, pub, ProducerConfig{
		Corpus:           cfg.Corpus,
		MaxPriority:      cfg.MaxPriority,
		EnqueueBatchSize: cfg.EnqueueBatchSize,
	})
	if err != nil {
		return DeltaStats{}, err
	}
	stats.Published = published

	state.LastChangeTS = plan.maxTS
	if err := store.SetSyncState(ctx, state); err != nil {
		return DeltaStats{}, fmt.Errorf("wiki: checkpoint sync state: %w", err)
	}
	return stats, nil
}

// changePlan is the deduplicated outcome of one RecentChanges window: the latest
// edit per page title, the titles whose latest event is a deletion, and the
// newest change timestamp to advance the checkpoint to.
type changePlan struct {
	edits   map[string]Change
	deletes []string
	maxTS   time.Time
}

// planChanges collapses a change stream to its net effect per title, keeping the
// latest event for each, so an edit-then-delete deletes and a delete-then-create
// refetches. maxTS is the newest timestamp seen, the next checkpoint.
func planChanges(changes []Change) changePlan {
	latest := make(map[string]Change, len(changes))
	var maxTS time.Time
	for _, ch := range changes {
		if ch.Timestamp.After(maxTS) {
			maxTS = ch.Timestamp
		}
		if cur, ok := latest[ch.Title]; !ok || ch.Timestamp.After(cur.Timestamp) {
			latest[ch.Title] = ch
		}
	}
	plan := changePlan{edits: make(map[string]Change, len(latest)), maxTS: maxTS}
	for title, ch := range latest {
		if ch.Deleted {
			plan.deletes = append(plan.deletes, title)
		} else {
			plan.edits[title] = ch
		}
	}
	return plan
}

// overBulkThreshold reports whether the change set exceeds the configured share
// of the corpus, signaling that a bulk re-run would beat incremental inserts.
func overBulkThreshold(ctx context.Context, src DeltaSource, changedPages int, fraction float64) (bool, error) {
	total, err := src.CountDocuments(ctx)
	if err != nil {
		return false, fmt.Errorf("wiki: count corpus pages: %w", err)
	}
	return total > 0 && float64(changedPages) > fraction*float64(total), nil
}

// unchangedFiltered drops edits whose stored revision already matches the one
// RecentChanges reported, returning the titles still to refetch and the count
// skipped - so a page seen again in an overlap window is neither refetched nor
// re-embedded.
func unchangedFiltered(ctx context.Context, src DeltaSource, source string, edits map[string]Change) ([]string, int, error) {
	pageIDs := make([]int64, 0, len(edits))
	for _, ch := range edits {
		if ch.PageID > 0 {
			pageIDs = append(pageIDs, ch.PageID)
		}
	}
	stored, err := src.StoredRevisions(ctx, source, pageIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("wiki: read stored revisions: %w", err)
	}
	refetch := make([]string, 0, len(edits))
	skipped := 0
	for title, ch := range edits {
		if rev, ok := stored[ch.PageID]; ok && rev == ch.RevisionID {
			skipped++
			continue
		}
		refetch = append(refetch, title)
	}
	return refetch, skipped, nil
}

// buildDeltaChunks turns refetched extracts into the chunks to upsert and the
// trims that drop each page's stale tail (a now-empty lead, e.g. a page turned
// redirect, trims from 0 and removes it). Titles the API no longer has a live
// page for come back as deletions. The revision id is the one the extract
// reports, falling back to the revision RecentChanges reported so a page is
// never stored at revision 0 (which would refetch it every run).
func buildDeltaChunks(extracts []Extract, edits map[string]Change, corpus string) (chunks []domain.EvidenceChunk, trims []domain.EvidenceTrim, missing []string) {
	chunks = make([]domain.EvidenceChunk, 0, len(extracts))
	trims = make([]domain.EvidenceTrim, 0, len(extracts))
	for _, ex := range extracts {
		if ex.Missing {
			missing = append(missing, ex.Title)
			continue
		}
		revID := ex.RevisionID
		if revID == 0 {
			revID = edits[ex.Title].RevisionID
		}
		pieces := Chunk(ex.Title, ex.Text)
		trims = append(trims, domain.EvidenceTrim{Source: corpus, ExternalID: strconv.FormatInt(ex.PageID, 10), FromIndex: len(pieces)})
		for i, content := range pieces {
			chunks = append(chunks, domain.EvidenceChunk{
				Source:     corpus,
				ExternalID: strconv.FormatInt(ex.PageID, 10),
				ChunkIndex: i,
				Title:      ex.Title,
				URL:        pageURL(corpus, ex.Title),
				Content:    content,
				Kind:       domain.EvidenceKindLead,
				Metadata:   domain.WikiMetadata{RevisionID: revID}.Map(),
			})
		}
	}
	return chunks, trims, missing
}

// dedupeTitles merges title slices into one with duplicates removed.
func dedupeTitles(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, g := range groups {
		for _, t := range g {
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}
