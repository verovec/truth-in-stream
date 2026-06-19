package config

import (
	"testing"
	"time"
)

func TestLoadAnalysisCache(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		want        AnalysisCache
		wantEnabled bool
		wantErr     bool
	}{
		{
			name:        "disabled by default",
			env:         map[string]string{},
			want:        AnalysisCache{RedisURL: "", TTL: 24 * time.Hour},
			wantEnabled: false,
		},
		{
			name:        "enabled with redis url, default ttl",
			env:         map[string]string{"REDIS_URL": "redis://localhost:6379/0"},
			want:        AnalysisCache{RedisURL: "redis://localhost:6379/0", TTL: 24 * time.Hour},
			wantEnabled: true,
		},
		{
			name:        "ttl override",
			env:         map[string]string{"REDIS_URL": "redis://cache:6379", "ANALYSIS_CACHE_TTL": "1h30m"},
			want:        AnalysisCache{RedisURL: "redis://cache:6379", TTL: 90 * time.Minute},
			wantEnabled: true,
		},
		{
			name:    "non-positive ttl fails",
			env:     map[string]string{"ANALYSIS_CACHE_TTL": "0"},
			wantErr: true,
		},
		{
			name:    "unparseable ttl fails",
			env:     map[string]string{"ANALYSIS_CACHE_TTL": "soon"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadAnalysisCache()
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
				t.Fatalf("LoadAnalysisCache() = %+v, want %+v", got, tc.want)
			}
			if got.Enabled() != tc.wantEnabled {
				t.Fatalf("Enabled() = %v, want %v", got.Enabled(), tc.wantEnabled)
			}
		})
	}
}
