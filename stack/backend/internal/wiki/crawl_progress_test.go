package wiki

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// capturedLog is one slog record reduced to the fields the progress tests assert.
type capturedLog struct {
	msg   string
	attrs map[string]any
}

// captureHandler records every slog record into a shared slice so a test can
// assert which progress lines the enumeration emitted. CategoryMembers logs from
// a single goroutine, but -race runs the suite, so the mutex keeps the recorder
// honest regardless.
type captureHandler struct {
	mu   *sync.Mutex
	logs *[]capturedLog
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := capturedLog{msg: r.Message, attrs: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		e.attrs[a.Key] = a.Value.Any()
		return true
	})
	*h.logs = append(*h.logs, e)
	return nil
}

func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }

// captureLogger returns a logger that records every record into the returned
// slice. The caller reads the slice after the run; no record is dropped.
func captureLogger() (*[]capturedLog, *slog.Logger) {
	logs := &[]capturedLog{}
	h := captureHandler{mu: &sync.Mutex{}, logs: logs}
	return logs, slog.New(h)
}

// pagesBody builds a categorymembers response body carrying n main-namespace
// pages with ids 1..n and no continuation, so one fetch yields the whole set.
func pagesBody(n int) string {
	var b strings.Builder
	b.WriteString(`{"query":{"categorymembers":[`)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"pageid":%d,"ns":0,"title":"P%d","type":"page"}`, i, i)
	}
	b.WriteString(`]}}`)
	return b.String()
}

// countMsg counts how many captured records carry the given message.
func countMsg(logs []capturedLog, msg string) int {
	n := 0
	for _, l := range logs {
		if l.msg == msg {
			n++
		}
	}
	return n
}

func TestCategoryMembersLogsThrottledProgress(t *testing.T) {
	t.Parallel()
	logs, lg := captureLogger()
	c := categoryClient(t, map[string]string{"Category:Big": pagesBody(5)})
	c.Logger = lg
	c.progressEvery = 2

	got, err := c.CategoryMembers(t.Context(), []string{"Category:Big"}, 0, 100)
	if err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d members, want 5", len(got))
	}
	// With an interval of 2, the running count crosses a threshold at 2 and 4 (not
	// at 5), so exactly two progress lines are emitted.
	if n := countMsg(*logs, "crawl enumerating category members"); n != 2 {
		t.Fatalf("progress lines = %d, want 2", n)
	}
	// The last progress line carries the running context an operator needs.
	var last capturedLog
	for _, l := range *logs {
		if l.msg == "crawl enumerating category members" {
			last = l
		}
	}
	if last.attrs["project"] != "simplewiki" {
		t.Errorf("project attr = %v, want simplewiki", last.attrs["project"])
	}
	if last.attrs["pages"] != int64(4) {
		t.Errorf("pages attr = %v, want 4", last.attrs["pages"])
	}
	if _, ok := last.attrs["frontier"]; !ok {
		t.Errorf("progress line missing frontier attr: %v", last.attrs)
	}
	if _, ok := last.attrs["depth"]; !ok {
		t.Errorf("progress line missing depth attr: %v", last.attrs)
	}
}

func TestCategoryMembersLogsPerSeedCategory(t *testing.T) {
	t.Parallel()
	logs, lg := captureLogger()
	c := categoryClient(t, map[string]string{
		"Category:One": pagesBody(1),
		"Category:Two": pagesBody(1),
	})
	c.Logger = lg

	if _, err := c.CategoryMembers(t.Context(), []string{"Category:One", "Category:Two"}, 0, 100); err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if n := countMsg(*logs, "crawl walking seed category"); n != 2 {
		t.Fatalf("seed lines = %d, want one per seed category (2)", n)
	}
}

func TestCategoryMembersSmallWalkDoesNotSpamProgress(t *testing.T) {
	t.Parallel()
	logs, lg := captureLogger()
	c := categoryClient(t, map[string]string{"Category:Small": pagesBody(3)})
	c.Logger = lg // default interval (categoryProgressInterval), far above 3

	if _, err := c.CategoryMembers(t.Context(), []string{"Category:Small"}, 0, 100); err != nil {
		t.Fatalf("CategoryMembers: %v", err)
	}
	if n := countMsg(*logs, "crawl enumerating category members"); n != 0 {
		t.Fatalf("progress lines = %d, want 0 for a walk under the interval", n)
	}
}

func TestCategoryMembersProgressDoesNotChangeResult(t *testing.T) {
	t.Parallel()
	byCat := map[string]string{
		"Category:Root": `{"query":{"categorymembers":[
			{"pageid":1,"ns":0,"title":"A","type":"page"},
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":0,"ns":14,"title":"Category:Sub","type":"subcat"}]}}`,
		"Category:Sub": `{"query":{"categorymembers":[
			{"pageid":2,"ns":0,"title":"B","type":"page"},
			{"pageid":3,"ns":0,"title":"C","type":"page"}]}}`,
	}
	quiet := categoryClient(t, byCat)
	got1, err := quiet.CategoryMembers(t.Context(), []string{"Category:Root"}, 1, 100)
	if err != nil {
		t.Fatalf("quiet CategoryMembers: %v", err)
	}

	_, lg := captureLogger()
	loud := categoryClient(t, byCat)
	loud.Logger = lg
	loud.progressEvery = 1 // log on every page; result must still be identical
	got2, err := loud.CategoryMembers(t.Context(), []string{"Category:Root"}, 1, 100)
	if err != nil {
		t.Fatalf("loud CategoryMembers: %v", err)
	}

	if len(got1) != len(got2) {
		t.Fatalf("logging changed the result: %d vs %d members", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i] != got2[i] {
			t.Fatalf("logging changed member %d: %+v vs %+v", i, got1[i], got2[i])
		}
	}
}
