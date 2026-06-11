package embed

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// ErrCacheMiss is returned by a Cached embedder configured without a filler
// when a text is not already in the cache. Offline seeding relies on a complete
// committed cache; a miss means a fixture changed and the cache must be
// refreshed with a real embedding key.
var ErrCacheMiss = errors.New("embed: cache miss and no embedder configured")

// NormalizeText collapses leading, trailing, and internal whitespace runs to
// single spaces so cache keys are stable across trivial reformatting of fixture
// text.
func NormalizeText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// CacheKey is the deterministic lookup key for an embedding: the hex SHA-256
// digest over the model, input type, and normalized text. The same text under
// the same model and input type always maps to the same key, and the three
// inputs are separated by a NUL so distinct fields cannot collide.
func CacheKey(model, inputType, text string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + inputType + "\x00" + NormalizeText(text)))
	return hex.EncodeToString(sum[:])
}

// Cache is an in-memory, file-backed map from CacheKey to embedding vector. It
// is safe for concurrent use; the seed loader fills it from one goroutine and
// the bulk embedder may write through from several.
type Cache struct {
	mu      sync.Mutex
	vectors map[string][]float32
	dirty   bool
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{vectors: make(map[string][]float32)}
}

// cacheEntry is the on-disk JSON Lines shape: one object per line, sorted by
// key, so the committed cache produces stable, per-entry diffs.
type cacheEntry struct {
	Key    string    `json:"key"`
	Vector []float32 `json:"vector"`
}

// LoadCache reads a JSON Lines cache file. A missing file yields an empty cache
// rather than an error: the first refresh run creates it.
func LoadCache(path string) (*Cache, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewCache(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("embed: open cache %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	c := NewCache()
	scanner := bufio.NewScanner(f)
	// Embeddings are long lines; raise the scanner buffer well above the 64KiB
	// default so a 1024-dim vector fits on one line.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e cacheEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("embed: decode cache %q: %w", path, err)
		}
		c.vectors[e.Key] = e.Vector
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("embed: read cache %q: %w", path, err)
	}
	return c, nil
}

// Get returns a copy of the vector for key, so callers cannot mutate the cached
// data.
func (c *Cache) Get(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.vectors[key]
	if !ok {
		return nil, false
	}
	return slices.Clone(v), true
}

// Set stores a copy of vec under key and marks the cache dirty.
func (c *Cache) Set(key string, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vectors[key] = slices.Clone(vec)
	c.dirty = true
}

// Dirty reports whether the cache has unsaved writes.
func (c *Cache) Dirty() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirty
}

// Len returns the number of cached vectors.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.vectors)
}

// Save writes the cache as key-sorted JSON Lines to path, then clears the dirty
// flag.
func (c *Cache) Save(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := make([]string, 0, len(c.vectors))
	for k := range c.vectors {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, k := range keys {
		if err := enc.Encode(cacheEntry{Key: k, Vector: c.vectors[k]}); err != nil {
			return fmt.Errorf("embed: encode cache entry %q: %w", k, err)
		}
	}
	// Write to a temp file in the same directory and rename into place, so a
	// crash mid-write never leaves the committed cache truncated and undecodable.
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return fmt.Errorf("embed: write cache %q: %w", path, err)
	}
	c.dirty = false
	return nil
}

// writeFileAtomic writes data to a temp file in path's directory and renames it
// over path; rename is atomic on the same filesystem.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Filler embeds cache misses. The Voyage *Client and the Deterministic embedder
// both satisfy it; a nil filler makes a Cached embedder offline-only, returning
// ErrCacheMiss when a text is absent.
type Filler interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQueries(ctx context.Context, texts []string) ([][]float32, error)
}

// Cached is an embedder that serves vectors from a Cache, embedding only the
// misses through an optional filler and writing the results back so the next run
// is a pure hit. It decorates any Filler, so seeding reads the committed cache
// offline and a refresh run fills new entries via Voyage.
type Cached struct {
	cache  *Cache
	model  string
	filler Filler
}

// NewCached wraps cache and filler for model. filler may be nil for offline
// seeding against a complete committed cache.
func NewCached(cache *Cache, model string, filler Filler) *Cached {
	return &Cached{cache: cache, model: model, filler: filler}
}

// EmbedDocuments embeds texts for storage (input_type=document), serving hits
// from the cache.
func (c *Cached) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts, string(inputTypeDocument))
}

// EmbedQueries embeds texts for retrieval (input_type=query), serving hits from
// the cache.
func (c *Cached) EmbedQueries(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts, string(inputTypeQuery))
}

func (c *Cached) embed(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	missTexts := make([]string, 0, len(texts))
	missPos := make([]int, 0, len(texts))

	for i, txt := range texts {
		key := CacheKey(c.model, inputType, txt)
		if vec, ok := c.cache.Get(key); ok {
			out[i] = vec
			continue
		}
		missTexts = append(missTexts, txt)
		missPos = append(missPos, i)
	}
	if len(missTexts) == 0 {
		return out, nil
	}
	if c.filler == nil {
		return nil, fmt.Errorf("%w: %q (run refresh-embeddings with EMBEDDING_API_KEY)", ErrCacheMiss, missTexts[0])
	}

	filled, err := c.fillMisses(ctx, missTexts, inputType)
	if err != nil {
		return nil, err
	}
	if len(filled) != len(missTexts) {
		return nil, fmt.Errorf("embed: filler returned %d vectors, want %d", len(filled), len(missTexts))
	}
	for i, vec := range filled {
		c.cache.Set(CacheKey(c.model, inputType, missTexts[i]), vec)
		out[missPos[i]] = vec
	}
	return out, nil
}

func (c *Cached) fillMisses(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if inputType == string(inputTypeQuery) {
		return c.filler.EmbedQueries(ctx, texts)
	}
	return c.filler.EmbedDocuments(ctx, texts)
}
