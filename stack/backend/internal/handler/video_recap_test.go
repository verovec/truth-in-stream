package handler

import (
	"context"
	"encoding/csv"
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

// fakeVideoRecapService is a VideoRecapService stand-in.
type fakeVideoRecapService struct {
	recap service.Recap
	err   error
}

func (f *fakeVideoRecapService) Get(context.Context, string) (service.Recap, error) {
	return f.recap, f.err
}

var _ VideoRecapService = (*fakeVideoRecapService)(nil)

func recapRequest(t *testing.T) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/v1/recap", nil)
	req.SetPathValue("id", "v1")
	return httptest.NewRecorder(), req
}

func TestGetVideoRecapHandlerStatusCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		svc      VideoRecapService
		wantCode int
	}{
		{name: "recap", svc: &fakeVideoRecapService{}, wantCode: http.StatusOK},
		{name: "unknown video", svc: &fakeVideoRecapService{err: domain.ErrVideoNotFound}, wantCode: http.StatusNotFound},
		{name: "no completed analysis", svc: &fakeVideoRecapService{err: domain.ErrVideoAnalysisNotFound}, wantCode: http.StatusConflict},
		{name: "store failure", svc: &fakeVideoRecapService{err: errors.New("boom")}, wantCode: http.StatusInternalServerError},
		{name: "no service", svc: nil, wantCode: http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, req := recapRequest(t)
			getVideoRecapHandler(tc.svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestGetVideoRecapHandlerResponseShape(t *testing.T) {
	t.Parallel()
	recap := service.Recap{
		VideoID: "v1", Title: "Le débat", Duration: 90 * time.Second,
		AnalyzedAt: time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC),
		Speakers: []service.RecapSpeaker{{
			Label: "A", Name: "Marine Durand", Role: domain.SpeakerRolePrincipal, NameConfirmed: true,
			SpeakingTime: 61500 * time.Millisecond, Turns: 2, Share: 1,
			Counts: service.RecapCounts{Detected: 3, Credible: 1, Disputed: 1, NotVerified: 1},
			Disputed: []service.RecapStatement{{
				ClaimID: "c1", At: 12500 * time.Millisecond, Verdict: "disputed", Flags: []string{"cherry-picked"},
				Quote: "Les prix ont doublé", Rationale: "Les séries officielles disent le contraire.",
				Sources: []domain.Source{{Title: "INSEE", URL: "https://example.org/insee"}},
			}},
			Credible: []service.RecapStatement{{ClaimID: "c2", Verdict: "credible", Quote: "Le chômage a baissé"}},
		}},
		Principals: service.RecapGroup{SpeakingTime: 61500 * time.Millisecond, Turns: 2, Counts: service.RecapCounts{Detected: 3, Credible: 1, Disputed: 1, NotVerified: 1}},
	}
	rec, req := recapRequest(t)
	getVideoRecapHandler(&fakeVideoRecapService{recap: recap})(rec, req)

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	counts := map[string]any{"detected": float64(3), "credible": float64(1), "disputed": float64(1), "unverifiable": float64(0), "not_verified": float64(1)}
	zeroCounts := map[string]any{"detected": float64(0), "credible": float64(0), "disputed": float64(0), "unverifiable": float64(0), "not_verified": float64(0)}
	want := map[string]any{
		"video_id": "v1", "title": "Le débat", "analyzed_at": "2026-11-19T20:00:00Z", "duration": float64(90),
		"speakers": []any{map[string]any{
			"label": "A", "name": "Marine Durand", "role": "principal", "name_confirmed": true,
			"speaking_time": 61.5, "turns": float64(2), "share": float64(1), "counts": counts,
			"disputed": []any{map[string]any{
				"claim_id": "c1", "at": 12.5, "verdict": "disputed", "flags": []any{"cherry-picked"},
				"quote": "Les prix ont doublé", "rationale": "Les séries officielles disent le contraire.",
				"sources": []any{map[string]any{"title": "INSEE", "url": "https://example.org/insee"}},
			}},
			"credible": []any{map[string]any{
				"claim_id": "c2", "at": float64(0), "verdict": "credible", "flags": []any{},
				"quote": "Le chômage a baissé", "rationale": "", "sources": []any{},
			}},
		}},
		"principals": map[string]any{"speaking_time": 61.5, "turns": float64(2), "counts": counts},
		"others":     map[string]any{"speaking_time": float64(0), "turns": float64(0), "counts": zeroCounts},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("response mismatch (-want +got):\n%s", diff)
	}
}

func TestGetVideoRecapHandlerEmptyRecapHasEmptyLists(t *testing.T) {
	t.Parallel()
	rec, req := recapRequest(t)
	getVideoRecapHandler(&fakeVideoRecapService{})(rec, req)
	if !strings.Contains(rec.Body.String(), `"speakers":[]`) {
		t.Errorf("an empty recap must serialize an empty speaker list, not null: %s", rec.Body.String())
	}
}

// TestVideoRecapRouteAccess runs through NewMux: the recap serves any
// authenticated caller, like the analysis read, and nobody else.
func TestVideoRecapRouteAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		bearer   string
		wantCode int
	}{
		{name: "guest", bearer: testGuestToken, wantCode: http.StatusOK},
		{name: "admin", bearer: testAdminToken, wantCode: http.StatusOK},
		{name: "anonymous", wantCode: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newTestServer(nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/v1/recap", nil)
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

// TestRecapEndToEndReconcilesWithTheCSVExport drives the recap through the
// real mux, recap reader and stored-analysis reader, and reconciles it with
// the CSV export of the same analysis: taking the last row of each claim, the
// CSV's verdict counts equal the recap's principals plus others.
func TestRecapEndToEndReconcilesWithTheCSVExport(t *testing.T) {
	t.Parallel()
	subtitle := func(speaker string, start, end int, text string) service.LiveEvent {
		return service.LiveEvent{Kind: service.LiveEventSubtitle, Segment: domain.Segment{
			Speaker: speaker, Start: time.Duration(start) * time.Second, End: time.Duration(end) * time.Second, Text: text,
		}}
	}
	result := func(speaker, claimID string, status service.ClaimStatus, verdict string) service.LiveEvent {
		ev := service.LiveEvent{
			Kind: service.LiveEventResult, ClaimID: claimID, ClaimStatus: status,
			Segment: domain.Segment{Speaker: speaker, Start: 10 * time.Second, Text: "Une affirmation de " + speaker + "."},
		}
		if verdict != "" {
			ev.Verdict = &service.VerifiedVerdict{Verdict: verdict, Rationale: "justification", Citations: []domain.SegmentMatch{
				{Sources: []domain.Source{{Title: "INSEE", URL: "https://example.org/insee"}}},
			}}
		}
		return ev
	}
	events := []service.LiveEvent{
		subtitle("F", 0, 5, "Bonsoir."),
		subtitle("A", 5, 65, "Une affirmation de A."),
		result("A", "a1", service.ClaimStatusVerified, "disputed"),
		result("A", "a1", service.ClaimStatusVerified, "credible"),
		result("A", "a2", service.ClaimStatusVerified, "unverifiable"),
		result("A", "a3", service.ClaimStatusUnchecked, ""),
		subtitle("F", 65, 70, "Une affirmation de F."),
		result("F", "f1", service.ClaimStatusVerified, "disputed"),
		subtitle("B", 70, 130, "Une affirmation de B."),
		result("B", "b1", service.ClaimStatusVerified, "disputed"),
		result("B", "b2", service.ClaimStatusError, ""),
		subtitle("F", 130, 134, "Merci."),
	}
	payload, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	video := domain.Video{ID: "vid-1", Title: "Le grand débat", AnalysisStatus: domain.VideoAnalysisComplete}
	store := &speakerE2EStore{
		storedAnalysisStub: storedAnalysisStub{analysis: domain.VideoAnalysis{
			VideoID: "vid-1", SnapshotVersion: service.SnapshotVersion, Events: payload, Engine: []byte(`{}`),
		}},
		video: video,
		rows: map[string]domain.VideoSpeaker{
			"A": {Label: "A", Name: "Marine Durand", Role: domain.SpeakerRolePrincipal},
			"B": {Label: "B", Role: domain.SpeakerRolePrincipal},
			"F": {Label: "F", Name: "Claire Martin", Role: domain.SpeakerRoleModerator, OperatorSet: true},
		},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	reader, err := service.NewStoredAnalysisReader(store, store, logger)
	if err != nil {
		t.Fatalf("NewStoredAnalysisReader: %v", err)
	}
	recaps, err := service.NewRecapReader(store, reader, store)
	if err != nil {
		t.Fatalf("NewRecapReader: %v", err)
	}
	namer, err := service.NewSpeakerNamer(store, reader, store, nil, logger)
	if err != nil {
		t.Fatalf("NewSpeakerNamer: %v", err)
	}
	videos := &fakeVideoService{playable: service.PlayableVideo{Video: video}}
	srv := NewMux(service.NewHealthChecker(fakePinger{}), videos, reader, &fakeVideoAnalysisStarter{}, namer, recaps, &fakeDocumentService{}, &fakeDocumentAnalyzer{}, &fakeYouTubeService{}, &fakeTVChannelService{}, &fakeTVRecordingService{}, testTVHub(), stubLiveAnalyzer{}, nil, reader, nil, false, nil, "", globalTestAuth, logger)

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		bearer(req, testAdminToken)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d; body=%s", path, rec.Code, rec.Body.String())
		}
		return rec
	}

	var recap videoRecapResponse
	if err := json.Unmarshal(get("/api/videos/vid-1/recap").Body.Bytes(), &recap); err != nil {
		t.Fatalf("decode recap: %v", err)
	}

	// The report: two principals with a sheet each, named or under their
	// letter, and the moderator outside the sheets.
	byLabel := make(map[string]recapSpeakerJSON, len(recap.Speakers))
	var sheets recapCountsJSON
	for _, s := range recap.Speakers {
		byLabel[s.Label] = s
		if s.Role == "principal" {
			sheets.Detected += s.Counts.Detected
			sheets.Credible += s.Counts.Credible
			sheets.Disputed += s.Counts.Disputed
			sheets.Unverifiable += s.Counts.Unverifiable
			sheets.NotVerified += s.Counts.NotVerified
		}
	}
	if a := byLabel["A"]; a.Name != "Marine Durand" || a.Counts != (recapCountsJSON{Detected: 3, Credible: 1, Unverifiable: 1, NotVerified: 1}) || len(a.Credible) != 1 || len(a.Credible[0].Sources) != 1 {
		t.Errorf("sheet A = %+v", a)
	}
	if b := byLabel["B"]; b.Name != "" || b.Counts != (recapCountsJSON{Detected: 2, Disputed: 1, NotVerified: 1}) {
		t.Errorf("sheet B = %+v", b)
	}
	if f := byLabel["F"]; f.Role != "moderator" || !f.NameConfirmed || f.Share != 0 {
		t.Errorf("moderator F = %+v", f)
	}
	if sheets != recap.Principals.Counts {
		t.Errorf("recap totals %+v do not equal the sum of the sheets %+v", recap.Principals.Counts, sheets)
	}

	// The CSV export of the same analysis, reduced to the last row per claim.
	rows, err := csv.NewReader(get("/api/videos/vid-1/export/claims.csv").Body).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	column := make(map[string]int, len(rows[0]))
	for i, name := range rows[0] {
		column[name] = i
	}
	last := make(map[string]string)
	for _, row := range rows[1:] {
		last[row[column["claim_id"]]] = row[column["credibility_verdict"]]
	}
	var fromCSV recapCountsJSON
	for _, verdict := range last {
		fromCSV.Detected++
		switch verdict {
		case "credible":
			fromCSV.Credible++
		case "disputed":
			fromCSV.Disputed++
		case "unverifiable":
			fromCSV.Unverifiable++
		default:
			fromCSV.NotVerified++
		}
	}
	whole := recapCountsJSON{
		Detected:     recap.Principals.Counts.Detected + recap.Others.Counts.Detected,
		Credible:     recap.Principals.Counts.Credible + recap.Others.Counts.Credible,
		Disputed:     recap.Principals.Counts.Disputed + recap.Others.Counts.Disputed,
		Unverifiable: recap.Principals.Counts.Unverifiable + recap.Others.Counts.Unverifiable,
		NotVerified:  recap.Principals.Counts.NotVerified + recap.Others.Counts.NotVerified,
	}
	if whole != fromCSV {
		t.Errorf("recap %+v does not reconcile with the CSV export %+v", whole, fromCSV)
	}
}
