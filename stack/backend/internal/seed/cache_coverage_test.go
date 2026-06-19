package seed_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/ingest"
	"github.com/verovec/truth-in-stream/backend/internal/seed"
)

// committedCacheModel is the model the committed offline cache is keyed under -
// the exported canonical default, so this guard probes the exact keys the seed
// binary and a refresh use; a real refresh re-keys under the same model.
const committedCacheModel = config.DefaultEmbeddingModel

// TestCommittedCacheCoversFixtures fails if the committed embedding cache is
// missing a vector for any fixture document text. It runs with no database and
// no API key, so CI catches a fixture edited without a cache refresh - the case
// that would otherwise turn an offline seed into a cache-miss error.
func TestCommittedCacheCoversFixtures(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "seed")
	cache, err := embed.LoadCache(filepath.Join(root, "embeddings.cache.jsonl"))
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if cache.Len() == 0 {
		t.Fatal("committed embedding cache is empty")
	}

	for _, text := range fixtureDocumentTexts(t, root) {
		key := embed.CacheKey(committedCacheModel, "document", text)
		if _, ok := cache.Get(key); !ok {
			t.Errorf("committed cache missing embedding for fixture text: %q\nrun: make refresh-embeddings (or cmd/seed -refresh)", text)
		}
	}
}

// fixtureDocumentTexts returns every claim statement and wiki chunk content that
// must have a committed embedding.
func fixtureDocumentTexts(t *testing.T, root string) []string {
	t.Helper()

	claimsF, err := os.Open(filepath.Join(root, "claims.json"))
	if err != nil {
		t.Fatalf("open claims fixture: %v", err)
	}
	defer func() { _ = claimsF.Close() }()
	claims, err := ingest.LoadSeed(claimsF)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}

	wikiF, err := os.Open(filepath.Join(root, "wiki_chunks.json"))
	if err != nil {
		t.Fatalf("open wiki fixture: %v", err)
	}
	defer func() { _ = wikiF.Close() }()
	chunks, err := seed.LoadWikiChunks(wikiF)
	if err != nil {
		t.Fatalf("LoadWikiChunks: %v", err)
	}

	politicalF, err := os.Open(filepath.Join(root, "political_claims.json"))
	if err != nil {
		t.Fatalf("open political claims fixture: %v", err)
	}
	defer func() { _ = politicalF.Close() }()
	political, err := seed.LoadPoliticalClaims(politicalF)
	if err != nil {
		t.Fatalf("LoadPoliticalClaims: %v", err)
	}

	texts := make([]string, 0, len(claims)+len(chunks)+len(political))
	for _, c := range claims {
		texts = append(texts, c.Text)
	}
	for _, c := range chunks {
		texts = append(texts, c.Content)
	}
	for _, c := range political {
		texts = append(texts, c.Text)
	}
	return texts
}
