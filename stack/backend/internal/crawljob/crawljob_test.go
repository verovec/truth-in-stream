package crawljob

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

type fakeEmbedder struct {
	vec [][]float32
	err error
}

func (f fakeEmbedder) EmbedDocuments(_ context.Context, _ []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

type fakeStore struct {
	got domain.WikiChunk
	err error
}

func (f *fakeStore) UpsertEmbeddedChunk(_ context.Context, c domain.WikiChunk) error {
	f.got = c
	return f.err
}

func fullVec() []float32 { return make([]float32, domain.EmbeddingDim) }

func validJob() CrawlJob {
	return CrawlJob{
		PageID: 5, ChunkIndex: 1, Title: "Atom", URL: "u", RevisionID: 9,
		Corpus: "simplewiki-crawl", Content: "Atom\n\ntext", Section: "", Kind: "body",
	}
}

func mustBody(t *testing.T, j CrawlJob) []byte {
	t.Helper()
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestProcessHappyPathUpserts(t *testing.T) {
	st := &fakeStore{}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{Concurrency: 1, MaxAttempts: 3})
	res := w.Process(t.Context(), mustBody(t, validJob()), 5)
	if res.Action != ActionAck {
		t.Fatalf("action = %v, want Ack", res.Action)
	}
	if st.got.PageID != 5 || st.got.Kind != domain.WikiChunkKindBody || len(st.got.Embedding) != domain.EmbeddingDim {
		t.Errorf("upserted chunk wrong: %+v", st.got)
	}
	if st.got.Title != "Atom" || st.got.URL != "u" || st.got.RevisionID != 9 || st.got.Corpus != "simplewiki-crawl" {
		t.Errorf("upserted chunk metadata wrong: %+v", st.got)
	}
}

func TestProcessMalformedIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	if res := w.Process(t.Context(), []byte("{not json"), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessInvalidJobIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{}, &fakeStore{}, nil, nil, nil, Config{})
	bad := validJob()
	bad.Content = ""
	if res := w.Process(t.Context(), mustBody(t, bad), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessWrongDimIsDropped(t *testing.T) {
	w := NewWorker(fakeEmbedder{vec: [][]float32{{0.1, 0.2}}}, &fakeStore{}, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(t.Context(), mustBody(t, validJob()), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop)", res.Action)
	}
}

func TestProcessTransientFailureRepublishes(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 3})
	res := w.Process(t.Context(), mustBody(t, validJob()), 7)
	if res.Action != ActionRepublish || res.RepublishPriority != 7 {
		t.Fatalf("action=%v prio=%d, want Republish @7", res.Action, res.RepublishPriority)
	}
	var retried CrawlJob
	if err := json.Unmarshal(res.RepublishBody, &retried); err != nil {
		t.Fatalf("unmarshal retry: %v", err)
	}
	if retried.Attempt != 1 {
		t.Errorf("retry attempt = %d, want 1", retried.Attempt)
	}
}

func TestProcessShutdownRequeues(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(ctx, mustBody(t, validJob()), 0); res.Action != ActionRequeue {
		t.Errorf("action = %v, want Requeue on shutdown", res.Action)
	}
}

func TestProcessExhaustedAttemptsDropped(t *testing.T) {
	st := &fakeStore{err: errors.New("db down")}
	w := NewWorker(fakeEmbedder{vec: [][]float32{fullVec()}}, st, nil, nil, nil, Config{MaxAttempts: 2})
	j := validJob()
	j.Attempt = 1 // already at budget-1
	if res := w.Process(t.Context(), mustBody(t, j), 0); res.Action != ActionAck {
		t.Errorf("action = %v, want Ack (drop after retries)", res.Action)
	}
}

func TestProcessEmbedErrorRepublishes(t *testing.T) {
	w := NewWorker(fakeEmbedder{err: errors.New("voyage 429")}, &fakeStore{}, nil, nil, nil, Config{MaxAttempts: 3})
	if res := w.Process(t.Context(), mustBody(t, validJob()), 4); res.Action != ActionRepublish {
		t.Errorf("action = %v, want Republish on transient embed error", res.Action)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*CrawlJob)
		ok   bool
	}{
		{"valid", func(*CrawlJob) {}, true},
		{"page id zero", func(j *CrawlJob) { j.PageID = 0 }, false},
		{"negative index", func(j *CrawlJob) { j.ChunkIndex = -1 }, false},
		{"empty content", func(j *CrawlJob) { j.Content = "" }, false},
		{"empty corpus", func(j *CrawlJob) { j.Corpus = "" }, false},
		{"bad kind", func(j *CrawlJob) { j.Kind = "sidebar" }, false},
		{"lead kind ok", func(j *CrawlJob) { j.Kind = "lead" }, true},
		{"negative revision", func(j *CrawlJob) { j.RevisionID = -1 }, false},
		{"negative attempt", func(j *CrawlJob) { j.Attempt = -1 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := validJob()
			tc.mut(&j)
			if err := j.validate(); (err == nil) != tc.ok {
				t.Errorf("validate() err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}
