package config

import (
	"testing"

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
			want: Config{Port: "8080", DatabaseURL: "postgres://localhost/db"},
		},
		{
			name:    "missing database url fails",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "port override",
			env:  map[string]string{"PORT": "9090", "DATABASE_URL": "postgres://localhost/db"},
			want: Config{Port: "9090", DatabaseURL: "postgres://localhost/db"},
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
