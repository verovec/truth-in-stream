package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/auth"
	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// adminContext attaches a verified admin identity, exactly as the /api gate
// does for a real admin caller.
func adminContext(ctx context.Context) context.Context {
	return middleware.WithIdentity(ctx, auth.Identity{Subject: "admin-sub", Username: "admin", Roles: []string{"admin", "guest"}})
}

// fakeVideoAnalysisService is a handler.VideoAnalysisService stand-in. getErr,
// when set, makes Get fail; lastGetID records the forwarded video id.
type fakeVideoAnalysisService struct {
	view      service.VideoAnalysisView
	getErr    error
	lastGetID string
}

func (f *fakeVideoAnalysisService) Get(_ context.Context, videoID string) (service.VideoAnalysisView, error) {
	f.lastGetID = videoID
	if f.getErr != nil {
		return service.VideoAnalysisView{}, f.getErr
	}
	return f.view, nil
}

var _ VideoAnalysisService = (*fakeVideoAnalysisService)(nil)

func analysisRequest(t *testing.T) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/v1/analysis", nil)
	req.SetPathValue("id", "v1")
	return rec, req
}

func TestGetVideoAnalysisHandlerStatusCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		svc      *fakeVideoAnalysisService
		wantCode int
	}{
		{name: "ok", svc: &fakeVideoAnalysisService{view: service.VideoAnalysisView{Video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisNone}}}, wantCode: http.StatusOK},
		{name: "unknown video", svc: &fakeVideoAnalysisService{getErr: domain.ErrVideoNotFound}, wantCode: http.StatusNotFound},
		{name: "wrapped not found", svc: &fakeVideoAnalysisService{getErr: errors.Join(errors.New("ctx"), domain.ErrVideoNotFound)}, wantCode: http.StatusNotFound},
		{name: "internal", svc: &fakeVideoAnalysisService{getErr: errors.New("boom")}, wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, req := analysisRequest(t)
			getVideoAnalysisHandler(tc.svc, false)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.svc.lastGetID != "v1" {
				t.Errorf("get id = %q, want v1", tc.svc.lastGetID)
			}
		})
	}
}

// TestGetVideoAnalysisHandlerLifecycleOnly proves the poll-target shape: while
// nothing is stored, the response carries only the lifecycle fields - no
// engine, no counters, no frames.
func TestGetVideoAnalysisHandlerLifecycleOnly(t *testing.T) {
	t.Parallel()
	svc := &fakeVideoAnalysisService{view: service.VideoAnalysisView{Video: domain.Video{
		ID:                 "v1",
		AnalysisStatus:     domain.VideoAnalysisAnalysing,
		AnalysisRuns:       2,
		AnalysisProgressMS: 15000,
	}}}
	rec, req := analysisRequest(t)
	getVideoAnalysisHandler(svc, false)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]any{
		"analysis_status":      "analysing",
		"analysis_runs":        float64(2),
		"analysis_progress_ms": float64(15000),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
}

// TestGetVideoAnalysisHandlerFailedCarriesError proves a failed run surfaces
// its reason.
func TestGetVideoAnalysisHandlerFailedCarriesError(t *testing.T) {
	t.Parallel()
	svc := &fakeVideoAnalysisService{view: service.VideoAnalysisView{Video: domain.Video{
		ID:             "v1",
		AnalysisStatus: domain.VideoAnalysisFailed,
		AnalysisError:  "interrupted by restart",
	}}}
	rec, req := analysisRequest(t)
	getVideoAnalysisHandler(svc, false)(rec, req)

	var got videoAnalysisResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AnalysisStatus != "failed" || got.AnalysisError != "interrupted by restart" {
		t.Errorf("got status %q error %q, want failed / interrupted by restart", got.AnalysisStatus, got.AnalysisError)
	}
}

// completedAnalysisView builds a complete view whose events cover the frame
// kinds a stored pre-analysis replays: subtitle, claims, and a per-claim
// verdict.
func completedAnalysisView(analyzedAt time.Time) service.VideoAnalysisView {
	return service.VideoAnalysisView{
		Video: domain.Video{
			ID:             "v1",
			AnalysisStatus: domain.VideoAnalysisComplete,
			AnalyzedAt:     analyzedAt,
			AnalysisRuns:   1,
		},
		Analysis: &domain.VideoAnalysis{
			VideoID:        "v1",
			Engine:         []byte(`{"transcriber":"u3-rt-pro"}`),
			ClaimsTotal:    2,
			ClaimsCredible: 1,
			ClaimsDisputed: 1,
		},
		Events: []service.LiveEvent{
			{Kind: service.LiveEventSubtitle, ID: "s1", Segment: domain.Segment{Start: 2 * time.Second, End: 4 * time.Second, Text: "bonjour", Speaker: "A"}},
			{Kind: service.LiveEventClaims, ID: "s1", Claims: []service.AtomicClaim{{ClaimID: "c1", Text: "claim one"}}},
			{Kind: service.LiveEventResult, ID: "s1", ClaimID: "c1", ClaimStatus: service.ClaimStatusVerified, Verdict: &service.VerifiedVerdict{Verdict: service.VerdictCredible, Confidence: 0.8}},
			// A malformed tally event must be skipped, exactly as the socket
			// writer skips it, not rendered as a zero-valued frame.
			{Kind: service.LiveEventSpeakerTally},
		},
	}
}

