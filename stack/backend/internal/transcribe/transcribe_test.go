package transcribe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func ptr(f float64) *float64 { return &f }

func word(text string, start, end float64) scribeWord {
	return scribeWord{Text: text, Start: ptr(start), End: ptr(end), Type: wordTypeWord}
}

func spacing() scribeWord {
	return scribeWord{Text: " ", Type: wordTypeSpacing}
}

func TestGroupWords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		words []scribeWord
		want  []Segment
	}{
		{
			name:  "no words",
			words: nil,
			want:  []Segment{},
		},
		{
			name: "sentence punctuation splits segments",
			words: []scribeWord{
				word("Hello", 0, 0.4), spacing(), word("world.", 0.5, 0.9), spacing(),
				word("Next", 1.0, 1.3), spacing(), word("one?", 1.4, 1.8), spacing(),
				word("Last!", 1.9, 2.2),
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(0.9), Text: "Hello world."},
				{Start: seconds(1.0), End: seconds(1.8), Text: "Next one?"},
				{Start: seconds(1.9), End: seconds(2.2), Text: "Last!"},
			},
		},
		{
			name: "long pause splits segments without punctuation",
			words: []scribeWord{
				word("trailing", 0, 0.5), spacing(), word("thought", 0.6, 1.0),
				spacing(), word("resumes", 2.5, 3.0), spacing(), word("here", 3.1, 3.4),
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(1.0), Text: "trailing thought"},
				{Start: seconds(2.5), End: seconds(3.4), Text: "resumes here"},
			},
		},
		{
			name: "max duration caps a runaway segment",
			words: []scribeWord{
				word("a", 0, 14), spacing(), word("b", 14.2, 31), spacing(), word("c", 31.2, 32),
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(31), Text: "a b"},
				{Start: seconds(31.2), End: seconds(32), Text: "c"},
			},
		},
		{
			name: "audio events are skipped",
			words: []scribeWord{
				word("quiet", 0, 0.4),
				{Text: "(laughter)", Start: ptr(0.5), End: ptr(0.8), Type: wordTypeAudioEvent},
				spacing(), word("room", 0.9, 1.2),
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(1.2), Text: "quiet room"},
			},
		},
		{
			name: "final segment without punctuation is flushed",
			words: []scribeWord{
				word("unfinished", 0, 0.5), spacing(), word("business", 0.6, 1.0),
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(1.0), Text: "unfinished business"},
			},
		},
		{
			name: "untimed segment does not inherit previous timestamps",
			words: []scribeWord{
				word("First.", 0, 5),
				spacing(),
				{Text: "Orphan.", Type: wordTypeWord},
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(5), Text: "First."},
				{Start: 0, End: 0, Text: "Orphan."},
			},
		},
		{
			name: "word without timestamps contributes text only",
			words: []scribeWord{
				word("timed", 0, 0.4), spacing(),
				{Text: "untimed", Type: wordTypeWord},
				spacing(), word("again.", 0.9, 1.3),
			},
			want: []Segment{
				{Start: seconds(0), End: seconds(1.3), Text: "timed untimed again."},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := groupWords(tc.words)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("groupWords mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// scribeServer replies with body and status, recording the multipart fields
// into the returned map and the auth header into the returned string.
func scribeServer(t *testing.T, status int, body []byte) (*httptest.Server, map[string]string, *string) {
	t.Helper()
	gotForm := map[string]string{}
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("xi-api-key")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		for k, vs := range r.MultipartForm.Value {
			gotForm[k] = vs[0]
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file part: %v", err)
		} else {
			audio, _ := io.ReadAll(file)
			gotForm["file"] = string(audio)
			_ = file.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, gotForm, &gotKey
}

func newTestClient(srv *httptest.Server) *Client {
	return New(Config{
		APIKey:     "test-key",
		Model:      "scribe_v2",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
}

func TestTranscribeFileSendsRequestAndGroupsSegments(t *testing.T) {
	t.Parallel()
	fixture, err := os.ReadFile("testdata/scribe_response.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv, gotForm, gotKey := scribeServer(t, http.StatusOK, fixture)
	client := newTestClient(srv)

	got, err := client.TranscribeFile(t.Context(), strings.NewReader("fake-audio-bytes"), Options{})
	if err != nil {
		t.Fatalf("TranscribeFile: %v", err)
	}

	if *gotKey != "test-key" {
		t.Errorf("xi-api-key = %q, want test-key", *gotKey)
	}
	wantForm := map[string]string{
		"model_id":               "scribe_v2",
		"timestamps_granularity": "word",
		"diarize":                "false",
		"tag_audio_events":       "false",
		"file":                   "fake-audio-bytes",
	}
	if diff := cmp.Diff(wantForm, gotForm); diff != "" {
		t.Errorf("form mismatch (-want +got):\n%s", diff)
	}

	want := Transcript{
		Language: "en",
		Segments: []Segment{
			{Start: seconds(0), End: seconds(0.9), Text: "Hello world."},
			{Start: seconds(1.0), End: seconds(1.9), Text: "This is live."},
			{Start: seconds(4.0), End: seconds(5.0), Text: "New thought here"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("transcript mismatch (-want +got):\n%s", diff)
	}
}

func TestTranscribeFileSendsLanguageWhenSet(t *testing.T) {
	t.Parallel()
	srv, gotForm, _ := scribeServer(t, http.StatusOK, []byte(`{"language_code":"fr","words":[]}`))
	client := newTestClient(srv)

	if _, err := client.TranscribeFile(t.Context(), strings.NewReader("x"), Options{Language: "fr"}); err != nil {
		t.Fatalf("TranscribeFile: %v", err)
	}
	if gotForm["language_code"] != "fr" {
		t.Errorf("language_code = %q, want fr", gotForm["language_code"])
	}
}

func TestTranscribeFileOmitsLanguageByDefault(t *testing.T) {
	t.Parallel()
	srv, gotForm, _ := scribeServer(t, http.StatusOK, []byte(`{"language_code":"en","words":[]}`))
	client := newTestClient(srv)

	if _, err := client.TranscribeFile(t.Context(), strings.NewReader("x"), Options{}); err != nil {
		t.Fatalf("TranscribeFile: %v", err)
	}
	if v, ok := gotForm["language_code"]; ok {
		t.Errorf("language_code sent as %q, want omitted", v)
	}
}

func TestTranscribeFileClassifiesAPIErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		wantStatus int
	}{
		{name: "auth failure", status: http.StatusUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, wantStatus: http.StatusTooManyRequests},
		{name: "validation", status: http.StatusUnprocessableEntity, wantStatus: http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, _, _ := scribeServer(t, tc.status, []byte(`{"detail":{"status":"failed"}}`))
			client := newTestClient(srv)

			_, err := client.TranscribeFile(t.Context(), strings.NewReader("x"), Options{})
			if err == nil {
				t.Fatal("want error, got nil")
			}
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("error %v is not *APIError", err)
			}
			if apiErr.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", apiErr.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestTranscribeFileRateLimitIsSentinel(t *testing.T) {
	t.Parallel()
	srv, _, _ := scribeServer(t, http.StatusTooManyRequests, []byte(`{"detail":{"status":"rate_limited"}}`))
	client := newTestClient(srv)

	_, err := client.TranscribeFile(t.Context(), strings.NewReader("x"), Options{})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestTranscribeFileRejectsBadBaseURL(t *testing.T) {
	t.Parallel()
	client := New(Config{APIKey: "k", Model: "scribe_v2", BaseURL: "://not-a-url"})

	if _, err := client.TranscribeFile(t.Context(), strings.NewReader("x"), Options{}); err == nil {
		t.Fatal("want request build error, got nil")
	}
}

func TestTranscribeFileRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-blocked
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(blocked) })
	client := newTestClient(srv)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.TranscribeFile(ctx, strings.NewReader("x"), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestTranscribeFileRejectsMalformedResponse(t *testing.T) {
	t.Parallel()
	srv, _, _ := scribeServer(t, http.StatusOK, []byte(`{not json`))
	client := newTestClient(srv)

	if _, err := client.TranscribeFile(t.Context(), strings.NewReader("x"), Options{}); err == nil {
		t.Fatal("want decode error, got nil")
	}
}

func TestTranscribeStreamDialErrorSurfaces(t *testing.T) {
	t.Parallel()
	// A malformed realtime URL fails before any network dial, so the streaming
	// setup error surfaces from the call without leaking a channel.
	client := New(Config{APIKey: "k", Model: "scribe_v2", RealtimeURL: "://bad"})

	events, err := client.TranscribeStream(t.Context(), make(chan []byte), Options{})
	if err == nil {
		t.Fatal("want dial setup error, got nil")
	}
	if events != nil {
		t.Fatalf("events = %v, want nil", events)
	}
}

var _ Transcriber = (*Client)(nil)

func TestSecondsConversion(t *testing.T) {
	t.Parallel()
	if got := seconds(1.5); got != 1500*time.Millisecond {
		t.Fatalf("seconds(1.5) = %v, want 1.5s", got)
	}
}
