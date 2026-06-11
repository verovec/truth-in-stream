package config

import (
	"testing"
	"time"
)

func TestLoadStorage(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Storage
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{"STORAGE_BUCKET": "media"},
			want: Storage{
				Region: "eu-west-3",
				Bucket: "media",
				PutTTL: 15 * time.Minute,
				GetTTL: time.Hour,
			},
		},
		{
			name:    "missing bucket fails",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "minio overrides",
			env: map[string]string{
				"STORAGE_BUCKET":          "media",
				"STORAGE_ENDPOINT":        "http://minio:9000",
				"STORAGE_PUBLIC_ENDPOINT": "http://localhost:9000",
				"STORAGE_REGION":          "us-east-1",
				"STORAGE_ACCESS_KEY":      "minioadmin",
				"STORAGE_SECRET_KEY":      "minioadmin",
				"STORAGE_USE_PATH_STYLE":  "true",
			},
			want: Storage{
				Endpoint:       "http://minio:9000",
				PublicEndpoint: "http://localhost:9000",
				Region:         "us-east-1",
				Bucket:         "media",
				AccessKey:      "minioadmin",
				SecretKey:      "minioadmin",
				UsePathStyle:   true,
				PutTTL:         15 * time.Minute,
				GetTTL:         time.Hour,
			},
		},
		{
			name: "presign ttl overrides",
			env: map[string]string{
				"STORAGE_BUCKET":          "media",
				"STORAGE_PRESIGN_PUT_TTL": "30m",
				"STORAGE_PRESIGN_GET_TTL": "2h",
			},
			want: Storage{
				Region: "eu-west-3",
				Bucket: "media",
				PutTTL: 30 * time.Minute,
				GetTTL: 2 * time.Hour,
			},
		},
		{
			name:    "access key without secret fails",
			env:     map[string]string{"STORAGE_BUCKET": "media", "STORAGE_ACCESS_KEY": "k"},
			wantErr: true,
		},
		{
			name:    "secret without access key fails",
			env:     map[string]string{"STORAGE_BUCKET": "media", "STORAGE_SECRET_KEY": "s"},
			wantErr: true,
		},
		{
			name:    "invalid path style bool fails",
			env:     map[string]string{"STORAGE_BUCKET": "media", "STORAGE_USE_PATH_STYLE": "yes"},
			wantErr: true,
		},
		{
			name:    "non-positive put ttl fails",
			env:     map[string]string{"STORAGE_BUCKET": "media", "STORAGE_PRESIGN_PUT_TTL": "0s"},
			wantErr: true,
		},
		{
			name:    "put ttl above sigv4 maximum fails",
			env:     map[string]string{"STORAGE_BUCKET": "media", "STORAGE_PRESIGN_PUT_TTL": "169h"},
			wantErr: true,
		},
		{
			name:    "get ttl above sigv4 maximum fails",
			env:     map[string]string{"STORAGE_BUCKET": "media", "STORAGE_PRESIGN_GET_TTL": "200h"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		// Subtests stay sequential: t.Setenv forbids t.Parallel.
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadStorage()
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
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
