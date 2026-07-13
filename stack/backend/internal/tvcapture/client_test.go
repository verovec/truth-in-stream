package tvcapture

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubTokens struct{ token string }

func (s stubTokens) Token(context.Context) (string, error) { return s.token, nil }

func newTestClient(t *testing.T, h http.HandlerFunc) *backendClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return newBackendClient(srv.URL, srv.Client(), stubTokens{token: "test-token"})
}

func TestListChannelsParsesFields(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tv/channels" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"channels":[
			{"id":"c1","slug":"tf1","name":"TF1","source_kind":"youtube","source_ref":"https://yt/tf1","enabled":true,"archive_enabled":true,"live":false},
			{"id":"c2","slug":"lcp","name":"LCP","source_kind":"hls","source_ref":"https://x/live.m3u8","enabled":false,"archive_enabled":false,"live":true}
		]}`)
	})

	got, err := c.ListChannels(context.Background())
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d channels", len(got))
	}
	want := Channel{ID: "c1", Slug: "tf1", Name: "TF1", SourceKind: "youtube", SourceRef: "https://yt/tf1", Enabled: true, ArchiveEnabled: true}
	if got[0] != want {
		t.Fatalf("channel[0] = %+v, want %+v", got[0], want)
	}
	// The backend still returns "live"; the worker decodes and ignores it.
	if got[1].Enabled || got[1].SourceKind != "hls" {
		t.Fatalf("channel[1] mis-parsed: %+v", got[1])
	}
}

func TestRequestUploadSendsBodyAndParsesTicket(t *testing.T) {
	t.Parallel()
	recordedAt := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tv/recordings/uploads" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["channel_id"] != "c1" || body["content_type"] != "video/mp4" {
			t.Errorf("body = %v", body)
		}
		if body["recorded_at"] != "2026-07-13T09:00:00Z" {
			t.Errorf("recorded_at = %v", body["recorded_at"])
		}
		if body["size_bytes"].(float64) != 4096 {
			t.Errorf("size_bytes = %v", body["size_bytes"])
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"video_id":"v1","object_key":"tv/v1.mp4","status":"pending","upload":{"url":"https://store/put","method":"PUT","headers":{"X-Amz-Meta":["a"]}}}`)
	})

	tk, err := c.RequestUpload(context.Background(), "c1", recordedAt, 4096)
	if err != nil {
		t.Fatalf("RequestUpload: %v", err)
	}
	if tk.VideoID != "v1" || tk.Upload.URL != "https://store/put" || tk.Upload.Method != "PUT" {
		t.Fatalf("ticket = %+v", tk)
	}
	if tk.Upload.Headers["X-Amz-Meta"][0] != "a" {
		t.Fatalf("headers = %v", tk.Upload.Headers)
	}
}

func TestUploadFileReplaysPresignedRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.mp4")
	if err := os.WriteFile(path, []byte("mp4-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotMethod, gotHeader string
	var gotBody []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("X-Custom")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	tk := recordingTicket{Upload: presignedRequest{
		URL:     c.baseURL + "/put",
		Method:  http.MethodPut,
		Headers: map[string][]string{"X-Custom": {"v"}},
	}}
	if err := c.UploadFile(context.Background(), tk, path); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if gotMethod != http.MethodPut || gotHeader != "v" || string(gotBody) != "mp4-bytes" {
		t.Fatalf("presigned request replay mismatch: method=%s header=%s body=%q", gotMethod, gotHeader, gotBody)
	}
}

func TestUploadFileTreats412AsSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	tk := recordingTicket{Upload: presignedRequest{URL: c.baseURL + "/put", Method: http.MethodPut}}
	if err := c.UploadFile(context.Background(), tk, path); err != nil {
		t.Fatalf("UploadFile 412 should succeed, got %v", err)
	}
}

func TestRegisterAndPrune(t *testing.T) {
	t.Parallel()

	t.Run("register", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/tv/recordings" {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["video_id"] != "v1" {
				t.Errorf("body = %v", body)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"v1"}`)
		})
		if err := c.Register(context.Background(), "v1"); err != nil {
			t.Fatalf("Register: %v", err)
		}
	})

	t.Run("register 409 errors for retry", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{}`)
		})
		if err := c.Register(context.Background(), "v1"); err == nil {
			t.Fatal("expected error on 409")
		}
	})

	t.Run("prune", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tv/recordings/prune" {
				t.Errorf("path = %s", r.URL.Path)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["retention_days"].(float64) != 30 {
				t.Errorf("retention_days = %v", body["retention_days"])
			}
			_, _ = io.WriteString(w, `{"deleted":7}`)
		})
		n, err := c.Prune(context.Background(), 30)
		if err != nil {
			t.Fatalf("Prune: %v", err)
		}
		if n != 7 {
			t.Fatalf("deleted = %d, want 7", n)
		}
	})
}

func TestFeedURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base string
		want string
	}{
		{"http://backend:8080", "ws://backend:8080/api/tv/channels/c1/feed"},
		{"https://api.example.com", "wss://api.example.com/api/tv/channels/c1/feed"},
	}
	for _, tc := range tests {
		c := newBackendClient(tc.base, http.DefaultClient, stubTokens{})
		got := c.FeedURL("c1")
		if got != tc.want {
			t.Fatalf("FeedURL = %q, want %q", got, tc.want)
		}
		// The token must never appear in the URL; it rides the Authorization header.
		if strings.Contains(got, "access_token") {
			t.Fatalf("FeedURL = %q leaks a token into the URL", got)
		}
	}
}
