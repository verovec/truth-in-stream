package factcheckarchive

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

// StreamCheckpoint is a crash-resume record for a broadened multi-stream ingest
// run: it remembers which query streams (a topic query or a publisher-scoped
// query) a run has already drained to the end, so a killed and restarted run skips
// those streams instead of re-paging them. It is NOT a cross-run dedup: a run
// Clears the checkpoint on successful completion of every stream, so the next
// scheduled run starts fresh and re-walks every stream to pick up new claims. The
// per-claim upsert (keyed on the review URL) makes re-ingesting an incomplete
// stream's already-seen claims harmless. Implementations are safe for concurrent
// use; NoStreamCheckpoint disables resume entirely.
type StreamCheckpoint interface {
	// Done reports whether the stream key was drained earlier in this resumed run.
	Done(key string) bool
	// MarkDone records a stream key as drained; it does not persist until Save.
	MarkDone(key string)
	// Save atomically writes the checkpoint if it changed since the last Save.
	Save() error
	// Clear discards the drained set (memory and disk) so a completed run leaves
	// nothing for the next run to skip.
	Clear() error
}

// NoStreamCheckpoint is the disabled checkpoint: it remembers nothing and persists
// nothing, so every stream runs every time (the pre-checkpoint behavior). Its
// methods are all no-ops.
type NoStreamCheckpoint struct{}

// Done always reports false: the disabled checkpoint remembers nothing.
func (NoStreamCheckpoint) Done(string) bool { return false }

// MarkDone is a no-op for the disabled checkpoint.
func (NoStreamCheckpoint) MarkDone(string) {}

// Save is a no-op for the disabled checkpoint.
func (NoStreamCheckpoint) Save() error { return nil }

// Clear is a no-op for the disabled checkpoint.
func (NoStreamCheckpoint) Clear() error { return nil }

// streamCheckpointFile is the on-disk shape: the sorted set of drained stream keys.
type streamCheckpointFile struct {
	Done []string `json:"done"`
}

// fileStreamCheckpoint persists drained stream keys to a JSON file with an atomic
// temp-write-and-rename, mirroring the wiki crawl checkpoint so a crash mid-write
// cannot corrupt it. A missing or corrupt file is treated as "nothing done" so a
// rerun re-walks rather than skipping streams it never finished.
type fileStreamCheckpoint struct {
	path string

	mu    sync.Mutex
	done  map[string]struct{}
	dirty bool
	gen   uint64
}

// LoadStreamCheckpoint reads the checkpoint at path, returning NoStreamCheckpoint
// for an empty path (resume disabled). A missing file starts empty; a corrupt file
// is treated as empty so the run re-walks rather than skipping streams it never
// finished.
func LoadStreamCheckpoint(path string) (StreamCheckpoint, error) {
	if path == "" {
		return NoStreamCheckpoint{}, nil
	}
	cp := &fileStreamCheckpoint{path: path, done: make(map[string]struct{})}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cp, nil
		}
		return nil, fmt.Errorf("factcheckarchive: read stream checkpoint %q: %w", path, err)
	}
	var stored streamCheckpointFile
	if err := json.Unmarshal(data, &stored); err != nil {
		return cp, nil
	}
	for _, k := range stored.Done {
		cp.done[k] = struct{}{}
	}
	return cp, nil
}

func (c *fileStreamCheckpoint) Done(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.done[key]
	return ok
}

func (c *fileStreamCheckpoint) MarkDone(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.done[key]; !ok {
		c.done[key] = struct{}{}
		c.dirty = true
		c.gen++
	}
}

// Save atomically persists the drained set when it changed. It writes a temp file
// in the target directory and renames it over the checkpoint, so a reader never
// sees a half-written file. A MarkDone that races the disk write is detected via
// gen and left dirty for the next Save rather than silently dropped.
func (c *fileStreamCheckpoint) Save() error {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	keys := make([]string, 0, len(c.done))
	for k := range c.done {
		keys = append(keys, k)
	}
	savedGen := c.gen
	c.mu.Unlock()
	slices.Sort(keys)

	data, err := json.Marshal(streamCheckpointFile{Done: keys})
	if err != nil {
		return fmt.Errorf("factcheckarchive: encode stream checkpoint: %w", err)
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("factcheckarchive: create stream checkpoint dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".factcheck-checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("factcheckarchive: create stream checkpoint temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("factcheckarchive: write stream checkpoint temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("factcheckarchive: close stream checkpoint temp: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != savedGen {
		return nil
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("factcheckarchive: rename stream checkpoint: %w", err)
	}
	c.dirty = false
	return nil
}

// Clear discards the drained set in memory and removes the checkpoint file, so a
// completed run leaves nothing for the next run to skip. A missing file is not an
// error.
func (c *fileStreamCheckpoint) Clear() error {
	c.mu.Lock()
	c.done = make(map[string]struct{})
	c.dirty = false
	c.gen++
	c.mu.Unlock()

	if err := os.Remove(c.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("factcheckarchive: remove stream checkpoint %q: %w", c.path, err)
	}
	return nil
}