// TestGetVideoAnalysisHandlerCompleteFrames proves the complete response
// carries engine, counters, and the frame list in the exact shapes the live
// socket serializer produces for the same events.
func TestGetVideoAnalysisHandlerCompleteFrames(t *testing.T) {
	t.Parallel()
	analyzedAt := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	svc := &fakeVideoAnalysisService{view: completedAnalysisView(analyzedAt)}
	rec, req := analysisRequest(t)
	getVideoAnalysisHandler(svc, false)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		videoAnalysisResponse
		Frames []json.RawMessage `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.AnalysisStatus != "complete" || !got.AnalyzedAt.Equal(analyzedAt) || got.AnalysisRuns != 1 {
		t.Errorf("lifecycle = %q/%s/%d, want complete/%s/1", got.AnalysisStatus, got.AnalyzedAt, got.AnalysisRuns, analyzedAt)
	}
	if string(got.Engine) != `{"transcriber":"u3-rt-pro"}` {
		t.Errorf("engine = %s, want the stored fingerprint", got.Engine)
	}
	if got.Counters == nil || *got.Counters != (analysisCountersJSON{Total: 2, Credible: 1, Disputed: 1}) {
		t.Errorf("counters = %+v, want total 2 credible 1 disputed 1", got.Counters)
	}

	// The malformed tally is skipped, so three frames remain, each identical
	// to the socket serializer's output for its event.
	events := svc.view.Events
	wantFrames := make([]json.RawMessage, 0, len(events))
	for _, ev := range events {
		frame := toLiveFrame(ev, false)
		if frame == nil {
			continue
		}
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatalf("marshal want frame: %v", err)
		}
		wantFrames = append(wantFrames, encoded)
	}
	if len(wantFrames) != 3 {
		t.Fatalf("want 3 frames from the serializer, got %d", len(wantFrames))
	}
	if len(got.Frames) != len(wantFrames) {
		t.Fatalf("got %d frames, want %d", len(got.Frames), len(wantFrames))
	}
	for i := range wantFrames {
		if string(got.Frames[i]) != string(wantFrames[i]) {
			t.Errorf("frame %d = %s, want %s", i, got.Frames[i], wantFrames[i])
		}
	}
	var subtitle map[string]any
	if err := json.Unmarshal(got.Frames[0], &subtitle); err != nil {
		t.Fatalf("decode subtitle frame: %v", err)
	}
	if subtitle["type"] != "subtitle" || subtitle["start"] != float64(2) || subtitle["end"] != float64(4) {
		t.Errorf("subtitle frame = %v, want type subtitle with absolute [2,4] timestamps", subtitle)
	}
}

// TestGetVideoAnalysisHandlerDebugDetailGate proves the per-claim evidence
// detail inside claim_result frames follows the same server-side gate as the
// live socket: emitted only when the debug flag is on AND the caller carries a
// verified admin claim, never from anything client-supplied.
func TestGetVideoAnalysisHandlerDebugDetailGate(t *testing.T) {
	t.Parallel()
	view := service.VideoAnalysisView{
		Video:    domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisComplete},
		Analysis: &domain.VideoAnalysis{VideoID: "v1", ClaimsTotal: 1, ClaimsCredible: 1},
		Events: []service.LiveEvent{{
			Kind: service.LiveEventResult, ID: "s1", ClaimID: "c1", ClaimStatus: service.ClaimStatusVerified,
			Verdict: &service.VerifiedVerdict{Verdict: service.VerdictCredible, Citations: []domain.SegmentMatch{{EvidenceID: "e1", Claim: "secret detail"}}},
		}},
	}
	tests := []struct {
		name       string
		debug      bool
		admin      bool
		wantDetail bool
	}{
		{name: "flag off guest", debug: false, admin: false, wantDetail: false},
		{name: "flag off admin", debug: false, admin: true, wantDetail: false},
		{name: "flag on guest", debug: true, admin: false, wantDetail: false},
		{name: "flag on admin", debug: true, admin: true, wantDetail: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, req := analysisRequest(t)
			if tc.admin {
				req = req.WithContext(adminContext(req.Context()))
			}
			getVideoAnalysisHandler(&fakeVideoAnalysisService{view: view}, tc.debug)(rec, req)
			hasDetail := strings.Contains(rec.Body.String(), "secret detail")
			if hasDetail != tc.wantDetail {
				t.Errorf("evidence detail present = %v, want %v", hasDetail, tc.wantDetail)
			}
		})
	}
}

// TestVideoJSONCarriesAnalysisFields proves every list item carries the
// analysis flag without any per-row lookup: the status and completion date
// ride on the record the list already loads.
func TestVideoJSONCarriesAnalysisFields(t *testing.T) {
	t.Parallel()
	analyzedAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	svc := &fakeVideoService{list: []domain.Video{
		{ID: "v1", Title: "Analyzed", Status: domain.VideoStatusReady, Kind: domain.VideoKindUpload, AnalysisStatus: domain.VideoAnalysisComplete, AnalyzedAt: analyzedAt},
		{ID: "v2", Title: "Fresh", Status: domain.VideoStatusReady, Kind: domain.VideoKindUpload, AnalysisStatus: domain.VideoAnalysisNone},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	listVideosHandler(svc)(rec, req)

	var got struct {
		Videos []map[string]any `json:"videos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Videos) != 2 {
		t.Fatalf("got %d videos, want 2", len(got.Videos))
	}
	if got.Videos[0]["analysis_status"] != "complete" {
		t.Errorf("analyzed item status = %v, want complete", got.Videos[0]["analysis_status"])
	}
	if _, ok := got.Videos[0]["analyzed_at"]; !ok {
		t.Error("analyzed item is missing analyzed_at")
	}
	if got.Videos[1]["analysis_status"] != "none" {
		t.Errorf("fresh item status = %v, want none", got.Videos[1]["analysis_status"])
	}
	if _, ok := got.Videos[1]["analyzed_at"]; ok {
		t.Error("never-analyzed item should omit analyzed_at")
	}
}

