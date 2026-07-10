package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// fakeTVChannelService is the handler's narrow TVChannelService, recording the
// last call so tests can assert the wiring (pointer patch semantics, archive
// default) and returning canned results or errors to drive the status mapping.
type fakeTVChannelService struct {
	channels  []domain.TVChannel
	created   domain.TVChannel
	updated   domain.TVChannel
	createErr error
	updateErr error
	deleteErr error
	listErr   error

	lastInput service.TVChannelInput
	lastPatch service.TVChannelPatch
	lastID    string
	deleteCnt int
}

var _ TVChannelService = (*fakeTVChannelService)(nil)

func (f *fakeTVChannelService) List(context.Context) ([]domain.TVChannel, error) {
	return f.channels, f.listErr
}

func (f *fakeTVChannelService) Create(_ context.Context, in service.TVChannelInput) (domain.TVChannel, error) {
	f.lastInput = in
	if f.createErr != nil {
		return domain.TVChannel{}, f.createErr
	}
	return f.created, nil
}

func (f *fakeTVChannelService) Update(_ context.Context, id string, patch service.TVChannelPatch) (domain.TVChannel, error) {
	f.lastID = id
	f.lastPatch = patch
	if f.updateErr != nil {
		return domain.TVChannel{}, f.updateErr
	}
	return f.updated, nil
}

func (f *fakeTVChannelService) Delete(_ context.Context, id string) error {
	f.lastID = id
	f.deleteCnt++
	return f.deleteErr
}

func TestListTVChannelsHandler(t *testing.T) {
	t.Parallel()
	svc := &fakeTVChannelService{channels: []domain.TVChannel{
		{ID: "c1", Slug: "franceinfo", Name: "franceinfo", SourceKind: domain.TVSourceYouTube, SourceRef: "u", Enabled: false, ArchiveEnabled: true},
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/tv/channels", nil)
	listTVChannelsHandler(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got listTVChannelsResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(got.Channels))
	}
	if got.Channels[0].Live {
		t.Fatalf("live should be false until the hub card enriches it")
	}
	if got.Channels[0].Slug != "franceinfo" {
		t.Fatalf("slug = %q, want franceinfo", got.Channels[0].Slug)
	}
}

func TestCreateTVChannelHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		svc      *fakeTVChannelService
		wantCode int
	}{
		{
			name:     "created",
			body:     `{"slug":"bfmtv","name":"BFMTV","source_kind":"youtube","source_ref":"u"}`,
			svc:      &fakeTVChannelService{created: domain.TVChannel{ID: "c1", Slug: "bfmtv"}},
			wantCode: http.StatusCreated,
		},
		{
			name:     "invalid slug is 400",
			body:     `{"slug":"Bad Slug","name":"x","source_kind":"youtube","source_ref":"u"}`,
			svc:      &fakeTVChannelService{createErr: service.ErrTVChannelInvalidSlug},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "bad kind is 400",
			body:     `{"slug":"x","name":"x","source_kind":"widevine","source_ref":"u"}`,
			svc:      &fakeTVChannelService{createErr: service.ErrTVChannelInvalidKind},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "duplicate slug is 409",
			body:     `{"slug":"bfmtv","name":"BFMTV","source_kind":"youtube","source_ref":"u"}`,
			svc:      &fakeTVChannelService{createErr: domain.ErrDuplicateTVChannelSlug},
			wantCode: http.StatusConflict,
		},
		{
			name:     "invalid json is 400",
			body:     `{`,
			svc:      &fakeTVChannelService{},
			wantCode: http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tv/channels", strings.NewReader(tc.body))
			createTVChannelHandler(tc.svc).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// TestCreateTVChannelHandlerArchiveDefault proves an omitted archive_enabled
// defaults to true (the safe posture), while an explicit false is honored.
func TestCreateTVChannelHandlerArchiveDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "omitted defaults true", body: `{"slug":"x","name":"x","source_kind":"youtube","source_ref":"u"}`, want: true},
		{name: "explicit false honored", body: `{"slug":"x","name":"x","source_kind":"youtube","source_ref":"u","archive_enabled":false}`, want: false},
		{name: "explicit true honored", body: `{"slug":"x","name":"x","source_kind":"youtube","source_ref":"u","archive_enabled":true}`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := &fakeTVChannelService{created: domain.TVChannel{ID: "c1"}}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tv/channels", strings.NewReader(tc.body))
			createTVChannelHandler(svc).ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201", rec.Code)
			}
			if svc.lastInput.ArchiveEnabled != tc.want {
				t.Fatalf("ArchiveEnabled = %v, want %v", svc.lastInput.ArchiveEnabled, tc.want)
			}
		})
	}
}

func TestUpdateTVChannelHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		body     string
		svc      *fakeTVChannelService
		wantCode int
	}{
		{name: "ok", body: `{"enabled":true}`, svc: &fakeTVChannelService{updated: domain.TVChannel{ID: "c1", Enabled: true}}, wantCode: http.StatusOK},
		{name: "unknown is 404", body: `{"enabled":true}`, svc: &fakeTVChannelService{updateErr: domain.ErrTVChannelNotFound}, wantCode: http.StatusNotFound},
		{name: "bad kind is 400", body: `{"source_kind":"widevine"}`, svc: &fakeTVChannelService{updateErr: service.ErrTVChannelInvalidKind}, wantCode: http.StatusBadRequest},
		{name: "invalid json is 400", body: `{`, svc: &fakeTVChannelService{}, wantCode: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/tv/channels/c1", strings.NewReader(tc.body))
			req.SetPathValue("id", "c1")
			updateTVChannelHandler(tc.svc).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// TestUpdateTVChannelHandlerPatchSemantics proves the pointer body distinguishes
// an omitted field from an explicit false: only the present toggle is set.
func TestUpdateTVChannelHandlerPatchSemantics(t *testing.T) {
	t.Parallel()
	svc := &fakeTVChannelService{updated: domain.TVChannel{ID: "c1"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/tv/channels/c1", strings.NewReader(`{"enabled":false}`))
	req.SetPathValue("id", "c1")
	updateTVChannelHandler(svc).ServeHTTP(rec, req)

	if svc.lastPatch.Enabled == nil || *svc.lastPatch.Enabled != false {
		t.Fatalf("Enabled patch = %v, want non-nil false", svc.lastPatch.Enabled)
	}
	if svc.lastPatch.ArchiveEnabled != nil {
		t.Fatalf("ArchiveEnabled should be nil (omitted), got %v", *svc.lastPatch.ArchiveEnabled)
	}
	if svc.lastID != "c1" {
		t.Fatalf("id = %q, want c1", svc.lastID)
	}
}

func TestDeleteTVChannelHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		svc      *fakeTVChannelService
		wantCode int
	}{
		{name: "no content", svc: &fakeTVChannelService{}, wantCode: http.StatusNoContent},
		{name: "unknown is 404", svc: &fakeTVChannelService{deleteErr: domain.ErrTVChannelNotFound}, wantCode: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/tv/channels/c1", nil)
			req.SetPathValue("id", "c1")
			deleteTVChannelHandler(tc.svc).ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

// TestTVChannelRoutesRoleGating drives the routes through NewMux to prove the
// read serves any authenticated user while the mutations are admin-only: a guest
// lists but cannot create, edit, or delete (403), and an anonymous caller fails
// the identity gate first (401).
func TestTVChannelRoutesRoleGating(t *testing.T) {
	t.Parallel()
	srv := newTestServer(nil)
	tests := []struct {
		name     string
		method   string
		target   string
		body     string
		bearer   string
		wantCode int
	}{
		{name: "guest lists", method: http.MethodGet, target: "/api/tv/channels", bearer: testGuestToken, wantCode: http.StatusOK},
		{name: "anonymous list is 401", method: http.MethodGet, target: "/api/tv/channels", bearer: "", wantCode: http.StatusUnauthorized},
		{name: "guest create is 403", method: http.MethodPost, target: "/api/tv/channels", body: `{"slug":"x","name":"x","source_kind":"youtube","source_ref":"u"}`, bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "anonymous create is 401", method: http.MethodPost, target: "/api/tv/channels", body: `{}`, bearer: "", wantCode: http.StatusUnauthorized},
		{name: "guest patch is 403", method: http.MethodPatch, target: "/api/tv/channels/c1", body: `{"enabled":true}`, bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "guest delete is 403", method: http.MethodDelete, target: "/api/tv/channels/c1", bearer: testGuestToken, wantCode: http.StatusForbidden},
		{name: "admin create passes the gate", method: http.MethodPost, target: "/api/tv/channels", body: `{"slug":"x","name":"x","source_kind":"youtube","source_ref":"u"}`, bearer: testAdminToken, wantCode: http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.target, bodyReader)
			if tc.bearer != "" {
				bearer(req, tc.bearer)
			}
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.target, rec.Code, tc.wantCode)
			}
		})
	}
}
