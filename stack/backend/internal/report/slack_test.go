package report

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func samplePayload() Payload {
	return Payload{
		GeneratedAt: time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC),
		Window:      24 * time.Hour,
		Commits:     []Commit{{Hash: "abc1234", Author: "Alice", Subject: "Fix <thing> & stuff", Files: 2}},
		CardMoves:   []CardMove{{ID: "VER-1", Title: "A card", State: "Done"}},
		OpenPRs:     []PullRequest{{Number: 7, Title: "A PR", Author: "Bob", URL: "https://gh/7", Draft: true}},
		Blockers:    []Blocker{{ID: "VER-2", Title: "stalled"}},
	}
}

func TestSlackPostSendsBlockKit(t *testing.T) {
	t.Parallel()
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	r := &SlackRenderer{httpClient: srv.Client(), webhookURL: srv.URL}
	if err := r.Post(context.Background(), samplePayload()); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q", gotContentType)
	}

	var msg slackMessage
	if err := json.Unmarshal(gotBody, &msg); err != nil {
		t.Fatalf("posted body is not valid JSON: %v", err)
	}
	if msg.Text == "" {
		t.Error("message missing fallback text")
	}
	if msg.Blocks[0].Type != "header" {
		t.Errorf("first block = %q, want header", msg.Blocks[0].Type)
	}
	joined := blocksText(msg.Blocks)
	for _, want := range []string{"Commits", "Linear activity", "Open pull requests", "Blockers", "VER-1", "VER-2", "#7"} {
		if !strings.Contains(joined, want) {
			t.Errorf("blocks missing %q", want)
		}
	}
	// mrkdwn special characters in the subject must be escaped.
	if !strings.Contains(joined, "&lt;thing&gt; &amp; stuff") {
		t.Errorf("subject not mrkdwn-escaped in: %s", joined)
	}
}

func TestSlackPostEmptyWebhookErrors(t *testing.T) {
	t.Parallel()
	if err := NewSlackRenderer("").Post(context.Background(), samplePayload()); err == nil {
		t.Fatal("want error for empty webhook, got nil")
	}
}

func TestSlackPostNon200(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	r := &SlackRenderer{httpClient: srv.Client(), webhookURL: srv.URL}
	if err := r.Post(context.Background(), samplePayload()); err == nil {
		t.Fatal("want error on non-200, got nil")
	}
}

func TestSlackRenderJSONCapsLongSections(t *testing.T) {
	t.Parallel()
	p := Payload{GeneratedAt: time.Now()}
	for i := 0; i < slackSectionCap+5; i++ {
		p.Commits = append(p.Commits, Commit{Hash: "h", Subject: "s", Author: "a"})
	}
	out, err := NewSlackRenderer("").RenderJSON(p)
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(string(out), "and 5 more") {
		t.Errorf("long section not capped with overflow note: %s", out)
	}
}

func TestSlackSectionRespectsByteBudget(t *testing.T) {
	t.Parallel()
	// Few items, each well under the count cap but long enough that together they
	// exceed the byte budget; the section must truncate and note the overflow.
	long := strings.Repeat("x", 1000)
	p := Payload{GeneratedAt: time.Now()}
	for i := 0; i < 5; i++ {
		p.CardMoves = append(p.CardMoves, CardMove{ID: "VER-1", Title: long, State: "Done"})
	}
	msg := NewSlackRenderer("").buildMessage(p)
	for _, bl := range msg.Blocks {
		if bl.Text == nil {
			continue
		}
		if len(bl.Text.Text) > 3000 {
			t.Fatalf("section text %d chars exceeds Slack's 3000 limit", len(bl.Text.Text))
		}
	}
	if !strings.Contains(blocksText(msg.Blocks), "more_") {
		t.Error("oversized section did not record an overflow note")
	}
}

func TestSlackEmptySectionsRenderPlaceholders(t *testing.T) {
	t.Parallel()
	out, err := NewSlackRenderer("").RenderJSON(Payload{GeneratedAt: time.Now()})
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(string(out), "No commits in the window.") {
		t.Errorf("empty commits section missing placeholder: %s", out)
	}
}

func TestEscapeMrkdwn(t *testing.T) {
	t.Parallel()
	if got := escapeMrkdwn("a & b < c > d"); got != "a &amp; b &lt; c &gt; d" {
		t.Errorf("escapeMrkdwn = %q", got)
	}
}

func blocksText(blocks []slackBlock) string {
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Text != nil {
			b.WriteString(bl.Text.Text)
			b.WriteString("\n")
		}
		for _, e := range bl.Elements {
			b.WriteString(e.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}
