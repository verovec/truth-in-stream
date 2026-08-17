package storage

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func validConfig(endpoint string, pathStyle bool) Config {
	return Config{
		Endpoint:     endpoint,
		Region:       "eu-west-3",
		Bucket:       "media",
		AccessKey:    "AKIDEXAMPLE",
		SecretKey:    "secret",
		UsePathStyle: pathStyle,
		PutTTL:       15 * time.Minute,
		GetTTL:       time.Hour,
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "missing bucket",
			cfg:  Config{Region: "eu-west-3", PutTTL: time.Minute, GetTTL: time.Minute},
		},
		{
			name: "secret without access key",
			cfg:  Config{Bucket: "media", SecretKey: "s", PutTTL: time.Minute, GetTTL: time.Minute},
		},
		{
			name: "access key without secret",
			cfg:  Config{Bucket: "media", AccessKey: "k", PutTTL: time.Minute, GetTTL: time.Minute},
		},
		{
			name: "non-positive put ttl",
			cfg:  Config{Bucket: "media", PutTTL: 0, GetTTL: time.Minute},
		},
		{
			name: "non-positive get ttl",
			cfg:  Config{Bucket: "media", PutTTL: time.Minute, GetTTL: -time.Second},
		},
		{
			name: "put ttl above sigv4 maximum",
			cfg:  Config{Bucket: "media", PutTTL: maxPresignTTL + time.Hour, GetTTL: time.Minute},
		},
		{
			name: "get ttl above sigv4 maximum",
			cfg:  Config{Bucket: "media", PutTTL: time.Minute, GetTTL: maxPresignTTL + time.Hour},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(t.Context(), tc.cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestPresignUploadPathStyle(t *testing.T) {
	t.Parallel()
	store, err := New(t.Context(), validConfig("http://minio.example:9000", true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	req, err := store.PresignUpload(t.Context(), "videos/clip.mp4")
	if err != nil {
		t.Fatalf("presign upload: %v", err)
	}
	if req.Method != http.MethodPut {
		t.Errorf("method = %q, want PUT", req.Method)
	}

	u := mustParse(t, req.URL)
	if u.Host != "minio.example:9000" {
		t.Errorf("host = %q, want minio.example:9000", u.Host)
	}
	// Path-style addressing puts the bucket in the path, not the host.
	if u.Path != "/media/videos/clip.mp4" {
		t.Errorf("path = %q, want /media/videos/clip.mp4", u.Path)
	}
	q := u.Query()
	if got := q.Get("X-Amz-Expires"); got != "900" {
		t.Errorf("X-Amz-Expires = %q, want 900", got)
	}
	if q.Get("X-Amz-Signature") == "" {
		t.Error("missing X-Amz-Signature")
	}
	// The host is always signed so the uploader cannot point the URL elsewhere.
	if !hasSignedHeader(req.SignedHeaders, "host") {
		t.Errorf("signed headers %v missing host", req.SignedHeaders)
	}
}

func TestPresignDownloadVirtualHosted(t *testing.T) {
	t.Parallel()
	// Empty endpoint + path-style off selects real AWS S3 addressing, proving
	// the same code path resolves the backend purely from config.
	store, err := New(t.Context(), validConfig("", false))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	req, err := store.PresignDownload(t.Context(), "videos/clip.mp4")
	if err != nil {
		t.Fatalf("presign download: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", req.Method)
	}

	u := mustParse(t, req.URL)
	if u.Host != "media.s3.eu-west-3.amazonaws.com" {
		t.Errorf("host = %q, want media.s3.eu-west-3.amazonaws.com", u.Host)
	}
	if u.Path != "/videos/clip.mp4" {
		t.Errorf("path = %q, want /videos/clip.mp4", u.Path)
	}
	if got := u.Query().Get("X-Amz-Expires"); got != "3600" {
		t.Errorf("X-Amz-Expires = %q, want 3600", got)
	}
}

func TestExists(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/media/present.mp4":
			w.WriteHeader(http.StatusOK)
		case "/media/missing.mp4":
			w.WriteHeader(http.StatusNotFound)
		default:
			// 403 is non-retryable, so the error case stays fast.
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	store, err := New(t.Context(), validConfig(srv.URL, true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		want    bool
		wantErr bool
	}{
		{name: "present object", key: "present.mp4", want: true},
		{name: "missing object", key: "missing.mp4", want: false},
		{name: "access denied is an error", key: "denied.mp4", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Exists(t.Context(), tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("exists = %v, want %v", got, tc.want)
			}
		})
	}
}

// objectServer is a minimal path-style object store: PUT records bytes and
// content type under the request path, GET serves them back, so the Upload and
// Download round trips exercise the real SDK request without a live S3.
type objectServer struct {
	mu      sync.Mutex
	objects map[string]storedObject
}

type storedObject struct {
	body        []byte
	contentType string
}

func newObjectServer() *objectServer {
	return &objectServer{objects: map[string]storedObject{}}
}

func (o *objectServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		o.mu.Lock()
		o.objects[r.URL.Path] = storedObject{body: body, contentType: r.Header.Get("Content-Type")}
		o.mu.Unlock()
		w.Header().Set("ETag", `"fixed"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		o.mu.Lock()
		obj, ok := o.objects[r.URL.Path]
		o.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if obj.contentType != "" {
			w.Header().Set("Content-Type", obj.contentType)
		}
		_, _ = w.Write(obj.body)
	case http.MethodDelete:
		// S3 DeleteObject is idempotent: deleting an absent key still returns
		// 204, so the fake mirrors that.
		o.mu.Lock()
		delete(o.objects, r.URL.Path)
		o.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (o *objectServer) get(path string) (storedObject, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	obj, ok := o.objects[path]
	return obj, ok
}

func TestUploadStoresObject(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newObjectServerHandler(t))
	defer srv.Close()
	objects := srv.Config.Handler.(*objectServer)

	store, err := New(t.Context(), validConfig(srv.URL, true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	want := []byte("fake video bytes")
	if err := store.Upload(t.Context(), "youtube/abc.mp4", bytes.NewReader(want), "video/mp4", int64(len(want))); err != nil {
		t.Fatalf("upload: %v", err)
	}

	got, ok := objects.get("/media/youtube/abc.mp4")
	if !ok {
		t.Fatal("object was not stored at the expected key")
	}
	if !bytes.Equal(got.body, want) {
		t.Errorf("stored body = %q, want %q", got.body, want)
	}
	if got.contentType != "video/mp4" {
		t.Errorf("content type = %q, want video/mp4", got.contentType)
	}
}

func TestDownloadStreamsObject(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newObjectServerHandler(t))
	defer srv.Close()

	store, err := New(t.Context(), validConfig(srv.URL, true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	want := []byte("downloadable bytes")
	if err := store.Upload(t.Context(), "youtube/dl.mp4", bytes.NewReader(want), "video/mp4", int64(len(want))); err != nil {
		t.Fatalf("upload: %v", err)
	}

	rc, err := store.Download(t.Context(), "youtube/dl.mp4")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("downloaded body = %q, want %q", got, want)
	}
}

// TestPresignUploadOnceSignsConstraints proves the strict upload presign binds
// the declared content type, exact length, and write-once semantics into the
// signature: the uploader cannot send a different size or type, and cannot
// overwrite an existing object, without failing the signature or precondition.
func TestPresignUploadOnceSignsConstraints(t *testing.T) {
	t.Parallel()
	store, err := New(t.Context(), validConfig("http://minio.example:9000", true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	req, err := store.PresignUploadOnce(t.Context(), "documents/doc-1/original.pdf", "application/pdf", 2048)
	if err != nil {
		t.Fatalf("presign upload once: %v", err)
	}
	if req.Method != http.MethodPut {
		t.Errorf("method = %q, want PUT", req.Method)
	}
	for header, want := range map[string]string{
		"Content-Type":   "application/pdf",
		"Content-Length": "2048",
		"If-None-Match":  "*",
	} {
		if !hasSignedHeader(req.SignedHeaders, header) {
			t.Errorf("signed headers %v missing %s", req.SignedHeaders, header)
			continue
		}
		if got := signedHeaderValue(req.SignedHeaders, header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if !hasSignedHeader(req.SignedHeaders, "host") {
		t.Errorf("signed headers %v missing host", req.SignedHeaders)
	}
}

func TestPresignUploadOnceRejectsBadInput(t *testing.T) {
	t.Parallel()
	store, err := New(t.Context(), validConfig("http://minio.example:9000", true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := store.PresignUploadOnce(t.Context(), "k", "", 1); err == nil {
		t.Error("empty content type accepted")
	}
	if _, err := store.PresignUploadOnce(t.Context(), "k", "application/pdf", 0); err == nil {
		t.Error("non-positive size accepted")
	}
}

func signedHeaderValue(headers map[string][]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func TestDeleteRemovesObject(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newObjectServerHandler(t))
	defer srv.Close()
	objects := srv.Config.Handler.(*objectServer)

	store, err := New(t.Context(), validConfig(srv.URL, true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	body := []byte("doomed bytes")
	if err := store.Upload(t.Context(), "documents/doc-1/original.pdf", bytes.NewReader(body), "application/pdf", int64(len(body))); err != nil {
		t.Fatalf("upload: %v", err)
	}

	if err := store.Delete(t.Context(), "documents/doc-1/original.pdf"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := objects.get("/media/documents/doc-1/original.pdf"); ok {
		t.Error("object still present after Delete")
	}
}

// TestDeleteMissingObjectSucceeds pins S3 delete semantics: removing an absent
// key is a no-op success, so a retried document deletion never fails on the
// already-removed object.
func TestDeleteMissingObjectSucceeds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newObjectServerHandler(t))
	defer srv.Close()

	store, err := New(t.Context(), validConfig(srv.URL, true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := store.Delete(t.Context(), "documents/absent/original.pdf"); err != nil {
		t.Errorf("delete of an absent key = %v, want nil", err)
	}
}

func TestDeleteServerErrorIsWrapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 403 is non-retryable, so the error case stays fast.
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	store, err := New(t.Context(), validConfig(srv.URL, true))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := store.Delete(t.Context(), "documents/denied/original.pdf"); err == nil {
		t.Error("expected error, got nil")
	}
}

// newObjectServerHandler returns a fresh objectServer; the helper exists so each
// test gets isolated storage while keeping the concrete type reachable for
// assertions via the server's Handler field.
func newObjectServerHandler(t *testing.T) *objectServer {
	t.Helper()
	return newObjectServer()
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func hasSignedHeader(headers map[string][]string, name string) bool {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// TestPresignPublicEndpoint proves the browser-issued upload and playback URLs
// are signed against PublicEndpoint, not the internal Endpoint the backend uses
// to reach storage. This is what lets a browser play a clip in local dev, where
// Endpoint is a Docker hostname the browser cannot resolve.
func TestPresignPublicEndpoint(t *testing.T) {
	t.Parallel()
	cfg := validConfig("http://minio:9000", true)
	cfg.PublicEndpoint = "http://localhost:9000"
	store, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	upload, err := store.PresignUpload(t.Context(), "videos/clip.mp4")
	if err != nil {
		t.Fatalf("presign upload: %v", err)
	}
	if host := mustParse(t, upload.URL).Host; host != "localhost:9000" {
		t.Errorf("upload host = %q, want localhost:9000", host)
	}

	download, err := store.PresignDownload(t.Context(), "videos/clip.mp4")
	if err != nil {
		t.Fatalf("presign download: %v", err)
	}
	if host := mustParse(t, download.URL).Host; host != "localhost:9000" {
		t.Errorf("download host = %q, want localhost:9000", host)
	}
	// The public host is signed, so the URL cannot be repointed after the fact.
	if !hasSignedHeader(download.SignedHeaders, "host") {
		t.Errorf("signed headers %v missing host", download.SignedHeaders)
	}

	// A server-side consumer (the pre-analysis ffmpeg fetch) dereferences its
	// URL from inside the backend's network horizon, so the internal download
	// presign must stay on the internal Endpoint even when a PublicEndpoint is
	// configured - and its host is signed too, so it cannot be repointed.
	internal, err := store.PresignInternalDownload(t.Context(), "videos/clip.mp4")
	if err != nil {
		t.Fatalf("presign internal download: %v", err)
	}
	if host := mustParse(t, internal.URL).Host; host != "minio:9000" {
		t.Errorf("internal download host = %q, want minio:9000", host)
	}
	if !hasSignedHeader(internal.SignedHeaders, "host") {
		t.Errorf("internal signed headers %v missing host", internal.SignedHeaders)
	}
}

// TestServerSideOpsIgnorePublicEndpoint proves Upload and Download address the
// internal Endpoint even when a PublicEndpoint is configured: the public host is
// only for browser-issued presigned URLs. The public endpoint points at a dead
// port, so a successful round trip can only mean the internal endpoint was used.
func TestServerSideOpsIgnorePublicEndpoint(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(newObjectServerHandler(t))
	defer srv.Close()

	cfg := validConfig(srv.URL, true)
	cfg.PublicEndpoint = "http://127.0.0.1:0"
	store, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	want := []byte("server-side bytes")
	if err := store.Upload(t.Context(), "youtube/srv.mp4", bytes.NewReader(want), "video/mp4", int64(len(want))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	rc, err := store.Download(t.Context(), "youtube/srv.mp4")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("downloaded body = %q, want %q", got, want)
	}
}
