package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

type fakeProcessing struct {
	submission  service.Submission
	submitErr   error
	progress    service.Progress
	progressErr error
	results     []domain.SegmentResult
	resultsErr  error

	gotSource  string
	gotVideoID string
}

func (f *fakeProcessing) Submit(_ context.Context, source string) (service.Submission, error) {
	f.gotSource = source
	return f.submission, f.submitErr
}

func (f *fakeProcessing) Progress(_ context.Context, videoID string) (service.Progress, error) {
	f.gotVideoID = videoID
	return f.progress, f.progressErr
}

func (f *fakeProcessing) Results(_ context.Context, videoID string) ([]domain.SegmentResult, error) {
	f.gotVideoID = videoID
	return f.results, f.resultsErr
}

func newProcessingServer(svc ProcessingService) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hc := service.NewHealthChecker(fakePinger{})
	return NewMux(hc, &stubTranscriber{}, svc, logger)
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestSubmitVideo(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		svc        *fakeProcessing
		wantCode   int
		wantStatus string
	}{
		{
			name:       "new video accepted",
			body:       `{"source":"https://example.com/v.mp4"}`,
			svc:        &fakeProcessing{submission: service.Submission{VideoID: "abc", Status: service.StatusProcessing}},
			wantCode:   http.StatusAccepted,
			wantStatus: "processing",
		},
		{
			name:       "cached video returns complete",
			body:       `{"source":"https://example.com/v.mp4"}`,
			svc:        &fakeProcessing{submission: service.Submission{VideoID: "abc", Status: service.StatusComplete}},
			wantCode:   http.StatusOK,
			wantStatus: "complete",
		},
		{
			name:     "empty source rejected",
			body:     `{"source":""}`,
			svc:      &fakeProcessing{submitErr: service.ErrEmptySource},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid JSON rejected",
			body:     `{not json`,
			svc:      &fakeProcessing{},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "oversized body rejected",
			body:     `{"source":"` + strings.Repeat("a", 2<<20) + `"}`,
			svc:      &fakeProcessing{},
			wantCode: http.StatusRequestEntityTooLarge,
		},
		{
			name:     "queue full",
			body:     `{"source":"v"}`,
			svc:      &fakeProcessing{submitErr: service.ErrQueueFull},
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name:     "internal error",
			body:     `{"source":"v"}`,
			svc:      &fakeProcessing{submitErr: errors.New("db down")},
			wantCode: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newProcessingServer(tc.svc)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/videos", strings.NewReader(tc.body))
			srv.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("POST /api/videos = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantStatus == "" {
				return
			}
			body := decodeBody(t, rec)
			if body["video_id"] != "abc" {
				t.Errorf("video_id = %v, want abc", body["video_id"])
			}
			if body["status"] != tc.wantStatus {
				t.Errorf("status = %v, want %v", body["status"], tc.wantStatus)
			}
		})
	}
}

func TestVideoStatus(t *testing.T) {
	tests := []struct {
		name     string
		svc      *fakeProcessing
		wantCode int
		wantBody map[string]any
	}{
		{
			name: "mid-run progress",
			svc: &fakeProcessing{progress: service.Progress{
				VideoID: "abc", Status: service.StatusProcessing, SegmentsTotal: 10, SegmentsDone: 4,
			}},
			wantCode: http.StatusOK,
			wantBody: map[string]any{
				"video_id":       "abc",
				"status":         "processing",
				"segments_total": float64(10),
				"segments_done":  float64(4),
			},
		},
		{
			name: "failed run carries the error",
			svc: &fakeProcessing{progress: service.Progress{
				VideoID: "abc", Status: service.StatusFailed, SegmentsTotal: 10, SegmentsDone: 4, Err: "transcribe: boom",
			}},
			wantCode: http.StatusOK,
			wantBody: map[string]any{
				"video_id":       "abc",
				"status":         "failed",
				"segments_total": float64(10),
				"segments_done":  float64(4),
				"error":          "transcribe: boom",
			},
		},
		{
			name:     "unknown video",
			svc:      &fakeProcessing{progressErr: service.ErrUnknownVideo},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "internal error",
			svc:      &fakeProcessing{progressErr: errors.New("db down")},
			wantCode: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newProcessingServer(tc.svc)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/videos/abc/status", nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("GET status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.svc.gotVideoID != "abc" {
				t.Errorf("service received video id %q, want abc", tc.svc.gotVideoID)
			}
			if tc.wantBody == nil {
				return
			}
			if diff := cmp.Diff(tc.wantBody, decodeBody(t, rec)); diff != "" {
				t.Errorf("body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestVideoResults(t *testing.T) {
	results := []domain.SegmentResult{
		{
			Segment: domain.Segment{Start: 1500 * time.Millisecond, End: 2250 * time.Millisecond, Text: "hello"},
			Matches: []domain.SegmentMatch{{
				Claim:      "the sky is blue",
				Verdict:    domain.VerdictCorroborates,
				Sources:    []domain.Source{{Title: "Sky study", URL: "https://sky.example"}},
				Similarity: 0.92,
			}},
		},
		{
			Segment: domain.Segment{Start: 3 * time.Second, End: 4 * time.Second, Text: "world"},
			Matches: []domain.SegmentMatch{},
		},
	}
	wantBody := map[string]any{
		"video_id": "abc",
		"segments": []any{
			map[string]any{
				"start": 1.5,
				"end":   2.25,
				"text":  "hello",
				"matches": []any{
					map[string]any{
						"claim":      "the sky is blue",
						"verdict":    "corroborates",
						"sources":    []any{map[string]any{"title": "Sky study", "url": "https://sky.example"}},
						"similarity": 0.92,
					},
				},
			},
			map[string]any{
				"start":   3.0,
				"end":     4.0,
				"text":    "world",
				"matches": []any{},
			},
		},
	}

	tests := []struct {
		name     string
		svc      *fakeProcessing
		wantCode int
		wantBody map[string]any
	}{
		{
			name:     "complete video serves the contract shape",
			svc:      &fakeProcessing{results: results},
			wantCode: http.StatusOK,
			wantBody: wantBody,
		},
		{
			name:     "zero segments serve an empty array",
			svc:      &fakeProcessing{results: []domain.SegmentResult{}},
			wantCode: http.StatusOK,
			wantBody: map[string]any{"video_id": "abc", "segments": []any{}},
		},
		{
			name:     "still processing",
			svc:      &fakeProcessing{resultsErr: service.ErrResultsNotReady},
			wantCode: http.StatusConflict,
		},
		{
			name:     "unknown video",
			svc:      &fakeProcessing{resultsErr: service.ErrUnknownVideo},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "internal error",
			svc:      &fakeProcessing{resultsErr: errors.New("db down")},
			wantCode: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newProcessingServer(tc.svc)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/videos/abc/results", nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("GET results = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.svc.gotVideoID != "abc" {
				t.Errorf("service received video id %q, want abc", tc.svc.gotVideoID)
			}
			if tc.wantBody == nil {
				return
			}
			if diff := cmp.Diff(tc.wantBody, decodeBody(t, rec)); diff != "" {
				t.Errorf("body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
