package postgres

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/embedjob"
)

// This file wires the real embedjob.Worker to the real Postgres store and drives
// the bulk-into-live path end to end: published jobs -> batched embed -> in-place
// live write -> searchable mid-drain. It uses a deterministic embedder instead of
// paying Voyage, so the production code paths (Worker.Run, SetLiveChunkEmbeddings,
// SearchEvidence) are exercised exactly as in a real run.

type e2eEmbedder struct{ calls atomic.Int32 }

// EmbedDocuments returns a deterministic unit vector per input (hot index varies
// by content length so distinct chunks get distinct vectors), counting calls so
// the test can prove the whole batch embedded in one round-trip.
func (e *e2eEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	out := make([][]float32, len(texts))
	for i, txt := range texts {
		v := make([]float32, domain.EmbeddingDim)
		v[len(txt)%domain.EmbeddingDim] = 1
		out[i] = v
	}
	return out, nil
}

// Minimal Delivery/Stream/Enqueuer fakes (the worker is transport-free; these
// stand in for the broker). The worker acks each delivery synchronously before
// Run returns, so plain fields need no locking.
type e2eDelivery struct {
	body   []byte
	acked  bool
	nacked bool
}

func (d *e2eDelivery) Body() []byte      { return d.body }
func (d *e2eDelivery) Priority() uint8   { return 5 }
func (d *e2eDelivery) Version() string   { return "" }
func (d *e2eDelivery) Ack() error        { d.acked = true; return nil }
func (d *e2eDelivery) Nack(_ bool) error { d.nacked = true; return nil }

type e2eStream struct{ deliveries []embedjob.Delivery }

func (s *e2eStream) Consume(_ context.Context) (<-chan embedjob.Delivery, error) {
	out := make(chan embedjob.Delivery)
	go func() {
		defer close(out)
		for _, d := range s.deliveries {
			out <- d
		}
	}()
	return out, nil
}

type e2eEnqueuer struct{}

func (e2eEnqueuer) Enqueue(context.Context, []byte, uint8) error { return nil }

func TestBulkIntoLiveEndToEndQueryableMidDrain(t *testing.T) {
	store := setupStore(t)
	ctx := t.Context()

	// Ingest five chunks straight into the live table (embedding NULL), as the
	// bulk-into-live producer does.
	chunks := []domain.EvidenceChunk{
		wikiChunk(1, 0, "alpha one"),
		wikiChunk(2, 0, "bravo two two"),
		wikiChunk(3, 0, "charlie three three three"),
		wikiChunk(4, 0, "delta four"),
		wikiChunk(5, 0, "echo five five"),
	}
	seedChunks(t, store, chunks)

	// Publish jobs for only the first three chunks, simulating a fleet that has
	// drained part of the queue while the rest is still pending.
	deliveries := make([]embedjob.Delivery, 0, 3)
	for _, c := range chunks[:3] {
		body, err := json.Marshal(embedjob.Job{Source: c.Source, ExternalID: c.ExternalID, ChunkIndex: c.ChunkIndex, Content: c.Content})
		if err != nil {
			t.Fatalf("marshal job: %v", err)
		}
		deliveries = append(deliveries, &e2eDelivery{body: body})
	}

	emb := &e2eEmbedder{}
	worker := embedjob.NewWorker(emb, store, &e2eStream{deliveries}, e2eEnqueuer{}, slog.New(slog.DiscardHandler),
		embedjob.Config{Concurrency: 1, BatchSize: 16, MaxAttempts: 3})
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("worker Run: %v", err)
	}

	// Throughput property: the three chunks embedded in a single provider call.
	if got := emb.calls.Load(); got != 1 {
		t.Fatalf("embed calls = %d, want 1 (the batch embeds in one round-trip)", got)
	}

	// Availability property: the embedded chunks are now searchable in the LIVE
	// corpus, mid-drain, with no swap - while the un-embedded chunks 4 and 5 stay
	// invisible until their vectors land.
	hot := make([]float32, domain.EmbeddingDim)
	hot[len("alpha one")%domain.EmbeddingDim] = 1
	got, err := store.SearchEvidence(ctx, hot, 5, 0, nil)
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("search returned %d chunks, want 3 (only the embedded prefix is visible mid-drain)", len(got))
	}
	for _, ev := range got {
		if ev.Content == "delta four" || ev.Content == "echo five five" {
			t.Errorf("un-embedded chunk %q is searchable; NULL rows must be invisible", ev.Content)
		}
	}

	// The corpus grew monotonically in place: 2 chunks remain un-embedded.
	if n, err := store.CountUnembeddedLive(ctx); err != nil || n != 2 {
		t.Fatalf("CountUnembeddedLive = %d, %v; want 2 still pending", n, err)
	}

	// Every processed delivery was acked (none nacked back to the broker).
	for i, d := range deliveries {
		dd := d.(*e2eDelivery)
		if !dd.acked || dd.nacked {
			t.Errorf("delivery %d acked=%v nacked=%v, want acked", i, dd.acked, dd.nacked)
		}
	}
}
