package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/verovec/truth-in-stream/backend/internal/transcribe"
)

type stubTranscriber struct {
	gotOpts  transcribe.Options
	gotAudio string
	tr       transcribe.Transcript
	err      error
}

func (s *stubTranscriber) TranscribeFile(_ context.Context, audio io.Reader, opts transcribe.Options) (transcribe.Transcript, error) {
	b, err := io.ReadAll(audio)
	if err != nil {
		return transcribe.Transcript{}, err
	}
	s.gotAudio = string(b)
	s.gotOpts = opts
	return s.tr, s.err
}

func (s *stubTranscriber) TranscribeStream(_ context.Context, _ <-chan []byte, _ transcribe.Options) (<-chan transcribe.TranscriptEvent, error) {
	return nil, errors.New("not implemented")
}

func newTranscriptServer(st *stubTranscriber) http.Handler {
	return newTestServer(nil, st)
}

func multipartBody(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func postTranscript(t *testing.T, srv http.Handler, target, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartBody(t, filename, content)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(authCookie(t))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestPostTranscriptReturnsSegments(t *testing.T) {
	t.Parallel()
	st := &stubTranscriber{tr: transcribe.Transcript{
		Language: "en",
		Segments: []transcribe.Segment{
			{Start: 0, End: 900 * time.Millisecond, Text: "Hello world."},
			{Start: time.Second, End: 1900 * time.Millisecond, Text: "This is live."},
		},
	}}
	srv := newTranscriptServer(st)

	rec := postTranscript(t, srv, "/api/transcripts", "talk.mp4", "fake-audio")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200, body %s", rec.Code, rec.Body)
	}
	if st.gotAudio != "fake-audio" {
		t.Errorf("audio = %q, want fake-audio", st.gotAudio)
	}
	if st.gotOpts.Filename != "talk.mp4" {
		t.Errorf("filename = %q, want talk.mp4", st.gotOpts.Filename)
	}

	var got transcriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	want := transcriptResponse{
		Language: "en",
		Segments: []segmentResponse{
			{Start: 0, End: 0.9, Text: "Hello world."},
			{Start: 1, End: 1.9, Text: "This is live."},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
}

func TestPostTranscriptForwardsLanguage(t *testing.T) {
	t.Parallel()
	st := &stubTranscriber{}
	srv := newTranscriptServer(st)

	rec := postTranscript(t, srv, "/api/transcripts?language=fr", "a.mp3", "x")

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200, body %s", rec.Code, rec.Body)
	}
	if st.gotOpts.Language != "fr" {
		t.Errorf("language = %q, want fr", st.gotOpts.Language)
	}
}

func TestPostTranscriptRejectsMissingFile(t *testing.T) {
	t.Parallel()
	srv := newTranscriptServer(&stubTranscriber{})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("language", "en"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/transcripts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(authCookie(t))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestPostTranscriptRejectsNonMultipart(t *testing.T) {
	t.Parallel()
	srv := newTranscriptServer(&stubTranscriber{})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/transcripts", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(authCookie(t))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestPostTranscriptClassifiesProviderErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{
			name:     "rate limited maps to 503",
			err:      fmt.Errorf("scribe: %w", transcribe.ErrRateLimited),
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "provider auth failure maps to 502",
			err:      &transcribe.APIError{StatusCode: http.StatusUnauthorized, Body: "bad key"},
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "timeout maps to 504",
			err:      context.DeadlineExceeded,
			wantCode: http.StatusGatewayTimeout,
		},
		{
			name:     "oversized upload maps to 413",
			err:      fmt.Errorf("scribe: copy audio: %w", &http.MaxBytesError{Limit: 1}),
			wantCode: http.StatusRequestEntityTooLarge,
		},
		{
			name:     "unreachable provider maps to 502",
			err:      errors.New("connection refused"),
			wantCode: http.StatusBadGateway,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newTranscriptServer(&stubTranscriber{err: tc.err})

			rec := postTranscript(t, srv, "/api/transcripts", "a.mp3", "x")

			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d, body %s", rec.Code, tc.wantCode, rec.Body)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid json error body: %v", err)
			}
			if body["error"] == "" {
				t.Error("error body is empty")
			}
		})
	}
}

func TestGetTranscriptsIsMethodNotAllowed(t *testing.T) {
	t.Parallel()
	srv := newTranscriptServer(&stubTranscriber{})

	// Unauthenticated requests get a uniform 401 before method resolution,
	// so the route table leaks nothing; the 405 needs a session.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/transcripts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated code = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/transcripts", nil)
	req.AddCookie(authCookie(t))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("authenticated code = %d, want 405", rec.Code)
	}
}
