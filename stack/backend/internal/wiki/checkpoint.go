package wiki

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Checkpoint is a crash-resume record for one crawl run: it remembers which pages
// a run has already resolved (published, dropped by the gate, or missing upstream)
// so that if the run is killed and restarted, the rerun skips those pages before
// their extract fetch and their fact-checkability gate spend. It is NOT a cross-run
// dedup: a run Clears the checkpoint on successful completion, so the next
// (scheduled) run starts fresh and re-crawls every category to pick up new and
// updated articles. Implementations are safe for concurrent use. NoCheckpoint
// disables resume entirely, so a crawl with no configured checkpoint path behaves
// as it did before.
type Checkpoint interface {
	// Done reports whether pageID was resolved earlier in the current (resumed) run.
	Done(pageID int64) bool
	// MarkDone records pages as resolved; it does not persist until Save.
	MarkDone(pageIDs ...int64)
	// Save atomically writes the checkpoint if it changed since the last Save.
	Save() error
	// Clear discards the resolved set (in memory and on disk), so a completed run
	// leaves nothing for the next run to skip.
	Clear() error
}

// NoCheckpoint is the disabled checkpoint: it remembers nothing and persists
// nothing, so every page is processed every run (the pre-resume behavior).
type NoCheckpoint struct{}

// Done always reports false: the disabled checkpoint remembers nothing.
func (NoCheckpoint) Done(int64) bool { return false }

// MarkDone is a no-op for the disabled checkpoint.
func (NoCheckpoint) MarkDone(...int64) {}

// Save is a no-op for the disabled checkpoint.
func (NoCheckpoint) Save() error { return nil }

// Clear is a no-op for the disabled checkpoint.
func (NoCheckpoint) Clear() error { return nil }

// checkpointFile is the on-disk shape: the sorted set of resolved page ids.
type checkpointFile struct {
	Done []int64 `json:"done"`
}

// fileCheckpoint persists resolved page ids to a JSON file with an atomic
// temp-write-and-rename, mirroring the scrutins marker so a crash mid-write cannot
// corrupt the checkpoint. A missing or corrupt file is treated as "nothing done"
// so a rerun re-crawls rather than skipping wrongly.
type fileCheckpoint struct {
	path string

	mu    sync.Mutex
	done  map[int64]struct{}
	dirty bool
	// gen increments on every mutation that adds a page, so Save can detect a
	// MarkDone that raced its own disk write and avoid clearing dirty for a mark it
	// did not persist.
	gen uint64
}

// LoadCheckpoint reads the checkpoint at path, returning NoCheckpoint for an empty
// path (resume disabled). A missing file starts empty; a corrupt file is treated as
// empty so the run re-crawls rather than skipping pages it never actually finished.
func LoadCheckpoint(path string) (Checkpoint, error) {
	if path == "" {
		return NoCheckpoint{}, nil
	}
	cp := &fileCheckpoint{path: path, done: make(map[int64]struct{})}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cp, nil
		}
		return nil, fmt.Errorf("wiki: read crawl checkpoint %q: %w", path, err)
	}
	var stored checkpointFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return cp, nil
	}
	for _, id := range stored.Done {
		cp.done[id] = struct{}{}
	}
	return cp, nil
}

func (c *fileCheckpoint) Done(pageID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.done[pageID]
	return ok
}

func (c *fileCheckpoint) MarkDone(pageIDs ...int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range pageIDs {
		if _, ok := c.done[id]; !ok {
			c.done[id] = struct{}{}
			c.dirty = true
			c.gen++
		}
	}
}

// Save atomically persists the resolved set when it changed. It writes a temp file
// in the target directory and renames it over the checkpoint, so a reader never
// sees a half-written file. A no-op when nothing changed since the last Save. A
// MarkDone that races the disk write is detected via gen and left dirty for the
// next Save rather than silently dropped.
func (c *fileCheckpoint) Save() error {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	ids := make([]int64, 0, len(c.done))
	for id := range c.done {
		ids = append(ids, id)
	}
	savedGen := c.gen
	c.mu.Unlock()
	slices.Sort(ids)

	data, err := json.Marshal(checkpointFile{Done: ids})
	if err != nil {
		return fmt.Errorf("wiki: encode crawl checkpoint: %w", err)
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("wiki: create crawl checkpoint dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".crawl-checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("wiki: create crawl checkpoint temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("wiki: write crawl checkpoint temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("wiki: close crawl checkpoint temp: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("wiki: rename crawl checkpoint: %w", err)
	}

	c.mu.Lock()
	if c.gen == savedGen {
		c.dirty = false
	}
	c.mu.Unlock()
	return nil
}

// Clear discards the resolved set in memory and removes the checkpoint file, so a
// completed run leaves nothing for the next run to skip. A missing file is not an
// error.
func (c *fileCheckpoint) Clear() error {
	c.mu.Lock()
	c.done = make(map[int64]struct{})
	c.dirty = false
	c.gen++
	c.mu.Unlock()

	if err := os.Remove(c.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("wiki: remove crawl checkpoint %q: %w", c.path, err)
	}
	return nil
}
