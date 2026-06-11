package embed

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNormalizeText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "trims ends", in: "  hello  ", want: "hello"},
		{name: "collapses internal runs", in: "a\t b\n  c", want: "a b c"},
		{name: "already normal", in: "one two three", want: "one two three"},
		{name: "empty", in: "   ", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeText(tc.in); got != tc.want {
				t.Errorf("NormalizeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCacheKeyStability(t *testing.T) {
	t.Parallel()
	base := CacheKey("voyage-4", "document", "the sky is blue")

	if base == "" {
		t.Fatal("CacheKey returned empty")
	}
	// Whitespace-only differences in the text must not change the key.
	if got := CacheKey("voyage-4", "document", "  the   sky\tis blue "); got != base {
		t.Errorf("normalized text changed key: %q != %q", got, base)
	}
	// Model, input type, and text are all part of the key.
	if CacheKey("voyage-4", "query", "the sky is blue") == base {
		t.Error("input type does not affect key")
	}
	if CacheKey("voyage-3", "document", "the sky is blue") == base {
		t.Error("model does not affect key")
	}
	if CacheKey("voyage-4", "document", "the grass is green") == base {
		t.Error("text does not affect key")
	}
}

func TestCacheSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "embeddings.cache.jsonl")

	c := NewCache()
	c.Set("zeta", []float32{0.5, -0.25, 1})
	c.Set("alpha", []float32{1, 2, 3})
	if !c.Dirty() {
		t.Fatal("cache with writes should be dirty")
	}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if loaded.Dirty() {
		t.Error("freshly loaded cache should not be dirty")
	}
	if got, want := loaded.Len(), 2; got != want {
		t.Fatalf("loaded %d entries, want %d", got, want)
	}
	for _, key := range []string{"alpha", "zeta"} {
		want, _ := c.Get(key)
		got, ok := loaded.Get(key)
		if !ok {
			t.Fatalf("key %q missing after reload", key)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("key %q vector mismatch (-want +got):\n%s", key, diff)
		}
	}
}

func TestLoadCacheMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	c, err := LoadCache(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("LoadCache(absent): %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("absent cache has %d entries, want 0", c.Len())
	}
}

func TestCacheGetReturnsCopy(t *testing.T) {
	t.Parallel()
	c := NewCache()
	c.Set("k", []float32{1, 2, 3})
	got, _ := c.Get("k")
	got[0] = 99
	again, _ := c.Get("k")
	if again[0] != 1 {
		t.Errorf("Get returned an aliased slice; mutation leaked back: %v", again)
	}
}

// countingFiller records how many texts it was asked to embed and returns a
// deterministic vector per text so callers can assert ordering.
type countingFiller struct {
	calls    int
	embedded int
}

func (f *countingFiller) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	return f.fill(texts), nil
}

func (f *countingFiller) EmbedQueries(_ context.Context, texts []string) ([][]float32, error) {
	return f.fill(texts), nil
}

func (f *countingFiller) fill(texts []string) [][]float32 {
	f.calls++
	f.embedded += len(texts)
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		out[i] = []float32{float32(len(txt))}
	}
	return out
}

func TestCachedHitDoesNotCallFiller(t *testing.T) {
	t.Parallel()
	c := NewCache()
	c.Set(CacheKey("voyage-4", "document", "cached text"), []float32{7})

	filler := &countingFiller{}
	cached := NewCached(c, "voyage-4", filler)

	got, err := cached.EmbedDocuments(t.Context(), []string{"cached text"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if filler.calls != 0 {
		t.Errorf("filler called %d times on a pure hit, want 0", filler.calls)
	}
	if diff := cmp.Diff([][]float32{{7}}, got); diff != "" {
		t.Errorf("hit vector mismatch (-want +got):\n%s", diff)
	}
}

func TestCachedMissCallsFillerAndWritesThrough(t *testing.T) {
	t.Parallel()
	c := NewCache()
	c.Set(CacheKey("voyage-4", "document", "hit"), []float32{1})

	filler := &countingFiller{}
	cached := NewCached(c, "voyage-4", filler)

	got, err := cached.EmbedDocuments(t.Context(), []string{"hit", "miss-1", "miss-2"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	// Only the two misses are sent to the filler, in one batch.
	if filler.calls != 1 || filler.embedded != 2 {
		t.Errorf("filler calls=%d embedded=%d, want 1 and 2", filler.calls, filler.embedded)
	}
	// Results stay aligned to the input order.
	want := [][]float32{{1}, {float32(len("miss-1"))}, {float32(len("miss-2"))}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ordered result mismatch (-want +got):\n%s", diff)
	}
	// Write-through: a second call for the same texts is a pure hit.
	if _, err := cached.EmbedDocuments(t.Context(), []string{"miss-1", "miss-2"}); err != nil {
		t.Fatalf("EmbedDocuments (second): %v", err)
	}
	if filler.calls != 1 {
		t.Errorf("filler called again after write-through: calls=%d, want 1", filler.calls)
	}
}

func TestCachedMissWithoutFillerErrors(t *testing.T) {
	t.Parallel()
	cached := NewCached(NewCache(), "voyage-4", nil)
	_, err := cached.EmbedDocuments(t.Context(), []string{"uncached"})
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("offline miss error = %v, want ErrCacheMiss", err)
	}
}

func TestCachedQueriesUseQueryInputType(t *testing.T) {
	t.Parallel()
	c := NewCache()
	c.Set(CacheKey("voyage-4", "query", "q"), []float32{42})
	cached := NewCached(c, "voyage-4", nil)

	got, err := cached.EmbedQueries(t.Context(), []string{"q"})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	if diff := cmp.Diff([][]float32{{42}}, got); diff != "" {
		t.Errorf("query hit mismatch (-want +got):\n%s", diff)
	}
}
