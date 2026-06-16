package wiki

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/verovec/truth-in-stream/backend/internal/evidencegate"
)

// TestRunCrawlEndToEndWithRealGate exercises the producer's full fact-checkability
// path - RunCrawl -> gateChunks -> evidencegate.Client -> internal/llm -> the
// Anthropic SDK -> HTTP - against a fake Anthropic server, with no mock of the
// Gate interface itself. It is the operator-level check the card requires: a
// crawl with the gate on publishes strictly fewer chunks than with it off, and
// the chunks that survive are the evidence-like ones. A fake MediaWiki source
// stands in for the live API so the test is hermetic and needs no secrets.
func TestRunCrawlEndToEndWithRealGate(t *testing.T) {
	t.Parallel()

	// Fake Anthropic: judge a passage fact-checkable unless it carries the
	// navigational "filler" marker, returning the same forced-tool shape the real
	// model would. This drives the real evidencegate + llm decode path.
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		factCheckable := !strings.Contains(string(body), "filler")
		resp, _ := json.Marshal(map[string]any{
			"id": "msg_e2e", "type": "message", "role": "assistant", "model": "claude-haiku-4-5-20251001",
			"content": []map[string]any{{
				"type": "tool_use", "id": "toolu_e2e", "name": "record_fact_checkability",
				"input": map[string]any{"fact_checkable": factCheckable, "reason": ""},
			}},
			"stop_reason": "tool_use",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(anthropic.Close)

	gate, err := evidencegate.New(
		evidencegate.Config{APIKey: "test-key"},
		option.WithBaseURL(anthropic.URL), option.WithMaxRetries(0),
	)
	if err != nil {
		t.Fatalf("evidencegate.New: %v", err)
	}

	// One article: an evidence-like lead and a navigational ("filler") body.
	src := fakeSource{
		members: []CategoryMember{{PageID: 7, Title: "Atom"}},
		lead:    map[string]Extract{"Atom": {PageID: 7, Title: "Atom", RevisionID: 5, Text: "An atom has a nucleus of protons and neutrons."}},
		full: map[string]Extract{"Atom": {PageID: 7, Title: "Atom", RevisionID: 5,
			Text: "An atom has a nucleus of protons and neutrons.\n\nfiller navigational see also references external links."}},
	}
	cfg := CrawlConfig{
		Categories: []string{"Category:Physics"}, Corpus: "simplewiki-crawl", Project: "simplewiki",
		MaxPages: 100, IncludeBody: true, MaxPriority: 10, GateConcurrency: 4,
	}

	// Gate on (real adapter): only the evidence-like lead survives.
	gated := &capturePublisher{}
	onStats, err := RunCrawl(t.Context(), discardLogger(), src, gated, gate, cfg)
	if err != nil {
		t.Fatalf("RunCrawl gate-on: %v", err)
	}

	// Gate off (nil): every chunk is published.
	ungated := &capturePublisher{}
	offStats, err := RunCrawl(t.Context(), discardLogger(), src, ungated, nil, cfg)
	if err != nil {
		t.Fatalf("RunCrawl gate-off: %v", err)
	}

	if onStats.Published >= offStats.Published {
		t.Fatalf("gate-on published %d, gate-off published %d; gate-on must publish fewer",
			onStats.Published, offStats.Published)
	}
	if onStats.Dropped == 0 {
		t.Fatal("expected the gate to drop the navigational body chunk")
	}
	if onStats.Published+onStats.Dropped != offStats.Published {
		t.Errorf("published(%d)+dropped(%d) != ungated total(%d)", onStats.Published, onStats.Dropped, offStats.Published)
	}
	for _, j := range gated.jobs {
		if strings.Contains(j.Content, "filler") {
			t.Errorf("gate published a navigational chunk: %q", j.Content)
		}
		if j.Kind != "lead" {
			t.Errorf("surviving chunk kind = %q, want the evidence-like lead", j.Kind)
		}
	}
}
