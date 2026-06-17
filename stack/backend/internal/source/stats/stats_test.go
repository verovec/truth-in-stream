package stats

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/source"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func TestRetrieveINSEESeries(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "insee_series.xml")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "001688526") {
			t.Errorf("unexpected INSEE path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack := New(Config{INSEEBaseURL: srv.URL})
	ev, err := pack.Retrieve(t.Context(), source.Query{
		Text:  "le chomage est a 7,3%",
		Hints: map[string]string{HintINSEEIDBANK: "001688526"},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 1 {
		t.Fatalf("want 1 evidence, got %d", len(ev))
	}
	got := ev[0]

	if got.Source.Name != inseeSourceName {
		t.Errorf("source name: got %q", got.Source.Name)
	}
	if got.Source.URL == "" {
		t.Errorf("source url empty")
	}
	if got.Source.Date != "2026-05-15" {
		t.Errorf("source date: got %q", got.Source.Date)
	}

	// A series, not a point: every period must be present and chronological.
	for _, period := range []string{"2023-Q3", "2023-Q4", "2024-Q1", "2024-Q2", "2024-Q3", "2024-Q4"} {
		if !strings.Contains(got.Passage, period) {
			t.Errorf("passage missing period %s:\n%s", period, got.Passage)
		}
	}
	// Earliest period before latest in the rendered order (chronological).
	if strings.Index(got.Passage, "2023-Q3") > strings.Index(got.Passage, "2024-Q4") {
		t.Errorf("periods not chronological:\n%s", got.Passage)
	}
	// The cited figure is present.
	if !strings.Contains(got.Passage, "7.3") {
		t.Errorf("passage missing cited value 7.3:\n%s", got.Passage)
	}
	// A NaN period is surfaced as unavailable, not dropped.
	if !strings.Contains(got.Passage, "indisponible") {
		t.Errorf("missing period not surfaced:\n%s", got.Passage)
	}

	// evidence_id round-trips.
	roundTripped, err := source.ParseEvidenceID(got.ID.String())
	if err != nil {
		t.Fatalf("ParseEvidenceID: %v", err)
	}
	if roundTripped != got.ID {
		t.Errorf("evidence_id round trip: got %+v want %+v", roundTripped, got.ID)
	}
	if got.ID.Kind != source.KindStatsINSEE || got.ID.SourceID != "001688526" {
		t.Errorf("evidence_id components: got %+v", got.ID)
	}
}

func TestRetrieveEurostatSeries(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "eurostat_series.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "une_rt_a") {
			t.Errorf("unexpected Eurostat path %q", r.URL.Path)
		}
		if r.URL.Query().Get("geo") != "FR" {
			t.Errorf("missing geo filter, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack := New(Config{EurostatBaseURL: srv.URL})
	ev, err := pack.Retrieve(t.Context(), source.Query{
		Text: "le chomage en France",
		Hints: map[string]string{
			HintEurostatDataset:               "une_rt_a",
			HintEurostatFilterPrefix + "geo":  "FR",
			HintEurostatFilterPrefix + "freq": "A",
		},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 1 {
		t.Fatalf("want 1 evidence, got %d", len(ev))
	}
	got := ev[0]

	if got.Source.Name != eurostatSourceName {
		t.Errorf("source name: got %q", got.Source.Name)
	}
	// The decoded values must match the sparse value map (flat-index decode):
	// 2020->8.0, 2021->7.9, 2022->7.3, 2023->missing, 2024->7.4.
	for _, want := range []string{"2020: 8", "2021: 7.9", "2022: 7.3", "2024: 7.4"} {
		if !strings.Contains(got.Passage, want) {
			t.Errorf("passage missing %q:\n%s", want, got.Passage)
		}
	}
	if !strings.Contains(got.Passage, "2023: indisponible") {
		t.Errorf("missing 2023 not surfaced:\n%s", got.Passage)
	}
	if got.ID.Kind != source.KindStatsEurostat || got.ID.SourceID != "une_rt_a" {
		t.Errorf("evidence_id components: got %+v", got.ID)
	}
}

func TestRetrieveCachesSeries(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "insee_series.xml")
	var calls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack := New(Config{INSEEBaseURL: srv.URL})
	q := source.Query{Hints: map[string]string{HintINSEEIDBANK: "001688526"}}

	for range 3 {
		if _, err := pack.Retrieve(t.Context(), q); err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cache not enforced: want 1 upstream call, got %d", got)
	}
}

func TestRetrieveCacheExpires(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "insee_series.xml")
	var calls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack := New(Config{INSEEBaseURL: srv.URL, CacheTTL: time.Nanosecond})
	q := source.Query{Hints: map[string]string{HintINSEEIDBANK: "001688526"}}

	for range 2 {
		if _, err := pack.Retrieve(t.Context(), q); err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expired entry not refetched: got %d calls", got)
	}
}

func TestRetrieveTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte("<too late/>"))
	}))
	defer srv.Close()
	defer close(release)

	pack := New(Config{INSEEBaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	_, err := pack.Retrieve(t.Context(), source.Query{
		Hints: map[string]string{HintINSEEIDBANK: "001688526"},
	})
	if err == nil {
		t.Fatalf("want timeout error, got nil")
	}
}

func TestRetrieveNoHintsReturnsNothing(t *testing.T) {
	t.Parallel()
	pack := New(Config{})
	ev, err := pack.Retrieve(t.Context(), source.Query{Text: "une affirmation generale"})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 0 {
		t.Fatalf("want no evidence, got %d", len(ev))
	}
}

func TestRetrieveBothSources(t *testing.T) {
	t.Parallel()
	inseeFixture := readFixture(t, "insee_series.xml")
	eurostatFixture := readFixture(t, "eurostat_series.json")

	insee := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(inseeFixture)
	}))
	defer insee.Close()
	eurostat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(eurostatFixture)
	}))
	defer eurostat.Close()

	pack := New(Config{INSEEBaseURL: insee.URL, EurostatBaseURL: eurostat.URL})
	ev, err := pack.Retrieve(t.Context(), source.Query{
		Hints: map[string]string{
			HintINSEEIDBANK:                  "001688526",
			HintEurostatDataset:              "une_rt_a",
			HintEurostatFilterPrefix + "geo": "FR",
		},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 2 {
		t.Fatalf("want 2 evidence, got %d", len(ev))
	}
	if ev[0].ID.Index == ev[1].ID.Index {
		t.Errorf("evidence indices not distinct: %+v", ev)
	}
}

func TestPackKindIsAdapterLevel(t *testing.T) {
	t.Parallel()
	if got := New(Config{}).Kind(); got != source.KindStats {
		t.Fatalf("Kind: got %q want %q", got, source.KindStats)
	}
}

func TestRetrieveBothFailJoinsErrors(t *testing.T) {
	t.Parallel()
	insee := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer insee.Close()
	eurostat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer eurostat.Close()

	pack := New(Config{INSEEBaseURL: insee.URL, EurostatBaseURL: eurostat.URL})
	_, err := pack.Retrieve(t.Context(), source.Query{
		Hints: map[string]string{
			HintINSEEIDBANK:                  "001688526",
			HintEurostatDataset:              "une_rt_a",
			HintEurostatFilterPrefix + "geo": "FR",
		},
	})
	if err == nil {
		t.Fatalf("want error when both sources fail")
	}
	// Both failures must be visible, not just the first.
	if !strings.Contains(err.Error(), "INSEE") || !strings.Contains(err.Error(), "Eurostat") {
		t.Fatalf("joined error must name both sources: %v", err)
	}
}

func TestRetrieveStableIndexUnderPartialFailure(t *testing.T) {
	t.Parallel()
	eurostatFixture := readFixture(t, "eurostat_series.json")

	// INSEE down, Eurostat up: the Eurostat evidence must keep its stable index
	// regardless of the INSEE failure, so the EvidenceID is deterministic.
	insee := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer insee.Close()
	eurostat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(eurostatFixture)
	}))
	defer eurostat.Close()

	pack := New(Config{INSEEBaseURL: insee.URL, EurostatBaseURL: eurostat.URL})
	ev, err := pack.Retrieve(t.Context(), source.Query{
		Hints: map[string]string{
			HintINSEEIDBANK:                  "001688526",
			HintEurostatDataset:              "une_rt_a",
			HintEurostatFilterPrefix + "geo": "FR",
		},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(ev) != 1 {
		t.Fatalf("want 1 evidence (only Eurostat), got %d", len(ev))
	}
	if ev[0].ID.Index != eurostatEvidenceIndex {
		t.Fatalf("eurostat index not stable under INSEE failure: got %d want %d", ev[0].ID.Index, eurostatEvidenceIndex)
	}
}

func TestRetrieveCoalescesConcurrentMisses(t *testing.T) {
	t.Parallel()
	fixture := readFixture(t, "insee_series.xml")
	var calls atomic.Int64
	gate := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-gate // hold every handler open so all callers miss the cache together
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	pack := New(Config{INSEEBaseURL: srv.URL})
	q := source.Query{Hints: map[string]string{HintINSEEIDBANK: "001688526"}}

	const n = 8
	done := make(chan error, n)
	for range n {
		go func() {
			_, err := pack.Retrieve(t.Context(), q)
			done <- err
		}()
	}
	// Give the goroutines time to all enter the cache miss, then release.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	for range n {
		if err := <-done; err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent misses not coalesced: want 1 upstream call, got %d", got)
	}
}

func TestRetrieveDialFailureSurfaces(t *testing.T) {
	t.Parallel()
	// An unreachable upstream surfaces as an error rather than empty evidence.
	// The load runs under a cancellation-detached context (so a coalesced
	// caller's cancellation cannot abort the shared flight), so the dial error,
	// not the context, is what must reach the caller here.
	pack := New(Config{INSEEBaseURL: "http://127.0.0.1:0", Timeout: 500 * time.Millisecond})
	if _, err := pack.Retrieve(t.Context(), source.Query{
		Hints: map[string]string{HintINSEEIDBANK: "x"},
	}); err == nil {
		t.Fatalf("want error on unreachable upstream")
	}
}