// fakeVideoAnalysisStarter is a handler.VideoAnalysisStarter stand-in. err,
// when set, makes Start fail; lastID records the forwarded video id.
type fakeVideoAnalysisStarter struct {
	err    error
	lastID string
}

func (f *fakeVideoAnalysisStarter) Start(_ context.Context, id string) error {
	f.lastID = id
	return f.err
}

var _ VideoAnalysisStarter = (*fakeVideoAnalysisStarter)(nil)

// TestAnalyseVideoHandlerStatusCodes pins the trigger contract: 202 on accept
// (first run and re-run alike), 404 unknown, 422 while the upload is not
// ready, 409 while a run holds the analysing lock, 500 otherwise.
func TestAnalyseVideoHandlerStatusCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		starter  *fakeVideoAnalysisStarter
		wantCode int
	}{
		{name: "accepted", starter: &fakeVideoAnalysisStarter{}, wantCode: http.StatusAccepted},
		{name: "unknown video", starter: &fakeVideoAnalysisStarter{err: domain.ErrVideoNotFound}, wantCode: http.StatusNotFound},
		{name: "not ready", starter: &fakeVideoAnalysisStarter{err: domain.ErrVideoNotReady}, wantCode: http.StatusUnprocessableEntity},
		{name: "already analysing", starter: &fakeVideoAnalysisStarter{err: domain.ErrVideoAnalysisInProgress}, wantCode: http.StatusConflict},
		{name: "wrapped conflict", starter: &fakeVideoAnalysisStarter{err: errors.Join(errors.New("ctx"), domain.ErrVideoAnalysisInProgress)}, wantCode: http.StatusConflict},
		{name: "internal", starter: &fakeVideoAnalysisStarter{err: errors.New("boom")}, wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/v1/analyse", nil)
			req.SetPathValue("id", "v1")
			analyseVideoHandler(tc.starter)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.starter.lastID != "v1" {
				t.Errorf("starter saw id %q, want v1", tc.starter.lastID)
			}
		})
	}
}

// TestAnalyseVideoRouteRoleGating proves the trigger is a backoffice
// operation: an admin gets 202, a guest 403 before the starter runs, an
// anonymous caller 401 - while the analysis read stays open to any
// authenticated caller. It runs through NewMux so the real identity and admin
// gates are exercised.
func TestAnalyseVideoRouteRoleGating(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		method, path string
		bearer       string
		wantCode     int
	}{
		{name: "analyse as admin", method: http.MethodPost, path: "/api/videos/v1/analyse", bearer: testAdminToken, wantCode: http.StatusAccepted},
		{name: "analyse as guest", method: http.MethodPost, path: "/api/videos/v1/analyse", bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "analyse anonymous", method: http.MethodPost, path: "/api/videos/v1/analyse", wantCode: http.StatusUnauthorized},
		{name: "analysis read as guest", method: http.MethodGet, path: "/api/videos/v1/analysis", bearer: testGuestToken, wantCode: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			if tc.bearer != "" {
				bearer(req, tc.bearer)
			}
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("%s %s = %d, want %d; body=%s", tc.method, tc.path, rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}
