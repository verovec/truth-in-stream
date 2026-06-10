package config

import (
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/db"},
			want: Config{Port: "8080", DatabaseURL: "postgres://localhost/db", DemoMediaDir: "demo"},
		},
		{
			name:    "missing database url fails",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "port override",
			env:  map[string]string{"PORT": "9090", "DATABASE_URL": "postgres://localhost/db"},
			want: Config{Port: "9090", DatabaseURL: "postgres://localhost/db", DemoMediaDir: "demo"},
		},
		{
			name: "demo media dir override",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/db", "DEMO_MEDIA_DIR": "/srv/media"},
			want: Config{Port: "8080", DatabaseURL: "postgres://localhost/db", DemoMediaDir: "/srv/media"},
		},
		{
			name: "cors allowed origin override",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/db", "CORS_ALLOWED_ORIGIN": "http://localhost:3000"},
			want: Config{Port: "8080", DatabaseURL: "postgres://localhost/db", DemoMediaDir: "demo", CORSAllowedOrigin: "http://localhost:3000"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := Load()
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

func TestLoadTranscription(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Transcription
		wantErr bool
	}{
		{
			name:    "missing api key fails",
			env:     map[string]string{"TRANSCRIPTION_API_KEY": ""},
			wantErr: true,
		},
		{
			name: "defaults applied",
			env:  map[string]string{"TRANSCRIPTION_API_KEY": "k"},
			want: Transcription{APIKey: "k", Model: "scribe_v2"},
		},
		{
			name: "model override",
			env:  map[string]string{"TRANSCRIPTION_API_KEY": "k", "TRANSCRIPTION_MODEL": "scribe_v1"},
			want: Transcription{APIKey: "k", Model: "scribe_v1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadTranscription()
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

func TestLoadEmbedding(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Embedding
		wantErr bool
	}{
		{
			name:    "missing api key fails",
			env:     map[string]string{"EMBEDDING_API_KEY": ""},
			wantErr: true,
		},
		{
			name: "defaults applied",
			env:  map[string]string{"EMBEDDING_API_KEY": "k"},
			want: Embedding{APIKey: "k", Model: "voyage-4", Dim: domain.EmbeddingDim},
		},
		{
			name: "model override",
			env:  map[string]string{"EMBEDDING_API_KEY": "k", "EMBEDDING_MODEL": "voyage-4-large"},
			want: Embedding{APIKey: "k", Model: "voyage-4-large", Dim: domain.EmbeddingDim},
		},
		{
			name: "matching dimension accepted",
			env:  map[string]string{"EMBEDDING_API_KEY": "k", "EMBEDDING_DIM": "1024"},
			want: Embedding{APIKey: "k", Model: "voyage-4", Dim: domain.EmbeddingDim},
		},
		{
			name:    "mismatched dimension fails",
			env:     map[string]string{"EMBEDDING_API_KEY": "k", "EMBEDDING_DIM": "512"},
			wantErr: true,
		},
		{
			name:    "non-numeric dimension fails",
			env:     map[string]string{"EMBEDDING_API_KEY": "k", "EMBEDDING_DIM": "big"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadEmbedding()
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

func TestLoadMatch(t *testing.T) {
	defaults := Match{
		TopK:             5,
		ScoreThreshold:   0.5,
		EmbedConcurrency: 4,
		Timeout:          10 * time.Second,
	}
	tests := []struct {
		name    string
		env     map[string]string
		want    Match
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{},
			want: defaults,
		},
		{
			name: "all overrides applied",
			env: map[string]string{
				"MATCH_TOP_K":             "10",
				"MATCH_SCORE_THRESHOLD":   "0.75",
				"MATCH_EMBED_CONCURRENCY": "2",
				"MATCH_TIMEOUT":           "30s",
			},
			want: Match{TopK: 10, ScoreThreshold: 0.75, EmbedConcurrency: 2, Timeout: 30 * time.Second},
		},
		{
			name: "negative threshold accepted",
			env:  map[string]string{"MATCH_SCORE_THRESHOLD": "-1"},
			want: Match{TopK: 5, ScoreThreshold: -1, EmbedConcurrency: 4, Timeout: 10 * time.Second},
		},
		{
			name:    "non-numeric top k fails",
			env:     map[string]string{"MATCH_TOP_K": "many"},
			wantErr: true,
		},
		{
			name:    "zero top k fails",
			env:     map[string]string{"MATCH_TOP_K": "0"},
			wantErr: true,
		},
		{
			name:    "non-numeric threshold fails",
			env:     map[string]string{"MATCH_SCORE_THRESHOLD": "high"},
			wantErr: true,
		},
		{
			name:    "threshold above cosine range fails",
			env:     map[string]string{"MATCH_SCORE_THRESHOLD": "1.5"},
			wantErr: true,
		},
		{
			name:    "NaN threshold fails",
			env:     map[string]string{"MATCH_SCORE_THRESHOLD": "NaN"},
			wantErr: true,
		},
		{
			name:    "top k beyond int32 fails",
			env:     map[string]string{"MATCH_TOP_K": "3000000000"},
			wantErr: true,
		},
		{
			name:    "zero concurrency fails",
			env:     map[string]string{"MATCH_EMBED_CONCURRENCY": "0"},
			wantErr: true,
		},
		{
			name:    "non-duration timeout fails",
			env:     map[string]string{"MATCH_TIMEOUT": "soon"},
			wantErr: true,
		},
		{
			name:    "non-positive timeout fails",
			env:     map[string]string{"MATCH_TIMEOUT": "0s"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadMatch()
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

func TestLoadWiki(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Wiki
		wantErr bool
	}{
		{
			name: "default corpus",
			env:  map[string]string{},
			want: Wiki{Corpus: "simplewiki"},
		},
		{
			name: "corpus override",
			env:  map[string]string{"WIKI_CORPUS": "enwiki"},
			want: Wiki{Corpus: "enwiki"},
		},
		{
			name:    "corpus must be a wiki dump name",
			env:     map[string]string{"WIKI_CORPUS": "not a dump"},
			wantErr: true,
		},
		{
			name:    "non-wikipedia project rejected",
			env:     map[string]string{"WIKI_CORPUS": "frwiktionary"},
			wantErr: true,
		},
		{
			name:    "underscore dump name rejected",
			env:     map[string]string{"WIKI_CORPUS": "zh_yuewiki"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadWiki()
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

func TestLoadWikiEmbed(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    WikiEmbed
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{},
			want: WikiEmbed{BatchSize: 128, Concurrency: 4, MaxRetries: 6, MaintenanceWorkMem: "512MB", MaxParallelWorkers: 7},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"WIKI_EMBED_BATCH_SIZE":           "256",
				"WIKI_EMBED_CONCURRENCY":          "8",
				"WIKI_EMBED_MAX_RETRIES":          "3",
				"WIKI_EMBED_MAINTENANCE_WORK_MEM": "2GB",
				"WIKI_EMBED_MAX_PARALLEL_WORKERS": "4",
			},
			want: WikiEmbed{BatchSize: 256, Concurrency: 8, MaxRetries: 3, MaintenanceWorkMem: "2GB", MaxParallelWorkers: 4},
		},
		{name: "batch size zero rejected", env: map[string]string{"WIKI_EMBED_BATCH_SIZE": "0"}, wantErr: true},
		{name: "batch size above voyage limit rejected", env: map[string]string{"WIKI_EMBED_BATCH_SIZE": "1001"}, wantErr: true},
		{name: "batch size non-numeric rejected", env: map[string]string{"WIKI_EMBED_BATCH_SIZE": "lots"}, wantErr: true},
		{name: "concurrency zero rejected", env: map[string]string{"WIKI_EMBED_CONCURRENCY": "0"}, wantErr: true},
		{name: "max retries zero rejected", env: map[string]string{"WIKI_EMBED_MAX_RETRIES": "0"}, wantErr: true},
		{name: "negative parallel workers rejected", env: map[string]string{"WIKI_EMBED_MAX_PARALLEL_WORKERS": "-1"}, wantErr: true},
		{name: "malformed work mem rejected", env: map[string]string{"WIKI_EMBED_MAINTENANCE_WORK_MEM": "512 megabytes"}, wantErr: true},
		{name: "work mem injection rejected", env: map[string]string{"WIKI_EMBED_MAINTENANCE_WORK_MEM": "512MB'; DROP TABLE wiki_chunks; --"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadWikiEmbed()
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
