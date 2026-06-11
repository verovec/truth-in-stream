package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
)

// TestSeedEmbedderIsOfflineOnly proves the seed embedder never reaches the
// embedding provider, even when EMBEDDING_API_KEY is set to a (here invalid)
// value. A cache miss must surface as embed.ErrCacheMiss rather than a live
// request, so a stray or stale key can never turn `make seed` into a 401.
func TestSeedEmbedderIsOfflineOnly(t *testing.T) {
	t.Setenv("EMBEDDING_API_KEY", "definitely-not-a-valid-key")

	embedder := seedEmbedder(embed.NewCache(), config.DefaultEmbeddingModel)
	if _, err := embedder.EmbedDocuments(t.Context(), []string{"uncached fixture text"}); !errors.Is(err, embed.ErrCacheMiss) {
		t.Fatalf("EmbedDocuments error = %v, want ErrCacheMiss (a set key must not trigger a live call)", err)
	}
}

// TestCacheMissHintGuidesRefresh checks that a committed-cache miss is wrapped
// with the offending model AND the default, plus actionable guidance, while
// unrelated errors pass through untouched. It passes an override model distinct
// from (and not a substring of) the default so each assertion can only pass if
// that exact string is surfaced - a regression that drops either is caught.
func TestCacheMissHintGuidesRefresh(t *testing.T) {
	const overrideModel = "voyage-3.5" // distinct from, and not a substring of, the default
	if overrideModel == config.DefaultEmbeddingModel || strings.Contains(config.DefaultEmbeddingModel, overrideModel) {
		t.Fatalf("test needs a model distinct from and disjoint with the default %q", config.DefaultEmbeddingModel)
	}

	hinted := cacheMissHint(embed.ErrCacheMiss, overrideModel)
	if !errors.Is(hinted, embed.ErrCacheMiss) {
		t.Fatalf("hinted error no longer wraps ErrCacheMiss: %v", hinted)
	}
	for _, want := range []string{overrideModel, config.DefaultEmbeddingModel, "refresh-embeddings", "EMBEDDING_MODEL"} {
		if !strings.Contains(hinted.Error(), want) {
			t.Errorf("hint %q missing %q", hinted.Error(), want)
		}
	}

	other := errors.New("connection refused")
	if got := cacheMissHint(other, overrideModel); got != other {
		t.Errorf("cacheMissHint mangled a non-miss error: got %v, want %v", got, other)
	}
}

// TestCacheMissHintOmitsDefaultWhenActive confirms the default-model suggestion
// is suppressed when the active model already is the default, so the message
// does not name the same string twice as if there were a mismatch.
func TestCacheMissHintOmitsDefaultWhenActive(t *testing.T) {
	msg := cacheMissHint(embed.ErrCacheMiss, config.DefaultEmbeddingModel).Error()
	if strings.Contains(msg, "unset EMBEDDING_MODEL") {
		t.Errorf("hint suggests unsetting EMBEDDING_MODEL when the active model already is the default: %q", msg)
	}
}
