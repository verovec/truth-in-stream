package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/embed"
)

// TestSeedEmbedderIsOfflineOnly proves the seed embedder never reaches the
// embedding provider, even when EMBEDDING_API_KEY is set to a (here invalid)
// value. A cache miss must surface as embed.ErrCacheMiss rather than a live
// request, so a stray or stale key can never turn `make seed` into a 401.
func TestSeedEmbedderIsOfflineOnly(t *testing.T) {
	t.Setenv("EMBEDDING_API_KEY", "definitely-not-a-valid-key")

	embedder := seedEmbedder(embed.NewCache(), "voyage-4-large")
	if _, err := embedder.EmbedDocuments(t.Context(), []string{"uncached fixture text"}); !errors.Is(err, embed.ErrCacheMiss) {
		t.Fatalf("EmbedDocuments error = %v, want ErrCacheMiss (a set key must not trigger a live call)", err)
	}
}

// TestCacheMissHintGuidesRefresh checks that a committed-cache miss is wrapped
// with the named model and actionable guidance, while unrelated errors pass
// through untouched.
func TestCacheMissHintGuidesRefresh(t *testing.T) {
	hinted := cacheMissHint(embed.ErrCacheMiss, "voyage-4-large")
	if !errors.Is(hinted, embed.ErrCacheMiss) {
		t.Fatalf("hinted error no longer wraps ErrCacheMiss: %v", hinted)
	}
	for _, want := range []string{"voyage-4-large", "refresh-embeddings", "EMBEDDING_MODEL"} {
		if !strings.Contains(hinted.Error(), want) {
			t.Errorf("hint %q missing %q", hinted.Error(), want)
		}
	}

	other := errors.New("connection refused")
	if got := cacheMissHint(other, "voyage-4-large"); got != other {
		t.Errorf("cacheMissHint mangled a non-miss error: got %v, want %v", got, other)
	}
}

// TestEmbeddingModelDefaultsToVoyage4Large pins the offline default so it cannot
// silently drift from the model the committed cache is keyed under.
func TestEmbeddingModelDefaultsToVoyage4Large(t *testing.T) {
	t.Setenv("EMBEDDING_MODEL", "")
	if got := embeddingModel(); got != "voyage-4-large" {
		t.Errorf("embeddingModel() = %q, want voyage-4-large", got)
	}
	t.Setenv("EMBEDDING_MODEL", "voyage-4")
	if got := embeddingModel(); got != "voyage-4" {
		t.Errorf("embeddingModel() with override = %q, want voyage-4", got)
	}
}
