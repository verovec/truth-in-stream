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

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// fakeVideoSpeakerService is a VideoSpeakerService stand-in recording what the
// handlers forward.
type fakeVideoSpeakerService struct {
	speakers   []domain.VideoSpeaker
	listErr    error
	nameErr    error
	updateErr  error
	nameCalls  int
	lastLabel  string
	lastUpdate service.SpeakerUpdate
	hadTimeout bool
}

func (f *fakeVideoSpeakerService) List(context.Context, string) ([]domain.VideoSpeaker, error) {
	return f.speakers, f.listErr
}

func (f *fakeVideoSpeakerService) Name(ctx context.Context, _ string) ([]domain.VideoSpeaker, error) {
	f.nameCalls++
	_, f.hadTimeout = ctx.Deadline()
	return f.speakers, f.nameErr
}

func (f *fakeVideoSpeakerService) Update(_ context.Context, _, label string, update service.SpeakerUpdate) (domain.VideoSpeaker, error) {
	f.lastLabel, f.lastUpdate = label, update
	if f.updateErr != nil {
		return domain.VideoSpeaker{}, f.updateErr
	}
	return domain.VideoSpeaker{
		Label: label, Name: update.Name, Role: update.Role, Confidence: 1,
		Method: domain.SpeakerMethodOperator, OperatorSet: true,
	}, nil
}

var _ VideoSpeakerService = (*fakeVideoSpeakerService)(nil)

func namedSpeakers() []domain.VideoSpeaker {
	return []domain.VideoSpeaker{
		{
			Label: "A", Name: "Marine Durand", Role: domain.SpeakerRolePrincipal, Confidence: 0.92, Method: domain.SpeakerMethodSignals,
			Evidence: []domain.SpeakerEvidence{{Kind: domain.SpeakerEvidenceHandoff, Quote: "Madame Durand, vous avez deux minutes.", At: 90500 * time.Millisecond}},
		},
		{Label: "B", Role: domain.SpeakerRoleModerator, Method: domain.SpeakerMethodNone},
	}
}

func speakerRequest(t *testing.T, method, body string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequestWithContext(adminContext(t.Context()), method, "/api/videos/v1/speakers", strings.NewReader(body))
	req.SetPathValue("id", "v1")
	req.SetPathValue("label", "A")
	return httptest.NewRecorder(), req
}

func TestListVideoSpeakersHandler(t *testing.T) {
	t.Parallel()
	rec, req := speakerRequest(t, http.MethodGet, "")
	listVideoSpeakersHandler(&fakeVideoSpeakerService{speakers: namedSpeakers()})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]any{"speakers": []any{
		map[string]any{
			"label": "A", "name": "Marine Durand", "role": "principal", "confidence": 0.92, "method": "signals", "operator_set": false,
			"evidence": []any{map[string]any{"kind": "handoff", "quote": "Madame Durand, vous avez deux minutes.", "at": 90.5}},
		},
		map[string]any{"label": "B", "name": "", "role": "moderator", "confidence": float64(0), "method": "none", "operator_set": false, "evidence": []any{}},
	}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
}

