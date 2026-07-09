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
	"github.com/verovec/truth-in-stream/backend/internal/pgtest"
	"github.com/verovec/truth-in-stream/backend/internal/seed"
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
	// Hold the shared schema-reset lock for the whole test, not just the
	// reset: the integration packages share one database, so releasing after
	// the reset would let another package drop these tables mid-test. Cleanup
	// runs at test end, serializing every DB-touching test across packages.
	release, err := pgtest.AcquireSchemaLock(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(release)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, document_sentences, document_claims, segment_results, processed_videos, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, evidence_chunks, evidence_chunks_staging, evidence_chunks_old, evidence_sync_state, political_claims, voting_records"); err != nil {
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
	got, err := store.SearchEvidence(ctx, qvecs[0], 3)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("SearchEvidence returned no evidence")
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
	pages, err := store.CountDocuments(ctx)
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if want := int64(7); pages != want {
		t.Errorf("distinct pages = %d, want %d (reseed must not duplicate)", pages, want)
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

func TestSeedPoliticalClaimsSearchable(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	claims, err := seed.LoadPoliticalClaims(openFixture(t, "political_claims.json"))
	if err != nil {
		t.Fatalf("LoadPoliticalClaims: %v", err)
	}
	embedder := embed.NewDeterministic(domain.EmbeddingDim)
	if err := seed.InsertPoliticalClaims(ctx, store, embedder, claims); err != nil {
		t.Fatalf("InsertPoliticalClaims: %v", err)
	}

	// The deterministic embedder maps a query to the same vector as the matching
	// document, so a query for the motivating statement's exact text returns its
	// curated claim first, carrying both verdict axes and the real source.
	var target domain.PoliticalClaim
	for _, c := range claims {
		if c.Text == "500 000 immigrés entrent chaque année dont seuls 10% travaillent" {
			target = c
			break
		}
	}
	if target.ID == "" {
		t.Fatal("motivating claim not present in fixture")
	}
	qvecs, err := embedder.EmbedQueries(ctx, []string{target.Text})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	got, err := store.SearchPoliticalClaims(ctx, qvecs[0], 3)
	if err != nil {
		t.Fatalf("SearchPoliticalClaims: %v", err)
	}
	if len(got) == 0 || got[0].ID != target.ID {
		t.Fatalf("nearest political claim = %v, want %q", got, target.ID)
	}
	if got[0].Distance > 1e-3 {
		t.Errorf("nearest distance = %v, want ~0", got[0].Distance)
	}
	if got[0].LiteralVerdict != domain.LiteralInaccurate {
		t.Errorf("literal verdict = %q, want inaccurate", got[0].LiteralVerdict)
	}
	if len(got[0].Flags) == 0 {
		t.Error("motivating claim must carry a manipulation flag")
	}
	if got[0].SourceURL == "" {
		t.Error("motivating claim must carry a resolvable source URL")
	}
}

func TestSeedPoliticalClaimsIdempotent(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	claims, err := seed.LoadPoliticalClaims(openFixture(t, "political_claims.json"))
	if err != nil {
		t.Fatalf("LoadPoliticalClaims: %v", err)
	}
	embedder := embed.NewDeterministic(domain.EmbeddingDim)
	for range 2 {
		if err := seed.InsertPoliticalClaims(ctx, store, embedder, claims); err != nil {
			t.Fatalf("InsertPoliticalClaims: %v", err)
		}
	}

	// Reseeding must rewrite the same rows, not duplicate them: searching wide
	// returns exactly the seeded count.
	qvecs, err := embedder.EmbedQueries(ctx, []string{claims[0].Text})
	if err != nil {
		t.Fatalf("EmbedQueries: %v", err)
	}
	got, err := store.SearchPoliticalClaims(ctx, qvecs[0], len(claims)+1)
	if err != nil {
		t.Fatalf("SearchPoliticalClaims: %v", err)
	}
	if len(got) != len(claims) {
		t.Fatalf("found %d political claims after reseed, want %d (must not duplicate)", len(got), len(claims))
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
