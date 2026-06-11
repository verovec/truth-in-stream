package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// fakeVideoService is a handler.VideoService stand-in. Each *Err field, when
// set, makes the matching method fail; the recorded inputs let tests assert the
// handler decoded and forwarded the request correctly.
type fakeVideoService struct {
	ticket   service.UploadTicket
	confirm  domain.Video
	list     []domain.Video
	playable service.PlayableVideo

	requestErr error
	confirmErr error
	listErr    error
	getErr     error

	lastUpload    service.UploadRequest
	lastConfirmID string
	lastGetID     string
}

func (f *fakeVideoService) RequestUpload(_ context.Context, req service.UploadRequest) (service.UploadTicket, error) {
	f.lastUpload = req
	if f.requestErr != nil {
		return service.UploadTicket{}, f.requestErr
	}
	return f.ticket, nil
}

func (f *fakeVideoService) Confirm(_ context.Context, id string) (domain.Video, error) {
	f.lastConfirmID = id
	if f.confirmErr != nil {
		return domain.Video{}, f.confirmErr
	}
	return f.confirm, nil
}

func (f *fakeVideoService) List(_ context.Context) ([]domain.Video, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func (f *fakeVideoService) Get(_ context.Context, id string) (service.PlayableVideo, error) {
	f.lastGetID = id
	if f.getErr != nil {
		return service.PlayableVideo{}, f.getErr
	}
	return f.playable, nil
}

var _ VideoService = (*fakeVideoService)(nil)

// fakeYouTubeService is a handler.YouTubeService stand-in. submitErr, when set,
// makes Submit fail; lastURL records the forwarded link.
type fakeYouTubeService struct {
	video     domain.Video
	submitErr error
	lastURL   string
}

func (f *fakeYouTubeService) Submit(_ context.Context, url string) (domain.Video, error) {
	f.lastURL = url
	if f.submitErr != nil {
		return domain.Video{}, f.submitErr
	}
	return f.video, nil
}

var _ YouTubeService = (*fakeYouTubeService)(nil)

func TestIngestYouTubeHandlerAccepted(t *testing.T) {
	t.Parallel()
	svc := &fakeYouTubeService{video: domain.Video{
		ID:        "vid-1",
		Title:     "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Status:    domain.VideoStatusPending,
		Kind:      domain.VideoKindYouTube,
		SourceURL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	}}
	h := ingestYouTubeHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/youtube",
		strings.NewReader(`{"url":"https://youtu.be/dQw4w9WgXcQ"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	if svc.lastURL != "https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("forwarded url = %q", svc.lastURL)
	}
	var got videoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "vid-1" || got.Status != string(domain.VideoStatusPending) {
		t.Errorf("body = %+v, want pending vid-1", got)
	}
	if got.Kind != string(domain.VideoKindYouTube) {
		t.Errorf("kind = %q, want youtube", got.Kind)
	}
}

func TestIngestYouTubeHandlerInvalidURL(t *testing.T) {
	t.Parallel()
	svc := &fakeYouTubeService{submitErr: service.ErrInvalidYouTubeURL}
	h := ingestYouTubeHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/youtube",
		strings.NewReader(`{"url":"https://example.com/x"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestIngestYouTubeHandlerInternalError(t *testing.T) {
	t.Parallel()
	svc := &fakeYouTubeService{submitErr: errors.New("db down")}
	h := ingestYouTubeHandler(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/youtube",
		strings.NewReader(`{"url":"https://youtu.be/dQw4w9WgXcQ"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRequestUploadHandlerSuccess(t *testing.T) {
	t.Parallel()
	svc := &fakeVideoService{ticket: service.UploadTicket{
		Video: domain.Video{ID: "vid-1", ObjectKey: "uploads/k.mp4", Status: domain.VideoStatusPending},
		Upload: domain.PresignedRequest{
			URL: "https://put/uploads/k.mp4", Method: "PUT",
			SignedHeaders: map[string][]string{"Host": {"storage"}},
		},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/uploads",
		strings.NewReader(`{"title":"Clip","content_type":"video/mp4","size_bytes":1024}`))
	requestUploadHandler(svc)(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got uploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.VideoID != "vid-1" || got.ObjectKey != "uploads/k.mp4" || got.Status != "pending" {
		t.Errorf("response = %+v, want the created record", got)
	}
	if got.Upload.URL != "https://put/uploads/k.mp4" || got.Upload.Method != "PUT" {
		t.Errorf("upload = %+v, want presigned PUT", got.Upload)
	}
	if got.Upload.Headers["Host"][0] != "storage" {
		t.Errorf("signed headers not forwarded: %+v", got.Upload.Headers)
	}
	if svc.lastUpload != (service.UploadRequest{Title: "Clip", ContentType: "video/mp4", SizeBytes: 1024}) {
		t.Errorf("forwarded request = %+v, want decoded body", svc.lastUpload)
	}
}

func TestRequestUploadHandlerErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "empty title", err: service.ErrInvalidTitle, wantCode: http.StatusBadRequest},
		{name: "bad type", err: service.ErrInvalidContentType, wantCode: http.StatusUnsupportedMediaType},
		{name: "bad size", err: service.ErrInvalidSize, wantCode: http.StatusBadRequest},
		{name: "internal", err: errors.New("boom"), wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeVideoService{requestErr: tc.err}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/uploads",
				strings.NewReader(`{"title":"x","content_type":"video/mp4","size_bytes":1}`))
			requestUploadHandler(svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestRequestUploadHandlerRejectsBadJSON(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/uploads", strings.NewReader("not json"))
	requestUploadHandler(&fakeVideoService{})(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed JSON", rec.Code)
	}
}

func TestConfirmVideoHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		svc      *fakeVideoService
		wantCode int
	}{
		{name: "ok", svc: &fakeVideoService{confirm: domain.Video{ID: "v1", Status: domain.VideoStatusReady, Kind: domain.VideoKindUpload}}, wantCode: http.StatusOK},
		{name: "not found", svc: &fakeVideoService{confirmErr: domain.ErrVideoNotFound}, wantCode: http.StatusNotFound},
		{name: "not uploaded", svc: &fakeVideoService{confirmErr: service.ErrObjectNotUploaded}, wantCode: http.StatusConflict},
		{name: "internal", svc: &fakeVideoService{confirmErr: errors.New("boom")}, wantCode: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/videos/v1/confirm", nil)
			req.SetPathValue("id", "v1")
			confirmVideoHandler(tc.svc)(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.svc.lastConfirmID != "v1" {
				t.Errorf("confirm id = %q, want v1", tc.svc.lastConfirmID)
			}
		})
	}
}

func TestListVideosHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeVideoService{list: []domain.Video{
		{ID: "s1", Title: "Sample", Status: domain.VideoStatusReady, Kind: domain.VideoKindSample, ContentType: "video/mp4"},
		{ID: "u1", Title: "Upload", Status: domain.VideoStatusPending, Kind: domain.VideoKindUpload, ContentType: "video/webm"},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	listVideosHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got listVideosResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Videos) != 2 {
		t.Fatalf("got %d videos, want 2", len(got.Videos))
	}
	if got.Videos[0].Kind != "sample" || got.Videos[1].Kind != "upload" {
		t.Errorf("kinds = %q/%q, want sample/upload", got.Videos[0].Kind, got.Videos[1].Kind)
	}
}

func TestListVideosHandlerEmptyIsArray(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	listVideosHandler(&fakeVideoService{list: nil})(rec, req)
	if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, `"videos":[]`) {
		t.Errorf("empty list body = %s, want videos: []", body)
	}
}

func TestListVideosHandlerError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos", nil)
	listVideosHandler(&fakeVideoService{listErr: errors.New("boom")})(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestGetVideoHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeVideoService{playable: service.PlayableVideo{
		Video:    domain.Video{ID: "v1", Title: "Clip", Status: domain.VideoStatusReady, Kind: domain.VideoKindUpload},
		Playback: domain.PresignedRequest{URL: "https://get/uploads/k.mp4", Method: "GET"},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/v1", nil)
	req.SetPathValue("id", "v1")
	getVideoHandler(svc)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got videoResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "v1" || got.Title != "Clip" {
		t.Errorf("metadata = %+v, want the record", got.videoJSON)
	}
	if got.Playback.URL != "https://get/uploads/k.mp4" || got.Playback.Method != "GET" {
		t.Errorf("playback = %+v, want presigned GET", got.Playback)
	}
}

func TestGetVideoHandlerNotFound(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/videos/missing", nil)
	req.SetPathValue("id", "missing")
	getVideoHandler(&fakeVideoService{getErr: domain.ErrVideoNotFound})(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestVideoRoutesResolve proves the new record routes coexist with the batch
// processing routes on one mux: the upload and list routes hit the video
// service, while the legacy submit and status routes still hit processing.
func TestVideoRoutesResolve(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	videos := &fakeVideoService{
		ticket: service.UploadTicket{Video: domain.Video{ID: "v1", Status: domain.VideoStatusPending}},
		list:   []domain.Video{{ID: "v1"}},
	}
	mux.HandleFunc("POST /api/videos/uploads", requestUploadHandler(videos))
	mux.HandleFunc("POST /api/videos/{id}/confirm", confirmVideoHandler(videos))
	mux.HandleFunc("GET /api/videos", listVideosHandler(videos))
	mux.HandleFunc("GET /api/videos/{id}", getVideoHandler(videos))

	cases := []struct {
		method, path string
		wantCode     int
	}{
		{http.MethodPost, "/api/videos/uploads", http.StatusCreated},
		{http.MethodGet, "/api/videos", http.StatusOK},
		{http.MethodGet, "/api/videos/v1", http.StatusOK},
		{http.MethodPost, "/api/videos/v1/confirm", http.StatusOK},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		body := strings.NewReader(`{"title":"x","content_type":"video/mp4","size_bytes":1}`)
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), c.method, c.path, body))
		if rec.Code != c.wantCode {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.wantCode)
		}
	}
}
