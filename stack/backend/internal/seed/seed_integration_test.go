package seed_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embed"
	"github.com/verovec/truth-in-stream/backend/internal/ingest"
	"github.com/verovec/truth-in-stream/backend/internal/seed"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// fakeVideoMedia is an in-memory object store: the video seed's media path runs
// with no real network or MinIO so the integration test stays hermetic.
type fakeVideoMedia struct {
	objects map[string][]byte
}

func (f *fakeVideoMedia) Exists(_ context.Context, key string) (bool, error) {
	_, ok := f.objects[key]
	return ok, nil
}

func (f *fakeVideoMedia) Upload(_ context.Context, key string, body io.Reader, _ string, _ int64) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.objects[key] = b
	return nil
}

// bytesFetcher serves fixed media bytes, standing in for the HTTP clip fetch.
type bytesFetcher struct {
	data []byte
}

func (f bytesFetcher) Fetch(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.data)), nil
}

// claimsSchemaLock matches the key the store and service integration tests take
// (postgres.claimsSchemaLock) so every package that resets the shared schema
// serializes against the same lock.
const claimsSchemaLock = int64(0x747275746873)

func lockSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("lock: connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", claimsSchemaLock); err != nil {
		t.Fatalf("lock: acquire: %v", err)
	}
}

func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, segment_results, processed_videos, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state"); err != nil {
		t.Fatalf("reset: drop tables: %v", err)
	}
	dir := filepath.Join("..", "..", "migrations")
	ups, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		t.Fatalf("reset: glob migrations: %v", err)
	}
	sort.Strings(ups)
	for _, up := range ups {
		sql, err := os.ReadFile(up)
		if err != nil {
			t.Fatalf("reset: read %s: %v", up, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("reset: apply %s: %v", up, err)
		}
	}
}

func setupStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping pgvector integration test")
	}
	ctx := t.Context()
	lockSchema(ctx, t, dsn)
	resetSchema(ctx, t, dsn)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "seed", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestSeedWikiChunksSearchable(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks, err := seed.LoadWikiChunks(openFixture(t, "wiki_chunks.json"))
	if err != nil {
		t.Fatalf("LoadWikiChunks: %v", err)
	}
	embedder := embed.NewDeterministic(domain.EmbeddingDim)
	if err := seed.InsertWikiChunks(ctx, store, embedder, chunks); err != nil {
		t.Fatalf("SeedWikiChunks: %v", err)
	}

	// The deterministic embedder maps a query to the same vector as the matching
	// document, so a query for a chunk's exact content returns that chunk first.
	target := chunks[0]
	qvecs, err := embedder.EmbedQueries(ctx, []string{target.Content})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	got, err := store.SearchWiki(ctx, qvecs[0], 3)
	if err != nil {
		t.Fatalf("SearchWiki: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("SearchWiki returned no evidence")
	}
	if got[0].Title != target.Title || got[0].URL != target.URL {
		t.Errorf("nearest = %q/%q, want %q/%q", got[0].Title, got[0].URL, target.Title, target.URL)
	}
	if got[0].Distance > 1e-3 {
		t.Errorf("nearest distance = %v, want ~0", got[0].Distance)
	}
}

func TestSeedWikiChunksIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	chunks, err := seed.LoadWikiChunks(openFixture(t, "wiki_chunks.json"))
	if err != nil {
		t.Fatalf("LoadWikiChunks: %v", err)
	}
	embedder := embed.NewDeterministic(domain.EmbeddingDim)
	for range 2 {
		if err := seed.InsertWikiChunks(ctx, store, embedder, chunks); err != nil {
			t.Fatalf("SeedWikiChunks: %v", err)
		}
	}
	pages, err := store.CountPages(ctx)
	if err != nil {
		t.Fatalf("CountPages: %v", err)
	}
	if want := int64(7); pages != want {
		t.Errorf("distinct pages = %d, want %d (reseed must not duplicate)", pages, want)
	}
}

