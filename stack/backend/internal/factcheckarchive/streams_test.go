package factcheckarchive

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestBuildStreams(t *testing.T) {
	t.Parallel()
	streams := BuildStreams(Strategy{
		Topics:         []string{"retraites", "", "retraites", "immigration"},
		PublisherSites: []string{"lemonde.fr", "", "lemonde.fr", "factuel.afp.com"},
		MaxPages:       2,
		MaxAgeDays:     30,
	})
	// Blank and duplicate entries are dropped: 2 topics + 2 publisher sites.
	if len(streams) != 4 {
		t.Fatalf("built %d streams, want 4: %+v", len(streams), streams)
	}
	if streams[0].Query != "retraites" || streams[0].PublisherSite != "" {
		t.Errorf("stream[0] = %+v, want the retraites topic", streams[0])
	}
	// Publisher streams carry no topic query, so they page the outlet's full catalog.
	last := streams[3]
	if last.PublisherSite != "factuel.afp.com" || last.Query != "" {
		t.Errorf("stream[3] = %+v, want the factuel.afp.com publisher stream", last)
	}
	for _, s := range streams {
		if s.MaxPages != 2 || s.MaxAgeDays != 30 || s.StreamKey == "" {
			t.Errorf("stream missing config/key: %+v", s)
		}
	}
}

// paramSpy records the query parameters of every request so a test can assert the
// publisher-scoped stream sends reviewPublisherSiteFilter and no query term.
type paramSpy struct {
	mu     sync.Mutex
	params []map[string]string
}

func (s *paramSpy) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]string{}
	for k, v := range r.URL.Query() {
		m[k] = v[0]
	}
	s.params = append(s.params, m)
}

func TestRunPublisherScopedStreamSendsFilterAndNoQuery(t *testing.T) {
	t.Parallel()
	spy := &paramSpy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spy.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"claims":[],"nextPageToken":""}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	if _, err := c.Run(t.Context(), nil, &recordingPublisher{}, RunConfig{PublisherSite: "lemonde.fr", MaxAgeDays: 45}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(spy.params) != 1 {
		t.Fatalf("made %d requests, want 1", len(spy.params))
	}
	p := spy.params[0]
	if p["reviewPublisherSiteFilter"] != "lemonde.fr" {
		t.Errorf("reviewPublisherSiteFilter = %q, want lemonde.fr", p["reviewPublisherSiteFilter"])
	}
	if _, hasQuery := p["query"]; hasQuery {
		t.Errorf("publisher-scoped stream sent a query param: %v", p)
	}
	if p["languageCode"] != "fr" {
		t.Errorf("languageCode = %q, want fr", p["languageCode"])
	}
	if p["maxAgeDays"] != "45" {
		t.Errorf("maxAgeDays = %q, want 45", p["maxAgeDays"])
	}
}

func TestRunStreamsSkipsCheckpointedAndClearsOnCompletion(t *testing.T) {
	t.Parallel()
	spy := &paramSpy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spy.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"claims":[],"nextPageToken":""}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	cp := &memCheckpoint{done: map[string]struct{}{}}
	// Pre-mark the first stream done: a resumed run must skip it.
	streams := BuildStreams(Strategy{Topics: []string{"retraites", "immigration"}, PublisherSites: []string{"lemonde.fr"}})
	cp.MarkDone(streams[0].key())

	_, err := c.RunStreams(t.Context(), nil, &recordingPublisher{}, streams, cp)
	if err != nil {
		t.Fatalf("RunStreams: %v", err)
	}
	// 3 streams, first skipped -> 2 requests.
	if len(spy.params) != 2 {
		t.Fatalf("made %d requests, want 2 (first stream checkpointed)", len(spy.params))
	}
	// Every stream drained -> the two remaining are marked done.
	if !cp.Done(streams[1].key()) || !cp.Done(streams[2].key()) {
		t.Errorf("undrained streams after a full run: %+v", cp.done)
	}
}

// TestRunStreamsStopsAtErroringStreamWithPartialCount restores the coverage the old
// per-query stop test provided: RunStreams walks streams in order, and the first
// stream that errors stops the walk, returning the counts gathered so far while a
// later stream is never requested and the checkpoint keeps only the completed one.
func TestRunStreamsStopsAtErroringStreamWithPartialCount(t *testing.T) {
	t.Parallel()
	const oneClaim = `{"claims":[{"text":"claim","claimReview":[{"publisher":{"name":"AFP","site":"factuel.afp.com"},"url":"https://factuel.afp.com/x","textualRating":"Faux"}]}],"nextPageToken":""}`
	spy := &paramSpy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spy.record(r)
		if r.URL.Query().Get("query") == "b" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(oneClaim))
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	cp := &memCheckpoint{done: map[string]struct{}{}}
	streams := BuildStreams(Strategy{Topics: []string{"a", "b", "c"}})
	pub := &recordingPublisher{}

	stats, err := c.RunStreams(t.Context(), nil, pub, streams, cp)
	if err == nil {
		t.Fatal("RunStreams returned nil, want the erroring-stream error")
	}
	// Stream "a" published its one claim before "b" failed; "c" never ran.
	if stats.Published != 1 {
		t.Errorf("published = %d, want the partial count 1", stats.Published)
	}
	if !cp.Done(streams[0].key()) {
		t.Error("completed stream a not checkpointed")
	}
	if cp.Done(streams[1].key()) || cp.Done(streams[2].key()) {
		t.Error("erroring or unreached stream wrongly checkpointed")
	}
	for _, p := range spy.params {
		if p["query"] == "c" {
			t.Error("stream c was requested despite the earlier error")
		}
	}
}

// memCheckpoint is an in-memory StreamCheckpoint for tests.
type memCheckpoint struct {
	mu   sync.Mutex
	done map[string]struct{}
}

func (m *memCheckpoint) Done(k string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.done[k]
	return ok
}

func (m *memCheckpoint) MarkDone(k string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done[k] = struct{}{}
}

func (m *memCheckpoint) Save() error { return nil }

func (m *memCheckpoint) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = map[string]struct{}{}
	return nil
}
