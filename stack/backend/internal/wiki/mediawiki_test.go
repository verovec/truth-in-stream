package wiki

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *APIClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &APIClient{Corpus: "simplewiki", BaseURL: srv.URL, retryBase: time.Millisecond}
}

// rtFunc adapts a function to http.RoundTripper so a test can drive transport
// behavior (transient errors, cancellation) that an httptest.Server cannot.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTransportClient(rt http.RoundTripper) *APIClient {
	return &APIClient{
		Corpus:     "simplewiki",
		BaseURL:    "http://wiki.invalid/w/api.php",
		HTTPClient: &http.Client{Transport: rt},
		retryBase:  time.Millisecond,
	}
}

func okExtractsBody() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"query":{"pages":{}}}`)),
		Header:     http.Header{},
	}
}

func TestExtractsRetriesTransientTransportError(t *testing.T) {
	t.Parallel()

	var calls int32
	c := newTransportClient(rtFunc(func(*http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			// A header timeout / connection reset: the server gave no answer.
			return nil, errors.New("dial tcp: connection reset by peer")
		}
		return okExtractsBody(), nil
	}))

	if _, err := c.Extracts(t.Context(), []string{"Atom"}); err != nil {
		t.Fatalf("Extracts after transient transport errors: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("made %d calls, want 3 (two transient failures then success)", got)
	}
}

func TestExtractsGivesUpOnPersistentTransportError(t *testing.T) {
	t.Parallel()

	var calls int32
	c := newTransportClient(rtFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("i/o timeout")
	}))

	if _, err := c.Extracts(t.Context(), []string{"Atom"}); err == nil {
		t.Fatal("Extracts under a sustained transport error succeeded, want error")
	}
	if got := atomic.LoadInt32(&calls); got != apiMaxRetries+1 {
		t.Errorf("made %d attempts, want %d (initial + retries)", got, apiMaxRetries+1)
	}
}

func TestExtractsDoesNotRetryOnContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	var calls int32
	c := newTransportClient(rtFunc(func(*http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		cancel() // operator SIGINT mid-flight
		return nil, errors.New("request canceled")
	}))

	if _, err := c.Extracts(ctx, []string{"Atom"}); err == nil {
		t.Fatal("Extracts after context cancel succeeded, want error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("made %d calls, want 1 (cancellation is terminal, not retried)", got)
	}
}

func TestRecentChangesParsesAndFiltersAndPaginates(t *testing.T) {
	t.Parallel()

	var calls int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		q := r.URL.Query()
		if q.Get("rcnamespace") != "0" || q.Get("rcdir") != "newer" {
			t.Errorf("call %d: bad params: ns=%q dir=%q", n, q.Get("rcnamespace"), q.Get("rcdir"))
		}
		if q.Get("format") != "json" || q.Get("maxlag") != "5" {
			t.Errorf("call %d: missing format/maxlag: %v", n, q)
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("call %d: User-Agent = %q", n, r.Header.Get("User-Agent"))
		}
		if n == 1 {
			if q.Get("rcstart") != "2026-06-01T00:00:00Z" {
				t.Errorf("call 1 rcstart = %q", q.Get("rcstart"))
			}
			// First page: an edit, a non-delete log (move) to ignore, and a
			// continuation token. A deletion and a new page follow on page two.
			_, _ = w.Write([]byte(`{
				"continue": {"rccontinue": "20260602|99", "continue": "-||"},
				"query": {"recentchanges": [
					{"type": "edit", "ns": 0, "pageid": 1, "revid": 200, "title": "Paris", "timestamp": "2026-06-02T10:00:00Z"},
					{"type": "log", "ns": 0, "pageid": 5, "title": "Berlin", "timestamp": "2026-06-02T11:00:00Z", "logtype": "move", "logaction": "move"}
				]}
			}`))
			return
		}
		if q.Get("rccontinue") != "20260602|99" {
			t.Errorf("call 2 did not carry continuation: %q", q.Get("rccontinue"))
		}
		_, _ = w.Write([]byte(`{
			"query": {"recentchanges": [
				{"type": "new", "ns": 0, "pageid": 7, "revid": 300, "title": "Lyon", "timestamp": "2026-06-03T09:00:00Z"},
				{"type": "log", "ns": 0, "pageid": 0, "revid": 0, "title": "Atlantis", "timestamp": "2026-06-03T12:00:00Z", "logtype": "delete", "logaction": "delete"}
			]}
		}`))
	})

	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	changes, err := client.RecentChanges(t.Context(), since)
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("made %d calls, want 2 (continuation followed)", calls)
	}

	want := []Change{
		{PageID: 1, Title: "Paris", RevisionID: 200, Timestamp: time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)},
		{PageID: 7, Title: "Lyon", RevisionID: 300, Timestamp: time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)},
		{Title: "Atlantis", Timestamp: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC), Deleted: true},
	}
	if len(changes) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(changes), len(want), changes)
	}
	for i, w := range want {
		if !changes[i].Timestamp.Equal(w.Timestamp) {
			t.Errorf("change %d timestamp = %v, want %v", i, changes[i].Timestamp, w.Timestamp)
		}
		changes[i].Timestamp, w.Timestamp = time.Time{}, time.Time{}
		if changes[i] != w {
			t.Errorf("change %d = %+v, want %+v", i, changes[i], w)
		}
	}
}

func TestRecentChangesRetriesOnThrottle(t *testing.T) {
	t.Parallel()

	var calls int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"query": {"recentchanges": []}}`))
	})

	if _, err := client.RecentChanges(t.Context(), time.Now().UTC()); err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("made %d calls, want 2 (retried after 503)", calls)
	}
}

