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

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// exportReplayer serves a canned snapshot so the export handlers can be exercised
// without a real cache.
type exportReplayer struct {
	events []service.LiveEvent
	found  bool
	err    error
}

func (r *exportReplayer) Snapshot(_ context.Context, _ string) ([]service.LiveEvent, bool, error) {
	return r.events, r.found, r.err
}

func newExportServer(videos VideoService, replayer AnalysisReplayer) http.Handler {
	health := service.NewHealthChecker(fakePinger{})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewMux(health, videos, &fakeVideoAnalysisService{}, &fakeVideoAnalysisStarter{}, &fakeDocumentService{}, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, &fakeTVChannelService{}, &fakeTVRecordingService{}, testTVHub(), stubLiveAnalyzer{}, nil, replayer, nil, false, nil, "", globalTestAuth, logger)
}

func exportSnapshot() []service.LiveEvent {
	return []service.LiveEvent{
		{
			Kind: service.LiveEventSubtitle,
			ID:   "s1",
			Segment: domain.Segment{
				Start: 0, End: 2 * time.Second, Speaker: "Speaker 1", Text: "Hello",
			},
		},
		{
			Kind:        service.LiveEventResult,
			ID:          "s1",
			Segment:     domain.Segment{Start: 0, End: 2 * time.Second, Speaker: "Speaker 1", Text: "Hello"},
			ClaimID:     "c1",
			ClaimStatus: service.ClaimStatusVerified,
			Source:      service.SourceVerified,
			Verdict:     &service.VerifiedVerdict{Verdict: "credible", Rationale: "ok"},
		},
	}
}

// storedAnalysisStub backs a real service.StoredAnalysisReader in tests: a
// domain.VideoAnalysisStore holding one stored analysis, plus the video-record
// read the reader's view path needs.
type storedAnalysisStub struct {
	analysis domain.VideoAnalysis
}

func (s *storedAnalysisStub) CompleteVideoAnalysis(_ context.Context, a domain.VideoAnalysis) (domain.VideoAnalysis, error) {
	return a, nil
}

func (s *storedAnalysisStub) GetVideoAnalysis(_ context.Context, videoID string) (domain.VideoAnalysis, error) {
	if videoID != s.analysis.VideoID {
		return domain.VideoAnalysis{}, domain.ErrVideoAnalysisNotFound
	}
	return s.analysis, nil
}

func (s *storedAnalysisStub) GetVideo(context.Context, string) (domain.Video, error) {
	return domain.Video{}, domain.ErrVideoNotFound
}

// TestExportServesStoredAnalysisOverComposite pins the durable acceptance
// path: the export endpoint reads through a real composite replayer whose
// Postgres tier holds the pre-analysis and whose Redis tier always misses, so
// the SRT is served from the durable store with no cache entry and no TTL
// involved.
func TestExportServesStoredAnalysisOverComposite(t *testing.T) {
	payload, err := json.Marshal(exportSnapshot())
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	store := &storedAnalysisStub{analysis: domain.VideoAnalysis{
		VideoID:         "vid-1",
		SnapshotVersion: service.SnapshotVersion,
		Events:          payload,
		Engine:          []byte(`{}`),
	}}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	stored, err := service.NewStoredAnalysisReader(store, store, logger)
	if err != nil {
		t.Fatalf("NewStoredAnalysisReader: %v", err)
	}
	composite, err := service.NewCompositeReplayer(logger, stored, &exportReplayer{})
	if err != nil {
		t.Fatalf("NewCompositeReplayer: %v", err)
	}
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1", Title: "My Video"}}}
	srv := newExportServer(videos, composite)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid-1/export/transcript.srt", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Hello") {
		t.Fatalf("SRT body missing the stored transcript:\n%s", rec.Body.String())
	}
}

func TestExportEndpointsRequireAdmin(t *testing.T) {
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1", Title: "My Video"}}}
	replayer := &exportReplayer{events: exportSnapshot(), found: true}
	srv := newExportServer(videos, replayer)

	paths := []string{
		"/api/videos/vid-1/export/transcript.srt",
		"/api/videos/vid-1/export/claims.csv",
	}
	cases := []struct {
		name     string
		bearer   string
		wantCode int
	}{
		{name: "admin allowed", bearer: testAdminToken, wantCode: http.StatusOK},
		{name: "guest forbidden", bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "anonymous unauthorized", bearer: "", wantCode: http.StatusUnauthorized},
	}
	for _, path := range paths {
		for _, tc := range cases {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
				if tc.bearer != "" {
					bearer(req, tc.bearer)
				}
				srv.ServeHTTP(rec, req)
				if rec.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
				}
			})
		}
	}
}

func TestExportSRTStreamsAttachment(t *testing.T) {
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1", Title: "My Video"}}}
	replayer := &exportReplayer{events: exportSnapshot(), found: true}
	srv := newExportServer(videos, replayer)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid-1/export/transcript.srt", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-subrip") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".srt") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(rec.Body.String(), "00:00:00,000 --> 00:00:02,000") {
		t.Fatalf("body missing cue:\n%s", rec.Body.String())
	}
}

func TestExportCSVStreamsAttachment(t *testing.T) {
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1", Title: "My Video"}}}
	replayer := &exportReplayer{events: exportSnapshot(), found: true}
	srv := newExportServer(videos, replayer)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid-1/export/claims.csv", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".csv") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(rec.Body.String(), "segment_start") {
		t.Fatalf("body missing header:\n%s", rec.Body.String())
	}
}

func TestExportReturns404WhenNoSnapshot(t *testing.T) {
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1", Title: "My Video"}}}
	replayer := &exportReplayer{found: false}
	srv := newExportServer(videos, replayer)

	for _, path := range []string{
		"/api/videos/vid-1/export/transcript.srt",
		"/api/videos/vid-1/export/claims.csv",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		bearer(req, testAdminToken)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "run an analysis") {
			t.Fatalf("%s 404 body should tell the operator to run an analysis: %s", path, rec.Body.String())
		}
	}
}

func TestExportReturns404WhenVideoUnknown(t *testing.T) {
	videos := &fakeVideoService{getErr: domain.ErrVideoNotFound}
	replayer := &exportReplayer{events: exportSnapshot(), found: true}
	srv := newExportServer(videos, replayer)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/ghost/export/claims.csv", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestExportReturns500WhenReplayErrors(t *testing.T) {
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1"}}}
	replayer := &exportReplayer{err: errors.New("cache unavailable")}
	srv := newExportServer(videos, replayer)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid-1/export/claims.csv", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestExportReturns500WhenVideoLookupErrors(t *testing.T) {
	videos := &fakeVideoService{getErr: errors.New("db down")}
	replayer := &exportReplayer{events: exportSnapshot(), found: true}
	srv := newExportServer(videos, replayer)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid-1/export/transcript.srt", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestExportReturns503WhenReplayUnavailable(t *testing.T) {
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "vid-1"}}}
	srv := newExportServer(videos, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/vid-1/export/claims.csv", nil)
	bearer(req, testAdminToken)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