func TestSeedDemoResultsServed(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	demo, err := seed.LoadDemoResults(openFixture(t, "demo_results.json"))
	if err != nil {
		t.Fatalf("LoadDemoResults: %v", err)
	}
	videoID := service.VideoID(demo.Source)
	if err := seed.InsertDemoResults(ctx, store, videoID, demo.Segments); err != nil {
		t.Fatalf("SeedDemoResults: %v", err)
	}

	count, processed, err := store.ProcessedSegmentCount(ctx, videoID)
	if err != nil {
		t.Fatalf("ProcessedSegmentCount: %v", err)
	}
	if !processed {
		t.Fatal("demo video not marked processed")
	}
	if count != len(demo.Segments) {
		t.Errorf("segment count = %d, want %d", count, len(demo.Segments))
	}

	results, err := store.ListSegmentResults(ctx, videoID)
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if len(results) != len(demo.Segments) {
		t.Fatalf("listed %d results, want %d", len(results), len(demo.Segments))
	}
	// First segment is a checked claim with both a claim and an evidence match.
	first := results[0]
	if first.SkipReason != domain.SkipReasonNone {
		t.Errorf("first segment skip reason = %q, want none", first.SkipReason)
	}
	if len(first.Matches) != 2 {
		t.Fatalf("first segment has %d matches, want 2", len(first.Matches))
	}
	if first.Matches[0].Kind != domain.MatchKindClaim || first.Matches[1].Kind != domain.MatchKindEvidence {
		t.Errorf("match kinds = %q,%q, want claim,evidence", first.Matches[0].Kind, first.Matches[1].Kind)
	}
	if first.Matches[1].Article == nil {
		t.Error("evidence match lost its article attribution")
	}
	// The skipped segment carries its reason and no matches.
	var sawSkip bool
	for _, r := range results {
		if r.SkipReason == domain.SkipReasonNotAClaim {
			sawSkip = true
			if len(r.Matches) != 0 {
				t.Errorf("skipped segment has %d matches, want 0", len(r.Matches))
			}
		}
	}
	if !sawSkip {
		t.Error("no skipped segment found in seeded demo results")
	}
}

func TestSeedDemoResultsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	demo, err := seed.LoadDemoResults(openFixture(t, "demo_results.json"))
	if err != nil {
		t.Fatalf("LoadDemoResults: %v", err)
	}
	videoID := service.VideoID(demo.Source)
	for range 2 {
		if err := seed.InsertDemoResults(ctx, store, videoID, demo.Segments); err != nil {
			t.Fatalf("SeedDemoResults: %v", err)
		}
	}
	results, err := store.ListSegmentResults(ctx, videoID)
	if err != nil {
		t.Fatalf("ListSegmentResults: %v", err)
	}
	if len(results) != len(demo.Segments) {
		t.Errorf("listed %d results after reseed, want %d (must not duplicate)", len(results), len(demo.Segments))
	}
}

func TestInsertSampleVideosListed(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	media := &fakeVideoMedia{objects: map[string][]byte{}}
	fetcher := bytesFetcher{data: []byte("sample-media-bytes")}
	samples := seed.Samples("https://example.test/clip.mp4")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cacheDir := t.TempDir()

	// Reseed twice to prove idempotency against the real store.
	for range 2 {
		if err := seed.InsertSampleVideos(ctx, store, media, fetcher, cacheDir, samples, logger); err != nil {
			t.Fatalf("InsertSampleVideos: %v", err)
		}
	}

	videos, err := store.ListVideos(ctx)
	if err != nil {
		t.Fatalf("ListVideos: %v", err)
	}
	if len(videos) != len(samples) {
		t.Fatalf("listed %d videos after reseed, want %d (must not duplicate)", len(videos), len(samples))
	}
	got := videos[0]
	if got.Kind != domain.VideoKindSample {
		t.Errorf("kind = %q, want sample", got.Kind)
	}
	if got.Status != domain.VideoStatusReady {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if want := int64(len("sample-media-bytes")); got.SizeBytes != want {
		t.Errorf("SizeBytes = %d, want %d (real media bytes)", got.SizeBytes, want)
	}
	if _, ok := media.objects[got.ObjectKey]; !ok {
		t.Errorf("media not uploaded for object key %q", got.ObjectKey)
	}
}

func TestSeedClaimsSearchable(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	seeds, err := ingest.LoadSeed(openFixture(t, "claims.json"))
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	embedder := embed.NewDeterministic(domain.EmbeddingDim)
	n, err := ingest.Run(ctx, store, embedder, seeds, 0)
	if err != nil {
		t.Fatalf("ingest.Run: %v", err)
	}
	if n != len(seeds) {
		t.Errorf("ingested %d claims, want %d", n, len(seeds))
	}

	target := seeds[0]
	qvecs, err := embedder.EmbedQueries(ctx, []string{target.Text})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	got, err := store.Search(ctx, qvecs[0], 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 || got[0].ID != target.ID {
		t.Fatalf("nearest claim = %v, want %q", got, target.ID)
	}
}
