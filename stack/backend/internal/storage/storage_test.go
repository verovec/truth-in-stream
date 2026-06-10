package storage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
