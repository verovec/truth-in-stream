package main

import (
	"archive/zip"
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsarchive"
	"github.com/verovec/truth-in-stream/backend/internal/scrutinsjob"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
)

// This test drives the whole scrutins pipeline through a real broker and
// database: the archive producer downloads a fixture zip and publishes one job
// per scrutin, the worker drains the queue and upserts, and a (person, bill,
// date) lookup returns the recorded position. Re-running the producer over the
// unchanged archive is a 304, so no new jobs are published; re-publishing the
// same jobs is idempotent over (person, scrutin). It skips unless both
// TEST_RABBITMQ_URL and TEST_DATABASE_URL are set. A throwaway broker/database is
// required; the schema reset drops tables.

func integrationEnv(t *testing.T) (broker, dsn string) {
	t.Helper()
	broker = os.Getenv("TEST_RABBITMQ_URL")
	dsn = os.Getenv("TEST_DATABASE_URL")
	if broker == "" || dsn == "" {
		t.Skip("set TEST_RABBITMQ_URL and TEST_DATABASE_URL to run the scrutins-worker integration test")
	}
	return broker, dsn
}

func resetSchema(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("reset: connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS claims, documents, videos, wiki_chunks, wiki_chunks_staging, wiki_chunks_old, wiki_sync_state, political_claims, voting_records"); err != nil {
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

// scrutinFixture is one real-shaped inner archive file, mirroring the AN
// open-data wire format the parser handles.
func scrutinFixture() []byte {
	return []byte(`{
  "scrutin": {
    "uid": "VTANR5L17V42",
    "numero": "42",
    "legislature": "17",
    "dateScrutin": "2024-10-15",
    "objet": {"libelle": "sur l'ensemble du projet de loi de finances pour 2025"},
    "ventilationVotes": {"organe": {"groupes": {"groupe": [
      {"vote": {"decompteNominatif": {
        "pours": {"votant": [{"acteurRef": "PA1592"}]},
        "contres": {"votant": {"acteurRef": "PA721002"}}
      }}}
    ]}}}
  }
}`)
}

func archiveBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("VTANR5L17V42.json")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write(scrutinFixture()); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func newQueue(t *testing.T, broker, name string) *queue.Client {
	t.Helper()
	client, err := queue.New(queue.Config{URL: broker, QueueName: name, Version: "1", MaxPriority: 10, Prefetch: 2})
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// waitForRecords polls the (person, bill, date) lookup until it returns at least
// want records or the deadline passes. A real broker has no deterministic
// completion signal, so a bounded poll is the honest way to wait.
func waitForRecords(ctx context.Context, t *testing.T, store *postgres.Store, want int) []recordKey {
	t.Helper()
	votedOn := time.Date(2024, 10, 15, 0, 0, 0, 0, time.UTC)
	bill := "sur l'ensemble du projet de loi de finances pour 2025"
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		got, err := store.LookupVotingRecords(ctx, "PA1592", bill, votedOn)
		if err != nil {
			t.Fatalf("LookupVotingRecords: %v", err)
		}
		if len(got) >= want {
			keys := make([]recordKey, 0, len(got))
			for _, r := range got {
				keys = append(keys, recordKey{r.PersonID, r.ScrutinID, string(r.Position)})
			}
			return keys
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d records, have %d", want, len(got))
		case <-tick.C:
		}
	}
}

type recordKey struct{ person, scrutin, position string }

func runProducer(ctx context.Context, t *testing.T, url, markerPath string, client *queue.Client) scrutinsjob.Config {
	t.Helper()
	c, err := scrutinsarchive.New(scrutinsarchive.Config{
		Legislature: "17",
		MarkerPath:  markerPath,
		MaxPriority: 10,
		URLTemplate: url + "?legislature=%s",
	}, qPublisher{client: client}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("producer New: %v", err)
	}
	if _, err := c.Run(ctx); err != nil {
		t.Fatalf("producer Run: %v", err)
	}
	return scrutinsjob.Config{Concurrency: 2, MaxAttempts: 3, KnownVersions: []string{"1"}}
}

// qPublisher adapts the broker client to the producer's Publisher, mirroring the
// cmd/scrutinscrawl adapter.
type qPublisher struct{ client *queue.Client }

func (p qPublisher) Publish(ctx context.Context, body []byte, priority uint8) error {
	return p.client.Publish(ctx, queue.Message{Body: body, Priority: priority})
}

func TestScrutinsPipelineEndToEnd(t *testing.T) {
	broker, dsn := integrationEnv(t)
	ctx := t.Context()
	resetSchema(ctx, t, dsn)

	srv := &condServer{etag: `"v1"`, body: archiveBytes(t)}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	client := newQueue(t, broker, "scrutins.votes.scrutinsworker_e2e")
	markerPath := filepath.Join(t.TempDir(), "marker.json")

	cfg := runProducer(ctx, t, ts.URL, markerPath, client)

	worker := scrutinsjob.NewWorker(store, qStream{client: client}, qEnqueuer{client: client},
		slog.New(slog.DiscardHandler), cfg)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()

	keys := waitForRecords(ctx, t, store, 1)
	if len(keys) != 1 || keys[0] != (recordKey{"PA1592", "VTANR5L17V42", "for"}) {
		t.Fatalf("lookup returned %+v, want one PA1592/VTANR5L17V42/for", keys)
	}

	// Re-running the producer over the unchanged archive is a 304: no new jobs.
	if srv.bodyServed() != 1 {
		t.Fatalf("body served %d times, want 1", srv.bodyServed())
	}
	runProducer(ctx, t, ts.URL, markerPath, client)
	if srv.bodyServed() != 1 {
		t.Fatalf("body served %d times after unchanged re-run, want still 1", srv.bodyServed())
	}

	// Re-publishing the same scrutin (forced via no marker) is idempotent over
	// (person, scrutin): the lookup still returns exactly one row.
	runProducer(ctx, t, ts.URL, "", client)
	time.Sleep(500 * time.Millisecond)
	again := waitForRecords(ctx, t, store, 1)
	if len(again) != 1 {
		t.Fatalf("after re-publish, lookup returned %d rows, want 1 (idempotent)", len(again))
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker Run: %v", err)
	}
}

// condServer serves the archive with an ETag and honors If-None-Match with a 304.
type condServer struct {
	etag  string
	body  []byte
	count int
}

func (s *condServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", s.etag)
		if r.Header.Get("If-None-Match") == s.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.count++
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(s.body)
	}
}

func (s *condServer) bodyServed() int { return s.count }