func TestRecentChangesTreatsRestoreAsChange(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"query": {"recentchanges": [
				{"type": "log", "ns": 0, "pageid": 0, "title": "Phoenix", "timestamp": "2026-06-02T10:00:00Z", "logtype": "delete", "logaction": "delete"},
				{"type": "log", "ns": 0, "pageid": 42, "title": "Phoenix", "timestamp": "2026-06-02T11:00:00Z", "logtype": "delete", "logaction": "restore"}
			]}
		}`))
	})

	changes, err := client.RecentChanges(t.Context(), time.Now().UTC())
	if err != nil {
		t.Fatalf("RecentChanges: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2 (delete + restore)", len(changes))
	}
	restore := changes[1]
	if restore.Deleted || restore.PageID != 42 || restore.Title != "Phoenix" {
		t.Errorf("restore parsed as %+v, want a live change for Phoenix (42)", restore)
	}
}

func TestRecentChangesGivesUpOnPersistentThrottle(t *testing.T) {
	t.Parallel()

	var calls int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if _, err := client.RecentChanges(t.Context(), time.Now().UTC()); err == nil {
		t.Fatal("RecentChanges under sustained 503 succeeded, want error")
	}
	// One initial attempt plus apiMaxRetries; never more (no sleep past the last).
	if got := atomic.LoadInt32(&calls); got != apiMaxRetries+1 {
		t.Errorf("made %d attempts, want %d", got, apiMaxRetries+1)
	}
}

func TestRecentChangesSurfacesNon200(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	if _, err := client.RecentChanges(t.Context(), time.Now().UTC()); err == nil {
		t.Fatal("RecentChanges over a 400 succeeded, want error")
	}
}

func TestExtractsParsesLiveAndMissing(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("explaintext") != "1" || q.Get("exintro") != "1" || q.Get("rvprop") != "ids" {
			t.Errorf("bad extract params: %v", q)
		}
		_, _ = w.Write([]byte(`{
			"query": {"pages": {
				"1": {"pageid": 1, "title": "Paris", "extract": "Paris is the capital of France.", "revisions": [{"revid": 201}]},
				"-1": {"ns": 0, "title": "Atlantis", "missing": ""}
			}}
		}`))
	})

	got, err := client.Extracts(t.Context(), []string{"Paris", "Atlantis"})
	if err != nil {
		t.Fatalf("Extracts: %v", err)
	}
	byTitle := map[string]Extract{}
	for _, e := range got {
		byTitle[e.Title] = e
	}
	paris := byTitle["Paris"]
	if paris.PageID != 1 || paris.RevisionID != 201 || !strings.Contains(paris.Text, "capital of France") {
		t.Errorf("Paris extract = %+v", paris)
	}
	if atlantis := byTitle["Atlantis"]; !atlantis.Missing {
		t.Errorf("Atlantis extract = %+v, want Missing", atlantis)
	}
}

func TestExtractsBatchesByLimit(t *testing.T) {
	t.Parallel()

	var calls int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if n := len(strings.Split(r.URL.Query().Get("titles"), "|")); n > extractsBatchMax {
			t.Errorf("request carried %d titles, over the %d cap", n, extractsBatchMax)
		}
		_, _ = w.Write([]byte(`{"query": {"pages": {}}}`))
	})

	titles := make([]string, extractsBatchMax+5)
	for i := range titles {
		titles[i] = "T" + strings.Repeat("x", i)
	}
	if _, err := client.Extracts(t.Context(), titles); err != nil {
		t.Fatalf("Extracts: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("made %d requests for %d titles, want 2 batches", calls, len(titles))
	}
}

func TestRetryAfterHonoursHeaderAndCap(t *testing.T) {
	t.Parallel()

	c := &APIClient{}
	h := http.Header{}
	h.Set("Retry-After", "3")
	if got := c.retryAfter(h); got != 3*time.Second {
		t.Errorf("retryAfter(3) = %v, want 3s", got)
	}
	h.Set("Retry-After", "100000")
	if got := c.retryAfter(h); got != apiMaxRetryWait {
		t.Errorf("retryAfter(huge) = %v, want cap %v", got, apiMaxRetryWait)
	}
	if got := c.retryAfter(http.Header{}); got != apiRetryBase {
		t.Errorf("retryAfter(absent) = %v, want base %v", got, apiRetryBase)
	}
}

func TestFullExtractsReturnsBodyTextWithoutExintro(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("exintro") != "" {
			t.Errorf("FullExtracts must not set exintro, got %q", q.Get("exintro"))
		}
		if q.Get("explaintext") != "1" {
			t.Errorf("explaintext = %q, want 1", q.Get("explaintext"))
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"10":{"pageid":10,"title":"Atom","extract":"Atom intro.\n\nBody section text.","revisions":[{"revid":99}]}}}}`))
	})
	got, err := c.FullExtracts(t.Context(), []string{"Atom"})
	if err != nil {
		t.Fatalf("FullExtracts: %v", err)
	}
	if len(got) != 1 || got[0].PageID != 10 || got[0].RevisionID != 99 {
		t.Fatalf("got %+v, want pageid 10 rev 99", got)
	}
	if got[0].Text != "Atom intro.\n\nBody section text." {
		t.Errorf("text = %q", got[0].Text)
	}
}

func TestExtractsStillSetsExintro(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("exintro") != "1" {
			t.Errorf("Extracts must set exintro=1, got %q", r.URL.Query().Get("exintro"))
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"10":{"pageid":10,"title":"Atom","extract":"Atom intro.","revisions":[{"revid":99}]}}}}`))
	})
	got, err := c.Extracts(t.Context(), []string{"Atom"})
	if err != nil {
		t.Fatalf("Extracts: %v", err)
	}
	if len(got) != 1 || got[0].Text != "Atom intro." {
		t.Fatalf("got %+v, want lead-only text", got)
	}
}
