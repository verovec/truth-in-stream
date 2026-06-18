package crawlnotify

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

func TestSlackNotifierPost(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL)
	ev := RunFinished{Source: "wikipedia", Scope: "category:Physics", New: 3, Updated: 2, Skipped: 1, Duration: 90 * time.Second}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	var msg struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
			Text *struct {
				Text string `json:"text"`
			} `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(gotBody, &msg); err != nil {
		t.Fatalf("unmarshal posted body: %v (%s)", err, gotBody)
	}
	if !strings.Contains(msg.Text, "wikipedia") {
		t.Errorf("fallback text = %q, want it to name the source", msg.Text)
	}
	if len(msg.Blocks) == 0 {
		t.Fatal("posted message carries no blocks")
	}
	if msg.Blocks[0].Text == nil || !strings.Contains(msg.Blocks[0].Text.Text, "3 new") {
		t.Errorf("first block = %+v, want it to carry the run summary", msg.Blocks[0])
	}
}

func TestSlackNotifierNon200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewSlackNotifier(srv.URL)
	err := n.Notify(context.Background(), RunStarted{Source: "wikipedia", Scope: "x"})
	if err == nil {
		t.Fatal("Notify err = nil, want non-200 error")
	}
}

// TestSlackNotifierErrorHidesURL proves a transport failure never surfaces the
// webhook URL (a secret) into the returned, and therefore loggable, error.
func TestSlackNotifierErrorHidesURL(t *testing.T) {
	t.Parallel()

	const secret = "https://hooks.slack.com/services/SECRET-TOKEN"
	// A connection-refused dial: nothing listens here, so Do fails.
	n := NewSlackNotifier(secret)
	err := n.Notify(context.Background(), RunStarted{Source: "wikipedia", Scope: "x"})
	if err == nil {
		t.Skip("dial unexpectedly succeeded; cannot assert on the error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") || strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the webhook URL: %v", err)
	}
}