func TestListVideoSpeakersHandlerEmptyAndErrors(t *testing.T) {
	t.Parallel()
	rec, req := speakerRequest(t, http.MethodGet, "")
	listVideoSpeakersHandler(&fakeVideoSpeakerService{})(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"speakers":[]}` {
		t.Errorf("unnamed video = %d %s, want 200 with an empty list", rec.Code, rec.Body.String())
	}

	rec, req = speakerRequest(t, http.MethodGet, "")
	listVideoSpeakersHandler(&fakeVideoSpeakerService{listErr: errors.New("boom")})(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("store failure = %d, want 500", rec.Code)
	}

	rec, req = speakerRequest(t, http.MethodGet, "")
	listVideoSpeakersHandler(nil)(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no service = %d, want 503", rec.Code)
	}
}

func TestNameVideoSpeakersHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "named", wantCode: http.StatusOK},
		{name: "unknown video", err: domain.ErrVideoNotFound, wantCode: http.StatusNotFound},
		{name: "no stored analysis", err: domain.ErrVideoAnalysisNotFound, wantCode: http.StatusConflict},
		{name: "step failed", err: errors.New("model down"), wantCode: http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeVideoSpeakerService{speakers: namedSpeakers(), nameErr: tc.err}
			rec, req := speakerRequest(t, http.MethodPost, "")
			nameVideoSpeakersHandler(svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if svc.nameCalls != 1 || !svc.hadTimeout {
				t.Errorf("name calls = %d, bounded context = %v", svc.nameCalls, svc.hadTimeout)
			}
			if tc.err == nil && !strings.Contains(rec.Body.String(), `"name":"Marine Durand"`) {
				t.Errorf("body lacks the mapping: %s", rec.Body.String())
			}
		})
	}
}

func TestUpdateVideoSpeakerHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		err      error
		wantCode int
	}{
		{name: "updated", body: `{"name":"Sophie Garnier","role":"guest"}`, wantCode: http.StatusOK},
		{name: "malformed body", body: `{`, wantCode: http.StatusBadRequest},
		{name: "oversized body", body: `{"name":"` + strings.Repeat("a", maxSpeakerUpdateBytes) + `"}`, wantCode: http.StatusRequestEntityTooLarge},
		{name: "invalid role", body: `{"name":"x","role":"host"}`, err: domain.ErrInvalidSpeakerRole, wantCode: http.StatusBadRequest},
		{name: "invalid name", body: `{"name":"x","role":"guest"}`, err: domain.ErrInvalidSpeakerName, wantCode: http.StatusBadRequest},
		{name: "unknown video", body: `{"role":"guest"}`, err: domain.ErrVideoNotFound, wantCode: http.StatusNotFound},
		{name: "unknown speaker", body: `{"role":"guest"}`, err: domain.ErrVideoSpeakerNotFound, wantCode: http.StatusNotFound},
		{name: "no stored analysis", body: `{"role":"guest"}`, err: domain.ErrVideoAnalysisNotFound, wantCode: http.StatusConflict},
		{name: "store failure", body: `{"role":"guest"}`, err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeVideoSpeakerService{updateErr: tc.err}
			rec, req := speakerRequest(t, http.MethodPatch, tc.body)
			updateVideoSpeakerHandler(svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			if svc.lastLabel != "A" || svc.lastUpdate != (service.SpeakerUpdate{Name: "Sophie Garnier", Role: domain.SpeakerRoleGuest}) {
				t.Errorf("forwarded label %q update %+v", svc.lastLabel, svc.lastUpdate)
			}
			var got videoSpeakerJSON
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Name != "Sophie Garnier" || got.Role != "guest" || !got.OperatorSet {
				t.Errorf("response = %+v", got)
			}
		})
	}
}

// TestVideoSpeakerRoutesRoleGating runs through NewMux so the real identity
// and admin gates are exercised: reading the mapping serves any authenticated
// caller, re-running the step and correcting a speaker are admin-only.
func TestVideoSpeakerRoutesRoleGating(t *testing.T) {
	t.Parallel()
	const update = `{"name":"Sophie Garnier","role":"guest"}`
	tests := []struct {
		name         string
		method, path string
		body, bearer string
		wantCode     int
	}{
		{name: "read as guest", method: http.MethodGet, path: "/api/videos/v1/speakers", bearer: testGuestToken, wantCode: http.StatusOK},
		{name: "read anonymous", method: http.MethodGet, path: "/api/videos/v1/speakers", wantCode: http.StatusUnauthorized},
		{name: "re-run as admin", method: http.MethodPost, path: "/api/videos/v1/speakers/name", bearer: testAdminToken, wantCode: http.StatusOK},
		{name: "re-run as guest", method: http.MethodPost, path: "/api/videos/v1/speakers/name", bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "re-run anonymous", method: http.MethodPost, path: "/api/videos/v1/speakers/name", wantCode: http.StatusUnauthorized},
		{name: "update as admin", method: http.MethodPatch, path: "/api/videos/v1/speakers/A", body: update, bearer: testAdminToken, wantCode: http.StatusOK},
		{name: "update as guest", method: http.MethodPatch, path: "/api/videos/v1/speakers/A", body: update, bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "update anonymous", method: http.MethodPatch, path: "/api/videos/v1/speakers/A", body: update, wantCode: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(tc.body))
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

func TestGetVideoAnalysisHandlerCarriesSpeakers(t *testing.T) {
	t.Parallel()
	view := completedAnalysisView(time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC))
	decode := func(t *testing.T, speakers speakerLister) map[string]any {
		t.Helper()
		rec, req := analysisRequest(t)
		getVideoAnalysisHandler(&fakeVideoAnalysisService{view: view}, speakers, false)(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}

	got := decode(t, &fakeVideoSpeakerService{speakers: namedSpeakers()})
	speakers, ok := got["speakers"].([]any)
	if !ok || len(speakers) != 2 {
		t.Fatalf("speakers = %v, want the two-label mapping", got["speakers"])
	}
	if first := speakers[0].(map[string]any); first["label"] != "A" || first["name"] != "Marine Durand" {
		t.Errorf("first speaker = %v", first)
	}

	// A mapping that cannot be read never fails the analysis read.
	got = decode(t, &fakeVideoSpeakerService{listErr: errors.New("boom")})
	if _, present := got["speakers"]; present {
		t.Errorf("speakers present despite a failed read: %v", got["speakers"])
	}
	if _, present := got["frames"]; !present {
		t.Error("frames missing when the speaker mapping failed")
	}
}

func TestGetVideoAnalysisHandlerOmitsSpeakersWithoutFrames(t *testing.T) {
	t.Parallel()
	svc := &fakeVideoAnalysisService{view: service.VideoAnalysisView{Video: domain.Video{ID: "v1", AnalysisStatus: domain.VideoAnalysisAnalysing}}}
	rec, req := analysisRequest(t)
	getVideoAnalysisHandler(svc, &fakeVideoSpeakerService{speakers: namedSpeakers()}, false)(rec, req)
	if strings.Contains(rec.Body.String(), "speakers") {
		t.Errorf("an analysis without frames carried speakers: %s", rec.Body.String())
	}
}

func TestExportsApplySpeakerNames(t *testing.T) {
	t.Parallel()
	replayer := &exportReplayer{found: true, events: []service.LiveEvent{
		{Kind: service.LiveEventSubtitle, Segment: domain.Segment{End: 2 * time.Second, Speaker: "A", Text: "Bonsoir."}},
		{Kind: service.LiveEventSubtitle, Segment: domain.Segment{Start: 2 * time.Second, End: 4 * time.Second, Speaker: "B", Text: "Merci."}},
		{Kind: service.LiveEventResult, ClaimID: "c1", ClaimStatus: service.ClaimStatusUnchecked, Segment: domain.Segment{Speaker: "A", Text: "Bonsoir."}},
	}}
	tests := []struct {
		name     string
		speakers speakerLister
		wantSRT  []string
		wantCSV  string
	}{
		{name: "named", speakers: &fakeVideoSpeakerService{speakers: namedSpeakers()}, wantSRT: []string{"Marine Durand: Bonsoir.", "B: Merci."}, wantCSV: ",Marine Durand\n"},
		{name: "no speaker service", speakers: nil, wantSRT: []string{"A: Bonsoir.", "B: Merci."}, wantCSV: ",\n"},
		{name: "mapping unreadable", speakers: &fakeVideoSpeakerService{listErr: errors.New("boom")}, wantSRT: []string{"A: Bonsoir.", "B: Merci."}, wantCSV: ",\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			videos := &fakeVideoService{playable: service.PlayableVideo{Video: domain.Video{ID: "v1", Title: "Débat"}}}
			rec, req := speakerRequest(t, http.MethodGet, "")
			exportTranscriptHandler(videos, replayer, tc.speakers)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("SRT status = %d", rec.Code)
			}
			for _, want := range tc.wantSRT {
				if !strings.Contains(rec.Body.String(), want) {
					t.Errorf("SRT lacks %q:\n%s", want, rec.Body.String())
				}
			}

			rec, req = speakerRequest(t, http.MethodGet, "")
			exportClaimsHandler(videos, replayer, tc.speakers)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("CSV status = %d", rec.Code)
			}
			if !strings.HasSuffix(rec.Body.String(), tc.wantCSV) {
				t.Errorf("CSV row does not end with %q:\n%s", tc.wantCSV, rec.Body.String())
			}
		})
	}
}
