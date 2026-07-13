package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// fakeTVRecordingService is the handler's narrow TVRecordingService: it records
// the last call and returns canned results or errors to drive status mapping.
type fakeTVRecordingService struct {
	ticket      service.UploadTicket
	registered  domain.Video
	pruned      int
	requestErr  error
	registerErr error
	pruneErr    error

	lastRequest   service.TVRecordingRequest
	lastVideoID   string
	lastRetention time.Duration
}

var _ TVRecordingService = (*fakeTVRecordingService)(nil)

func (f *fakeTVRecordingService) RequestUpload(_ context.Context, req service.TVRecordingRequest) (service.UploadTicket, error) {
	f.lastRequest = req
	if f.requestErr != nil {
		return service.UploadTicket{}, f.requestErr
	}
	return f.ticket, nil
}

func (f *fakeTVRecordingService) Register(_ context.Context, videoID string) (domain.Video, error) {
	f.lastVideoID = videoID
	if f.registerErr != nil {
		return domain.Video{}, f.registerErr
	}
	return f.registered, nil
}

func (f *fakeTVRecordingService) Prune(_ context.Context, retention time.Duration) (int, error) {
	f.lastRetention = retention
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	return f.pruned, nil
}

func TestRequestTVRecordingUploadHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		svc      *fakeTVRecordingService
		wantCode int
	}{
		{
			name: "created",
			body: `{"channel_id":"c1","recorded_at":"2026-07-10T20:00:00Z","content_type":"video/mp4","size_bytes":1000}`,
			svc: &fakeTVRecordingService{ticket: service.UploadTicket{
				Video:  domain.Video{ID: "v1", ObjectKey: "recordings/x/2026/07/10/200000.mp4", Status: domain.VideoStatusPending},
				Upload: domain.PresignedRequest{URL: "https://put/x", Method: "PUT"},
			}},
			wantCode: http.StatusCreated,
		},
		{
			name:     "unknown channel is 404",
			body:     `{"channel_id":"nope","recorded_at":"2026-07-10T20:00:00Z","content_type":"video/mp4","size_bytes":1000}`,
			svc:      &fakeTVRecordingService{requestErr: domain.ErrTVChannelNotFound},
			wantCode: http.StatusNotFound,
		},
		{
			name:     "bad content type is 415",
			body:     `{"channel_id":"c1","recorded_at":"2026-07-10T20:00:00Z","content_type":"video/webm","size_bytes":1000}`,
			svc:      &fakeTVRecordingService{requestErr: service.ErrTVRecordingInvalidContentType},
			wantCode: http.StatusUnsupportedMediaType,
		},
		{
			name:     "missing recorded-at is 400",
			body:     `{"channel_id":"c1","content_type":"video/mp4","size_bytes":1000}`,
			svc:      &fakeTVRecordingService{requestErr: service.ErrTVRecordingNoRecordedAt},
			wantCode: http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tv/recordings/uploads", strings.NewReader(tc.body))
			requestTVRecordingUploadHandler(tc.svc).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusCreated {
				var got uploadResponse
				if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if got.VideoID != "v1" || got.Upload.URL != "https://put/x" {
					t.Fatalf("response = %+v, want video v1 with presigned url", got)
				}
			}
		})
	}
}

func TestRegisterTVRecordingHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		svc      *fakeTVRecordingService
		wantCode int
	}{
		{"ok", &fakeTVRecordingService{registered: domain.Video{ID: "v1", Status: domain.VideoStatusReady, Kind: domain.VideoKindTV}}, http.StatusOK},
		{"unknown recording is 404", &fakeTVRecordingService{registerErr: domain.ErrVideoNotFound}, http.StatusNotFound},
		{"object missing is 409", &fakeTVRecordingService{registerErr: service.ErrObjectNotUploaded}, http.StatusConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tv/recordings", strings.NewReader(`{"video_id":"v1"}`))
			registerTVRecordingHandler(tc.svc).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusOK && tc.svc.lastVideoID != "v1" {
				t.Fatalf("register called with %q, want v1", tc.svc.lastVideoID)
			}
		})
	}
}

func TestPruneTVRecordingsHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeTVRecordingService{pruned: 3}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tv/recordings/prune", strings.NewReader(`{"retention_days":30}`))
	pruneTVRecordingsHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.lastRetention != 30*24*time.Hour {
		t.Fatalf("retention = %v, want 720h", svc.lastRetention)
	}
	var got tvRecordingPruneResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Deleted != 3 {
		t.Fatalf("deleted = %d, want 3", got.Deleted)
	}

	// A non-positive retention is rejected before the service is touched.
	rec = httptest.NewRecorder()
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tv/recordings/prune", strings.NewReader(`{"retention_days":0}`))
	pruneTVRecordingsHandler(&fakeTVRecordingService{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero retention status = %d, want 400", rec.Code)
	}
}
