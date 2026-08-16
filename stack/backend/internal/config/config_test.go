package config

import (
	"maps"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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
	// Neutralize every config-relevant variable before each case so the subtests
	// are hermetic. `make test` runs `go test` with DATABASE_URL exported by the
	// Makefile (the go-run targets need it); without this clearing the leaked value
	// masks the expected error in "missing database url fails". Load treats an empty
	// value as unset (requireEnv/getenv), so clearing to "" is equivalent to absent.
	configEnvKeys := []string{"DATABASE_URL", "PORT", "DEMO_MEDIA_DIR", "CORS_ALLOWED_ORIGIN"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range configEnvKeys {
				t.Setenv(k, "")
			}
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
			want: Transcription{APIKey: "k", Model: "u3-rt-pro"},
		},
		{
			name: "model and max speakers override",
			env:  map[string]string{"TRANSCRIPTION_API_KEY": "k", "TRANSCRIPTION_MODEL": "u3-rt", "TRANSCRIPTION_MAX_SPEAKERS": "3"},
			want: Transcription{APIKey: "k", Model: "u3-rt", MaxSpeakers: 3},
		},
		{
			name:    "negative max speakers fails",
			env:     map[string]string{"TRANSCRIPTION_API_KEY": "k", "TRANSCRIPTION_MAX_SPEAKERS": "-1"},
			wantErr: true,
		},
		{
			name:    "non-numeric max speakers fails",
			env:     map[string]string{"TRANSCRIPTION_API_KEY": "k", "TRANSCRIPTION_MAX_SPEAKERS": "two"},
			wantErr: true,
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

func TestLoadWorkerIdle(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{"unset disables idle exit", "", 0, false},
		{"explicit zero disables", "0", 0, false},
		{"positive enables drain-to-idle", "5m", 5 * time.Minute, false},
		{"negative is rejected", "-1s", 0, true},
		{"non-duration is rejected", "soon", 0, true},
		{"above the cap is rejected", "48h", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WORKER_IDLE_TIMEOUT", tc.raw)
			got, err := LoadWorkerIdle()
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
				t.Errorf("idle = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestEvidenceBinaryQuantizationMultiplier(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{"unset defaults to off", "", 0, false},
		{"explicit zero is off", "0", 0, false},
		{"positive enables", "10", 10, false},
		{"non-numeric fails", "lots", 0, true},
		{"negative out of range", "-1", 0, true},
		{"above the cap fails", "1001", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.raw != "" {
				t.Setenv("EVIDENCE_BQ_MULTIPLIER", tc.raw)
			} else {
				t.Setenv("EVIDENCE_BQ_MULTIPLIER", "")
			}
			got, err := EvidenceBinaryQuantizationMultiplier()
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
				t.Errorf("multiplier = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEvidenceNearDupSimilarity(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    float64
		wantErr bool
	}{
		{"unset defaults to off", "", 0, false},
		{"explicit zero is off", "0", 0, false},
		{"positive enables the gate", "0.97", 0.97, false},
		{"one is valid", "1", 1, false},
		{"non-numeric fails", "high", 0, true},
		{"negative out of range", "-0.1", 0, true},
		{"above one out of range", "1.5", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EVIDENCE_NEAR_DUP_SIMILARITY", tc.raw)
			got, err := EvidenceNearDupSimilarity()
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
				t.Errorf("similarity = %g, want %g", got, tc.want)
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
			want: Embedding{APIKey: "k", Model: "voyage-4-large", Dim: domain.EmbeddingDim},
		},
		{
			name: "model override",
			env:  map[string]string{"EMBEDDING_API_KEY": "k", "EMBEDDING_MODEL": "voyage-4"},
			want: Embedding{APIKey: "k", Model: "voyage-4", Dim: domain.EmbeddingDim},
		},
		{
			name: "matching dimension accepted",
			env:  map[string]string{"EMBEDDING_API_KEY": "k", "EMBEDDING_DIM": "1024"},
			want: Embedding{APIKey: "k", Model: "voyage-4-large", Dim: domain.EmbeddingDim},
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

// TestEmbeddingModel pins the keyless resolver: the default is asserted against
// the literal "voyage-4-large" (an independent oracle, so an unintended change to
// DefaultEmbeddingModel is caught), an override is honored, and resolution works
// with no EMBEDDING_API_KEY set - the property the offline seed path relies on.
func TestEmbeddingModel(t *testing.T) {
	t.Setenv("EMBEDDING_API_KEY", "")
	t.Setenv("EMBEDDING_MODEL", "")
	if got := EmbeddingModel(); got != "voyage-4-large" {
		t.Errorf("EmbeddingModel() default = %q, want voyage-4-large", got)
	}
	t.Setenv("EMBEDDING_MODEL", "voyage-4")
	if got := EmbeddingModel(); got != "voyage-4" {
		t.Errorf("EmbeddingModel() override = %q, want voyage-4", got)
	}
}

func TestLoadAuth(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	required := map[string]string{
		"AUTH_EMAIL":         "op@example.com",
		"AUTH_PASSWORD_HASH": "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2g",
		"SESSION_SECRET":     secret,
	}
	withRequired := func(extra map[string]string) map[string]string {
		env := maps.Clone(required)
		maps.Copy(env, extra)
		return env
	}
	tests := []struct {
		name    string
		env     map[string]string
		want    Auth
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  withRequired(nil),
			want: Auth{
				Email:         "op@example.com",
				PasswordHash:  "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2g",
				SessionSecret: secret,
				SessionTTL:    24 * time.Hour,
				SecureCookie:  true,
			},
		},
		{
			name:    "missing email fails",
			env:     withRequired(map[string]string{"AUTH_EMAIL": ""}),
			wantErr: true,
		},
		{
			name:    "missing password hash fails",
			env:     withRequired(map[string]string{"AUTH_PASSWORD_HASH": ""}),
			wantErr: true,
		},
		{
			name:    "missing session secret fails",
			env:     withRequired(map[string]string{"SESSION_SECRET": ""}),
			wantErr: true,
		},
		{
			name:    "short session secret fails",
			env:     withRequired(map[string]string{"SESSION_SECRET": "tooshort"}),
			wantErr: true,
		},
		{
			name: "ttl override applied",
			env:  withRequired(map[string]string{"SESSION_TTL": "1h"}),
			want: Auth{
				Email:         "op@example.com",
				PasswordHash:  "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2g",
				SessionSecret: secret,
				SessionTTL:    time.Hour,
				SecureCookie:  true,
			},
		},
		{
			name:    "non-duration ttl fails",
			env:     withRequired(map[string]string{"SESSION_TTL": "tomorrow"}),
			wantErr: true,
		},
		{
			name:    "non-positive ttl fails",
			env:     withRequired(map[string]string{"SESSION_TTL": "0s"}),
			wantErr: true,
		},
		{
			name: "insecure cookie flag disables the secure cookie",
			env:  withRequired(map[string]string{"AUTH_INSECURE_COOKIE": "true"}),
			want: Auth{
				Email:         "op@example.com",
				PasswordHash:  "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2g",
				SessionSecret: secret,
				SessionTTL:    24 * time.Hour,
				SecureCookie:  false,
			},
		},
		{
			name: "explicit false flag keeps the secure cookie",
			env:  withRequired(map[string]string{"AUTH_INSECURE_COOKIE": "false"}),
			want: Auth{
				Email:         "op@example.com",
				PasswordHash:  "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2g",
				SessionSecret: secret,
				SessionTTL:    24 * time.Hour,
				SecureCookie:  true,
			},
		},
		{
			name:    "invalid insecure cookie flag fails",
			env:     withRequired(map[string]string{"AUTH_INSECURE_COOKIE": "maybe"}),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadAuth()
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

func TestLoadTelemetry(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		got, err := LoadTelemetry()
		if err != nil {
			t.Fatalf("LoadTelemetry: %v", err)
		}
		want := Telemetry{Enabled: true, QueueDepth: 256, FlushEvery: time.Second, SampleRate: 1}
		if got != want {
			t.Errorf("LoadTelemetry() = %+v, want %+v", got, want)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		t.Setenv("TELEMETRY_ENABLED", "false")
		t.Setenv("TELEMETRY_QUEUE_DEPTH", "512")
		t.Setenv("TELEMETRY_SAMPLE_RATE", "0.25")
		t.Setenv("TELEMETRY_FLUSH_INTERVAL", "5s")
		got, err := LoadTelemetry()
		if err != nil {
			t.Fatalf("LoadTelemetry: %v", err)
		}
		want := Telemetry{Enabled: false, QueueDepth: 512, FlushEvery: 5 * time.Second, SampleRate: 0.25}
		if got != want {
			t.Errorf("LoadTelemetry() = %+v, want %+v", got, want)
		}
	})
	t.Run("rejects out-of-range values", func(t *testing.T) {
		for _, tc := range []struct{ key, value string }{
			{"TELEMETRY_SAMPLE_RATE", "0"},
			{"TELEMETRY_SAMPLE_RATE", "1.5"},
			{"TELEMETRY_QUEUE_DEPTH", "0"},
			{"TELEMETRY_FLUSH_INTERVAL", "-1s"},
		} {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadTelemetry(); err == nil {
				t.Errorf("LoadTelemetry with %s=%s returned nil error", tc.key, tc.value)
			}
			t.Setenv(tc.key, "")
		}
	})
}

func TestLoadMatch(t *testing.T) {
	defaults := Match{
		TopK:                  5,
		ScoreThreshold:        0.5,
		EvidenceTopK:          5,
		EvidenceThreshold:     0.6,
		MaxResults:            5,
		EmbedConcurrency:      4,
		Timeout:               10 * time.Second,
		ConfidenceClusterSize: 5,
		ConfidenceLeadWeight:  1,
		ConfidenceBodyWeight:  0.6,
		HybridSearch:          true,
		LexicalTopK:           20,
		RRFK:                  60,
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
				"MATCH_TOP_K":                    "10",
				"MATCH_SCORE_THRESHOLD":          "0.75",
				"MATCH_EVIDENCE_TOP_K":           "3",
				"MATCH_EVIDENCE_SCORE_THRESHOLD": "0.8",
				"MATCH_MAX_RESULTS":              "6",
				"MATCH_EMBED_CONCURRENCY":        "2",
				"MATCH_TIMEOUT":                  "30s",
				"MATCH_CONFIDENCE_CLUSTER_SIZE":  "3",
				"MATCH_CONFIDENCE_LEAD_WEIGHT":   "0.9",
				"MATCH_CONFIDENCE_BODY_WEIGHT":   "0.4",
				"MATCH_HYBRID_SEARCH":            "false",
				"MATCH_LEXICAL_TOP_K":            "30",
				"MATCH_RRF_K":                    "40",
				"MATCH_CLAIMS_EF_SEARCH":         "100",
				"MATCH_EVIDENCE_EF_SEARCH":       "150",
			},
			want: Match{TopK: 10, ScoreThreshold: 0.75, EvidenceTopK: 3, EvidenceThreshold: 0.8, MaxResults: 6, EmbedConcurrency: 2, Timeout: 30 * time.Second, ConfidenceClusterSize: 3, ConfidenceLeadWeight: 0.9, ConfidenceBodyWeight: 0.4, HybridSearch: false, LexicalTopK: 30, RRFK: 40, ClaimsEfSearch: 100, EvidenceEfSearch: 150},
		},
		{
			name:    "claims ef_search above pgvector max fails",
			env:     map[string]string{"MATCH_CLAIMS_EF_SEARCH": "1001"},
			wantErr: true,
		},
		{
			name:    "negative evidence ef_search fails",
			env:     map[string]string{"MATCH_EVIDENCE_EF_SEARCH": "-1"},
			wantErr: true,
		},
		{
			name: "negative threshold accepted",
			env:  map[string]string{"MATCH_SCORE_THRESHOLD": "-1"},
			want: Match{TopK: 5, ScoreThreshold: -1, EvidenceTopK: 5, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0.6, HybridSearch: true, LexicalTopK: 20, RRFK: 60},
		},
		{
			name: "evidence retrieval can be disabled",
			env:  map[string]string{"MATCH_EVIDENCE_TOP_K": "0"},
			want: Match{TopK: 5, ScoreThreshold: 0.5, EvidenceTopK: 0, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0.6, HybridSearch: true, LexicalTopK: 20, RRFK: 60},
		},
		{
			name: "zero body weight accepted disables body evidence",
			env:  map[string]string{"MATCH_CONFIDENCE_BODY_WEIGHT": "0"},
			want: Match{TopK: 5, ScoreThreshold: 0.5, EvidenceTopK: 5, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0, HybridSearch: true, LexicalTopK: 20, RRFK: 60},
		},
		{
			name: "hybrid search off leaves the lexical knobs at their defaults",
			env:  map[string]string{"MATCH_HYBRID_SEARCH": "false"},
			want: Match{TopK: 5, ScoreThreshold: 0.5, EvidenceTopK: 5, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0.6, HybridSearch: false, LexicalTopK: 20, RRFK: 60},
		},
		{
			name:    "non-boolean hybrid search fails",
			env:     map[string]string{"MATCH_HYBRID_SEARCH": "maybe"},
			wantErr: true,
		},
		{
			name:    "zero lexical top k fails",
			env:     map[string]string{"MATCH_LEXICAL_TOP_K": "0"},
			wantErr: true,
		},
		{
			name:    "zero RRF constant fails",
			env:     map[string]string{"MATCH_RRF_K": "0"},
			wantErr: true,
		},
		{
			name:    "evidence threshold above cosine range fails",
			env:     map[string]string{"MATCH_EVIDENCE_SCORE_THRESHOLD": "1.5"},
			wantErr: true,
		},
		{
			name:    "negative evidence top k fails",
			env:     map[string]string{"MATCH_EVIDENCE_TOP_K": "-1"},
			wantErr: true,
		},
		{
			name:    "zero max results fails",
			env:     map[string]string{"MATCH_MAX_RESULTS": "0"},
			wantErr: true,
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
		{
			name:    "zero confidence cluster size fails",
			env:     map[string]string{"MATCH_CONFIDENCE_CLUSTER_SIZE": "0"},
			wantErr: true,
		},
		{
			name:    "non-numeric confidence cluster size fails",
			env:     map[string]string{"MATCH_CONFIDENCE_CLUSTER_SIZE": "lots"},
			wantErr: true,
		},
		{
			name:    "lead weight above one fails",
			env:     map[string]string{"MATCH_CONFIDENCE_LEAD_WEIGHT": "1.5"},
			wantErr: true,
		},
		{
			name:    "negative body weight fails",
			env:     map[string]string{"MATCH_CONFIDENCE_BODY_WEIGHT": "-0.1"},
			wantErr: true,
		},
		{
			name:    "non-numeric body weight fails",
			env:     map[string]string{"MATCH_CONFIDENCE_BODY_WEIGHT": "heavy"},
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

func TestLoadDebugSearch(t *testing.T) {
	defaults := DebugSearch{Enabled: false, TopK: 10, Timeout: 10 * time.Second}
	tests := []struct {
		name    string
		env     map[string]string
		want    DebugSearch
		wantErr bool
	}{
		{
			name: "disabled by default",
			env:  map[string]string{},
			want: defaults,
		},
		{
			name: "enabled with overrides",
			env: map[string]string{
				"DEBUG_WIKI_SEARCH":         "true",
				"DEBUG_WIKI_SEARCH_TOP_K":   "25",
				"DEBUG_WIKI_SEARCH_TIMEOUT": "5s",
			},
			want: DebugSearch{Enabled: true, TopK: 25, Timeout: 5 * time.Second},
		},
		{
			name:    "non-boolean enable fails",
			env:     map[string]string{"DEBUG_WIKI_SEARCH": "maybe"},
			wantErr: true,
		},
		{
			name:    "zero top k fails",
			env:     map[string]string{"DEBUG_WIKI_SEARCH_TOP_K": "0"},
			wantErr: true,
		},
		{
			name:    "non-positive timeout fails",
			env:     map[string]string{"DEBUG_WIKI_SEARCH_TIMEOUT": "0s"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadDebugSearch()
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

func TestLoadDebugFactCheck(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    bool
		wantErr bool
	}{
		{name: "disabled by default", env: map[string]string{}, want: false},
		{name: "enabled when opted in", env: map[string]string{"DEBUG_FACT_CHECK": "true"}, want: true},
		{name: "explicitly disabled", env: map[string]string{"DEBUG_FACT_CHECK": "false"}, want: false},
		{name: "non-boolean fails", env: map[string]string{"DEBUG_FACT_CHECK": "maybe"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadDebugFactCheck()
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
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadLegacyPasswordLogin(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    bool
		wantErr bool
	}{
		{name: "retired by default", env: map[string]string{}, want: false},
		{name: "re-enabled when opted in", env: map[string]string{"LEGACY_PASSWORD_LOGIN": "true"}, want: true},
		{name: "explicitly disabled", env: map[string]string{"LEGACY_PASSWORD_LOGIN": "false"}, want: false},
		{name: "non-boolean fails", env: map[string]string{"LEGACY_PASSWORD_LOGIN": "maybe"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadLegacyPasswordLogin()
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
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadKeycloak(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Keycloak
	}{
		{
			name: "defaults to local dev realm",
			env:  map[string]string{},
			want: Keycloak{
				Issuer:   "http://localhost:8081/realms/truth-in-stream",
				ClientID: "truth-in-stream-web",
				JWKSURL:  "http://localhost:8081/realms/truth-in-stream/protocol/openid-connect/certs",
			},
		},
		{
			name: "issuer override derives the jwks url",
			env: map[string]string{
				"KEYCLOAK_ISSUER": "https://id.example.com/realms/prod",
			},
			want: Keycloak{
				Issuer:   "https://id.example.com/realms/prod",
				ClientID: "truth-in-stream-web",
				JWKSURL:  "https://id.example.com/realms/prod/protocol/openid-connect/certs",
			},
		},
		{
			name: "trailing slash on issuer is trimmed",
			env: map[string]string{
				"KEYCLOAK_ISSUER": "https://id.example.com/realms/prod/",
			},
			want: Keycloak{
				Issuer:   "https://id.example.com/realms/prod",
				ClientID: "truth-in-stream-web",
				JWKSURL:  "https://id.example.com/realms/prod/protocol/openid-connect/certs",
			},
		},
		{
			name: "explicit jwks url override and client id",
			env: map[string]string{
				"KEYCLOAK_ISSUER":    "https://id.example.com/realms/prod",
				"KEYCLOAK_CLIENT_ID": "another-client",
				"KEYCLOAK_JWKS_URL":  "https://internal/certs",
			},
			want: Keycloak{
				Issuer:   "https://id.example.com/realms/prod",
				ClientID: "another-client",
				JWKSURL:  "https://internal/certs",
			},
		},
		{
			// Locks the exact production wiring the prod Terraform sets: the
			// public login.jeminforme.fr issuer and the shared client id, with no
			// KEYCLOAK_JWKS_URL override, so prod validates tokens against the
			// single public issuer (JWKS derived from it) - the back-channel goes
			// to the same host via CloudFront, matching the dev-networking spec's
			// production behavior.
			name: "production jeminforme.fr issuer derives the public jwks url",
			env: map[string]string{
				"KEYCLOAK_ISSUER":    "https://login.jeminforme.fr/realms/truth-in-stream",
				"KEYCLOAK_CLIENT_ID": "truth-in-stream-web",
			},
			want: Keycloak{
				Issuer:   "https://login.jeminforme.fr/realms/truth-in-stream",
				ClientID: "truth-in-stream-web",
				JWKSURL:  "https://login.jeminforme.fr/realms/truth-in-stream/protocol/openid-connect/certs",
			},
		},
		{
			// The capture worker authenticates with a service-account client whose
			// azp differs from the web client; the server must accept it, so the
			// additional-client-ids list is parsed (comma-separated, trimmed).
			name: "additional client ids are parsed for service accounts",
			env: map[string]string{
				"KEYCLOAK_ISSUER":                "https://id.example.com/realms/prod",
				"KEYCLOAK_CLIENT_ID":             "truth-in-stream-web",
				"KEYCLOAK_ADDITIONAL_CLIENT_IDS": " tv-capture , other-worker ",
			},
			want: Keycloak{
				Issuer:              "https://id.example.com/realms/prod",
				ClientID:            "truth-in-stream-web",
				JWKSURL:             "https://id.example.com/realms/prod/protocol/openid-connect/certs",
				AdditionalClientIDs: []string{"tv-capture", "other-worker"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if diff := cmp.Diff(tc.want, LoadKeycloak()); diff != "" {
				t.Fatalf("LoadKeycloak() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadTVCapture(t *testing.T) {
	t.Run("disabled by default needs no secret", func(t *testing.T) {
		cfg, err := LoadTVCapture()
		if err != nil {
			t.Fatalf("LoadTVCapture: %v", err)
		}
		if cfg.Enabled || cfg.Active() {
			t.Fatalf("capture enabled by default")
		}
		if cfg.SegmentDuration != time.Hour {
			t.Errorf("segment duration = %v, want 1h", cfg.SegmentDuration)
		}
		if cfg.RetentionDays != 30 {
			t.Errorf("retention days = %d, want 30", cfg.RetentionDays)
		}
		if cfg.ClientID != "tv-capture" {
			t.Errorf("client id = %q, want tv-capture", cfg.ClientID)
		}
	})

	t.Run("enabled requires the client secret", func(t *testing.T) {
		t.Setenv("TV_CAPTURE_ENABLED", "true")
		if _, err := LoadTVCapture(); err == nil {
			t.Fatal("LoadTVCapture accepted enabled capture with no client secret")
		}
	})

	t.Run("tunables and derived token url", func(t *testing.T) {
		t.Setenv("TV_CAPTURE_ENABLED", "true")
		t.Setenv("TV_CAPTURE_CLIENT_SECRET", "s3cr3t")
		t.Setenv("KEYCLOAK_ISSUER", "https://id.example.com/realms/prod")
		t.Setenv("TV_SEGMENT_SECONDS", "1800")
		t.Setenv("TV_RECORDING_RETENTION_DAYS", "7")
		t.Setenv("TV_CAPTURE_BACKEND_URL", "http://backend:8080/")
		cfg, err := LoadTVCapture()
		if err != nil {
			t.Fatalf("LoadTVCapture: %v", err)
		}
		if !cfg.Active() {
			t.Fatal("capture not active")
		}
		if cfg.SegmentDuration != 30*time.Minute {
			t.Errorf("segment duration = %v, want 30m", cfg.SegmentDuration)
		}
		if cfg.RetentionDays != 7 {
			t.Errorf("retention days = %d, want 7", cfg.RetentionDays)
		}
		if cfg.BackendBaseURL != "http://backend:8080" {
			t.Errorf("backend base url = %q, want trimmed", cfg.BackendBaseURL)
		}
		if want := "https://id.example.com/realms/prod/protocol/openid-connect/token"; cfg.TokenURL != want {
			t.Errorf("token url = %q, want %q", cfg.TokenURL, want)
		}
	})
}

func TestLoadPrecheck(t *testing.T) {
	defaults := Precheck{Enabled: true, MinWords: 4, CoverageThreshold: 0.4, WikiCoverageEnabled: true, WikiCoverageThreshold: 0.46, CoverageEfSearch: 200}
	tests := []struct {
		name    string
		env     map[string]string
		want    Precheck
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
				"PRECHECK_ENABLED":                 "false",
				"PRECHECK_MIN_WORDS":               "6",
				"PRECHECK_COVERAGE_THRESHOLD":      "0.6",
				"PRECHECK_WIKI_COVERAGE_ENABLED":   "false",
				"PRECHECK_WIKI_COVERAGE_THRESHOLD": "0.5",
				"PRECHECK_COVERAGE_EF_SEARCH":      "120",
			},
			want: Precheck{Enabled: false, MinWords: 6, CoverageThreshold: 0.6, WikiCoverageEnabled: false, WikiCoverageThreshold: 0.5, CoverageEfSearch: 120},
		},
		{
			name:    "non-bool enabled fails",
			env:     map[string]string{"PRECHECK_ENABLED": "sometimes"},
			wantErr: true,
		},
		{
			name:    "zero min words fails",
			env:     map[string]string{"PRECHECK_MIN_WORDS": "0"},
			wantErr: true,
		},
		{
			name:    "threshold above cosine range fails",
			env:     map[string]string{"PRECHECK_COVERAGE_THRESHOLD": "1.5"},
			wantErr: true,
		},
		{
			name:    "NaN threshold fails",
			env:     map[string]string{"PRECHECK_COVERAGE_THRESHOLD": "NaN"},
			wantErr: true,
		},
		{
			name:    "non-bool wiki coverage enabled fails",
			env:     map[string]string{"PRECHECK_WIKI_COVERAGE_ENABLED": "maybe"},
			wantErr: true,
		},
		{
			name:    "wiki threshold above cosine range fails",
			env:     map[string]string{"PRECHECK_WIKI_COVERAGE_THRESHOLD": "1.5"},
			wantErr: true,
		},
		{
			name:    "NaN wiki threshold fails",
			env:     map[string]string{"PRECHECK_WIKI_COVERAGE_THRESHOLD": "NaN"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadPrecheck()
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

// TestDefaultWikiCoverageThresholdSitsInMeasuredSeparationGap fails if the
// default drifts out of the measured separation gap (see the constant's comment
// for the calibration). The slices are the per-statement top wiki cosine
// similarities behind that gap, so the regression guard is checked against the
// data, not just the chosen number.
func TestDefaultWikiCoverageThresholdSitsInMeasuredSeparationGap(t *testing.T) {
	onTopicTops := []float64{0.5143, 0.5535, 0.5541, 0.5918, 0.6012, 0.6488}
	offTopicTops := []float64{0.3162, 0.3423, 0.3795, 0.4129, 0.4180}

	floor := defaultPrecheckWikiCoverageThreshold
	for _, score := range onTopicTops {
		if score < floor {
			t.Errorf("on-topic top similarity %.4f is below the wiki coverage floor %.4f; it would be wrongly skipped as not_covered", score, floor)
		}
	}
	for _, score := range offTopicTops {
		if score >= floor {
			t.Errorf("off-topic top similarity %.4f is at or above the wiki coverage floor %.4f; it would be wrongly admitted to checking", score, floor)
		}
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
			want: WikiEmbed{MaintenanceWorkMem: "512MB", MaxParallelWorkers: 7},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"WIKI_EMBED_MAINTENANCE_WORK_MEM": "2GB",
				"WIKI_EMBED_MAX_PARALLEL_WORKERS": "4",
			},
			want: WikiEmbed{MaintenanceWorkMem: "2GB", MaxParallelWorkers: 4},
		},
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

func TestLoadWikiDelta(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    WikiDelta
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{},
			want: WikiDelta{RetentionDays: 30, BulkFraction: 0.25},
		},
		{
			name: "overrides applied",
			env:  map[string]string{"WIKI_DELTA_RETENTION_DAYS": "7", "WIKI_DELTA_BULK_FRACTION": "0.5"},
			want: WikiDelta{RetentionDays: 7, BulkFraction: 0.5},
		},
		{name: "retention zero rejected", env: map[string]string{"WIKI_DELTA_RETENTION_DAYS": "0"}, wantErr: true},
		{name: "retention above api limit rejected", env: map[string]string{"WIKI_DELTA_RETENTION_DAYS": "31"}, wantErr: true},
		{name: "retention non-numeric rejected", env: map[string]string{"WIKI_DELTA_RETENTION_DAYS": "weekly"}, wantErr: true},
		{name: "fraction below zero rejected", env: map[string]string{"WIKI_DELTA_BULK_FRACTION": "-0.1"}, wantErr: true},
		{name: "fraction above one rejected", env: map[string]string{"WIKI_DELTA_BULK_FRACTION": "1.5"}, wantErr: true},
		{name: "fraction non-numeric rejected", env: map[string]string{"WIKI_DELTA_BULK_FRACTION": "half"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadWikiDelta()
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

func TestLoadQueue(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Queue
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{"RABBITMQ_URL": "amqp://guest:guest@localhost:5672/"},
			want: Queue{URL: "amqp://guest:guest@localhost:5672/", Name: "embedding.jobs", MaxPriority: 10, Prefetch: 1, Version: "2", KnownVersions: []string{"2"}, DLQEnabled: true, ReconnectMinBackoff: defaultQueueMinBackoff, ReconnectMaxBackoff: defaultQueueMaxBackoff},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"RABBITMQ_URL":          "amqps://user:pass@broker:5671/",
				"RABBITMQ_QUEUE":        "embedding.priority",
				"RABBITMQ_MAX_PRIORITY": "255",
				"RABBITMQ_PREFETCH":     "16",
			},
			want: Queue{URL: "amqps://user:pass@broker:5671/", Name: "embedding.priority", MaxPriority: 255, Prefetch: 16, Version: "2", KnownVersions: []string{"2"}, DLQEnabled: true, ReconnectMinBackoff: defaultQueueMinBackoff, ReconnectMaxBackoff: defaultQueueMaxBackoff},
		},
		{
			name: "version list takes the newest as active",
			env: map[string]string{
				"RABBITMQ_URL":            "amqp://localhost",
				"RABBITMQ_QUEUE_VERSIONS": "1, 2, 20260612",
			},
			want: Queue{URL: "amqp://localhost", Name: "embedding.jobs", MaxPriority: 10, Prefetch: 1, Version: "20260612", KnownVersions: []string{"1", "2", "20260612"}, DLQEnabled: true, ReconnectMinBackoff: defaultQueueMinBackoff, ReconnectMaxBackoff: defaultQueueMaxBackoff},
		},
		{
			name: "resilience knobs overridden",
			env: map[string]string{
				"RABBITMQ_URL":                   "amqp://localhost",
				"RABBITMQ_DLQ_ENABLED":           "false",
				"RABBITMQ_RECONNECT_MIN_BACKOFF": "1s",
				"RABBITMQ_RECONNECT_MAX_BACKOFF": "1m",
			},
			want: Queue{URL: "amqp://localhost", Name: "embedding.jobs", MaxPriority: 10, Prefetch: 1, Version: "2", KnownVersions: []string{"2"}, DLQEnabled: false, ReconnectMinBackoff: time.Second, ReconnectMaxBackoff: time.Minute},
		},
		{name: "reconnect max below min rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_RECONNECT_MIN_BACKOFF": "10s", "RABBITMQ_RECONNECT_MAX_BACKOFF": "1s"}, wantErr: true},
		{name: "missing url rejected", env: map[string]string{}, wantErr: true},
		{name: "max priority zero rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_MAX_PRIORITY": "0"}, wantErr: true},
		{name: "max priority above byte rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_MAX_PRIORITY": "256"}, wantErr: true},
		{name: "max priority non-numeric rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_MAX_PRIORITY": "high"}, wantErr: true},
		{name: "negative prefetch rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_PREFETCH": "-1"}, wantErr: true},
		{name: "prefetch above uint16 rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_PREFETCH": "65536"}, wantErr: true},
		{name: "prefetch non-numeric rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_PREFETCH": "lots"}, wantErr: true},
		{name: "empty version rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_QUEUE_VERSIONS": "1,,2"}, wantErr: true},
		{name: "version with dot rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_QUEUE_VERSIONS": "1.2"}, wantErr: true},
		{name: "duplicate version rejected", env: map[string]string{"RABBITMQ_URL": "amqp://localhost", "RABBITMQ_QUEUE_VERSIONS": "1,1"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadQueue()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Fatalf("LoadQueue() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestQueueVersionedName(t *testing.T) {
	q := Queue{Name: "embedding.jobs", Version: "20260612"}
	if got, want := q.VersionedName(), "embedding.jobs.v20260612"; got != want {
		t.Fatalf("VersionedName() = %q, want %q", got, want)
	}
}

func TestLoadWikiProducer(t *testing.T) {
	defaults := WikiProducer{EnqueueBatchSize: 1000, DrainPollInterval: 5 * time.Second, DrainStallTimeout: 30 * time.Minute}
	tests := []struct {
		name    string
		env     map[string]string
		want    WikiProducer
		wantErr bool
	}{
		{name: "defaults applied", env: map[string]string{}, want: defaults},
		{
			name: "overrides applied",
			env: map[string]string{
				"WIKI_ENQUEUE_BATCH_SIZE":  "250",
				"WIKI_DRAIN_POLL_INTERVAL": "2s",
				"WIKI_DRAIN_STALL_TIMEOUT": "10m",
			},
			want: WikiProducer{EnqueueBatchSize: 250, DrainPollInterval: 2 * time.Second, DrainStallTimeout: 10 * time.Minute},
		},
		{name: "zero batch size rejected", env: map[string]string{"WIKI_ENQUEUE_BATCH_SIZE": "0"}, wantErr: true},
		{name: "non-numeric batch size rejected", env: map[string]string{"WIKI_ENQUEUE_BATCH_SIZE": "lots"}, wantErr: true},
		{name: "zero poll interval rejected", env: map[string]string{"WIKI_DRAIN_POLL_INTERVAL": "0s"}, wantErr: true},
		{name: "bad stall timeout rejected", env: map[string]string{"WIKI_DRAIN_STALL_TIMEOUT": "soon"}, wantErr: true},
		{name: "stall timeout below poll interval rejected", env: map[string]string{"WIKI_DRAIN_POLL_INTERVAL": "30s", "WIKI_DRAIN_STALL_TIMEOUT": "10s"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadWikiProducer()
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

func TestLoadWikiCluster(t *testing.T) {
	defaults := WikiCluster{K: 64, MaxIters: 20, Seed: 1, ReadBatch: 5000, WriteBatch: 1000}
	tests := []struct {
		name    string
		env     map[string]string
		want    WikiCluster
		wantErr bool
	}{
		{name: "defaults applied", env: map[string]string{}, want: defaults},
		{
			name: "overrides applied",
			env: map[string]string{
				"WIKI_CLUSTER_K":           "128",
				"WIKI_CLUSTER_MAX_ITERS":   "10",
				"WIKI_CLUSTER_SEED":        "99",
				"WIKI_CLUSTER_READ_BATCH":  "2000",
				"WIKI_CLUSTER_WRITE_BATCH": "500",
			},
			want: WikiCluster{K: 128, MaxIters: 10, Seed: 99, ReadBatch: 2000, WriteBatch: 500},
		},
		{name: "zero K rejected", env: map[string]string{"WIKI_CLUSTER_K": "0"}, wantErr: true},
		{name: "zero iterations rejected", env: map[string]string{"WIKI_CLUSTER_MAX_ITERS": "0"}, wantErr: true},
		{name: "non-numeric seed rejected", env: map[string]string{"WIKI_CLUSTER_SEED": "abc"}, wantErr: true},
		{name: "zero read batch rejected", env: map[string]string{"WIKI_CLUSTER_READ_BATCH": "0"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadWikiCluster()
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

func TestLoadEmbedWorker(t *testing.T) {
	defaults := EmbedWorker{Concurrency: 4, BatchSize: 128, BatchWait: 200 * time.Millisecond, MaxAttempts: 5, MaxBatchTokens: 96000, HTTPTimeout: 30 * time.Second, RequestsPerMinute: 0, EmbedMaxRetries: 6}
	tests := []struct {
		name    string
		env     map[string]string
		want    EmbedWorker
		wantErr bool
	}{
		{name: "defaults applied", env: map[string]string{}, want: defaults},
		{
			name: "overrides applied",
			env: map[string]string{
				"EMBED_WORKER_CONCURRENCY":       "8",
				"EMBED_WORKER_BATCH_SIZE":        "256",
				"EMBED_WORKER_BATCH_WAIT":        "500ms",
				"EMBED_WORKER_MAX_ATTEMPTS":      "3",
				"EMBED_WORKER_MAX_BATCH_TOKENS":  "50000",
				"EMBED_WORKER_HTTP_TIMEOUT":      "45s",
				"EMBED_WORKER_RPM":               "120",
				"EMBED_WORKER_EMBED_MAX_RETRIES": "2",
			},
			want: EmbedWorker{Concurrency: 8, BatchSize: 256, BatchWait: 500 * time.Millisecond, MaxAttempts: 3, MaxBatchTokens: 50000, HTTPTimeout: 45 * time.Second, RequestsPerMinute: 120, EmbedMaxRetries: 2},
		},
		{name: "batch size above provider cap rejected", env: map[string]string{"EMBED_WORKER_BATCH_SIZE": "1001"}, wantErr: true},
		{name: "batch tokens above provider ceiling rejected", env: map[string]string{"EMBED_WORKER_MAX_BATCH_TOKENS": "120001"}, wantErr: true},
		{name: "zero batch tokens rejected", env: map[string]string{"EMBED_WORKER_MAX_BATCH_TOKENS": "0"}, wantErr: true},
		{name: "zero batch size rejected", env: map[string]string{"EMBED_WORKER_BATCH_SIZE": "0"}, wantErr: true},
		{name: "non-positive batch wait rejected", env: map[string]string{"EMBED_WORKER_BATCH_WAIT": "0s"}, wantErr: true},
		{name: "zero concurrency rejected", env: map[string]string{"EMBED_WORKER_CONCURRENCY": "0"}, wantErr: true},
		{name: "zero max attempts rejected", env: map[string]string{"EMBED_WORKER_MAX_ATTEMPTS": "0"}, wantErr: true},
		{name: "non-positive timeout rejected", env: map[string]string{"EMBED_WORKER_HTTP_TIMEOUT": "0s"}, wantErr: true},
		{name: "negative rpm rejected", env: map[string]string{"EMBED_WORKER_RPM": "-1"}, wantErr: true},
		{name: "zero embed retries rejected", env: map[string]string{"EMBED_WORKER_EMBED_MAX_RETRIES": "0"}, wantErr: true},
		{name: "non-numeric concurrency rejected", env: map[string]string{"EMBED_WORKER_CONCURRENCY": "many"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadEmbedWorker()
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

func TestLoadLive(t *testing.T) {
	defaults := Live{Concurrency: 4, QueueDepth: 32, MaxSentences: 4}
	tests := []struct {
		name    string
		env     map[string]string
		want    Live
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{},
			want: defaults,
		},
		{
			name: "overrides applied",
			env:  map[string]string{"LIVE_CONCURRENCY": "8", "LIVE_QUEUE_DEPTH": "64", "LIVE_MAX_SENTENCES": "3"},
			want: Live{Concurrency: 8, QueueDepth: 64, MaxSentences: 3},
		},
		{name: "zero concurrency rejected", env: map[string]string{"LIVE_CONCURRENCY": "0"}, wantErr: true},
		{name: "negative concurrency rejected", env: map[string]string{"LIVE_CONCURRENCY": "-1"}, wantErr: true},
		{name: "non-numeric concurrency rejected", env: map[string]string{"LIVE_CONCURRENCY": "lots"}, wantErr: true},
		{name: "zero queue depth rejected", env: map[string]string{"LIVE_QUEUE_DEPTH": "0"}, wantErr: true},
		{name: "negative queue depth rejected", env: map[string]string{"LIVE_QUEUE_DEPTH": "-4"}, wantErr: true},
		{name: "non-numeric queue depth rejected", env: map[string]string{"LIVE_QUEUE_DEPTH": "deep"}, wantErr: true},
		{name: "zero sentence cap rejected", env: map[string]string{"LIVE_MAX_SENTENCES": "0"}, wantErr: true},
		{name: "non-numeric sentence cap rejected", env: map[string]string{"LIVE_MAX_SENTENCES": "many"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadLive()
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

func TestLoadConsistency(t *testing.T) {
	defaults := Consistency{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: false, APIKey: "", Model: "", TopK: 3, SimilarityFloor: 0.6}
	tests := []struct {
		name    string
		env     map[string]string
		want    Consistency
		wantErr bool
	}{
		{
			name: "off by default (deepseek)",
			env:  map[string]string{},
			want: defaults,
		},
		{
			name: "enabled with key and overrides under explicit anthropic",
			env: map[string]string{
				"LLM_PROVIDER":                 "anthropic",
				"CONSISTENCY_ENABLED":          "true",
				"CONSISTENCY_API_KEY":          "sk-test",
				"CONSISTENCY_MODEL":            "claude-haiku-4-5",
				"CONSISTENCY_TOP_K":            "5",
				"CONSISTENCY_SIMILARITY_FLOOR": "0.72",
			},
			want: Consistency{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "sk-test", Model: "claude-haiku-4-5", TopK: 5, SimilarityFloor: 0.72},
		},
		{
			name: "gemini provider reads gemini key",
			env: map[string]string{
				"LLM_PROVIDER":   "gemini",
				"GEMINI_API_KEY": "g-test",
			},
			want: Consistency{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g-test"}, Enabled: false, APIKey: "", Model: "", TopK: 3, SimilarityFloor: 0.6},
		},
		{
			name: "deepseek provider reads deepseek key",
			env: map[string]string{
				"DEEPSEEK_API_KEY": "d-test",
			},
			want: Consistency{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d-test"}, Enabled: false, APIKey: "", Model: "", TopK: 3, SimilarityFloor: 0.6},
		},
		{name: "unknown provider rejected", env: map[string]string{"LLM_PROVIDER": "mistral"}, wantErr: true},
		{name: "non-bool enabled rejected", env: map[string]string{"CONSISTENCY_ENABLED": "maybe"}, wantErr: true},
		{name: "zero top-k rejected", env: map[string]string{"CONSISTENCY_TOP_K": "0"}, wantErr: true},
		{name: "non-numeric top-k rejected", env: map[string]string{"CONSISTENCY_TOP_K": "many"}, wantErr: true},
		{name: "floor above range rejected", env: map[string]string{"CONSISTENCY_SIMILARITY_FLOOR": "1.5"}, wantErr: true},
		{name: "negative floor rejected", env: map[string]string{"CONSISTENCY_SIMILARITY_FLOOR": "-0.2"}, wantErr: true},
		{name: "non-numeric floor rejected", env: map[string]string{"CONSISTENCY_SIMILARITY_FLOOR": "close"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadConsistency()
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

func TestConsistencyActive(t *testing.T) {
	tests := []struct {
		name string
		cfg  Consistency
		want bool
	}{
		{"anthropic enabled with key", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "k"}, true},
		{"anthropic enabled without key degrades to off", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: ""}, false},
		{"anthropic disabled with key stays off", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: false, APIKey: "k"}, false},
		{"gemini with gemini key and no anthropic key stays active", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g"}, Enabled: true, APIKey: ""}, true},
		{"gemini without gemini key degrades to off", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderGemini}, Enabled: true, APIKey: "k"}, false},
		{"deepseek with deepseek key and no anthropic key stays active", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d"}, Enabled: true, APIKey: ""}, true},
		{"deepseek without deepseek key degrades to off", Consistency{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: true, APIKey: "k"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Active(); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadCheckWorthiness(t *testing.T) {
	defaults := CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: false, APIKey: "", Model: ""}
	tests := []struct {
		name    string
		env     map[string]string
		want    CheckWorthiness
		wantErr bool
	}{
		{
			name: "off by default (deepseek)",
			env:  map[string]string{},
			want: defaults,
		},
		{
			name: "enabled with key and model override under explicit anthropic",
			env: map[string]string{
				"LLM_PROVIDER":            "anthropic",
				"CHECKWORTHINESS_ENABLED": "true",
				"CHECKWORTHINESS_API_KEY": "sk-test",
				"CHECKWORTHINESS_MODEL":   "claude-haiku-4-5",
			},
			want: CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "sk-test", Model: "claude-haiku-4-5"},
		},
		{
			name: "gemini provider reads gemini key",
			env: map[string]string{
				"LLM_PROVIDER":   "gemini",
				"GEMINI_API_KEY": "g-test",
			},
			want: CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g-test"}, Enabled: false, APIKey: "", Model: ""},
		},
		{
			name: "deepseek provider reads deepseek key",
			env: map[string]string{
				"DEEPSEEK_API_KEY": "d-test",
			},
			want: CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d-test"}, Enabled: false, APIKey: "", Model: ""},
		},
		{name: "unknown provider rejected", env: map[string]string{"LLM_PROVIDER": "mistral"}, wantErr: true},
		{name: "non-bool enabled rejected", env: map[string]string{"CHECKWORTHINESS_ENABLED": "maybe"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadCheckWorthiness()
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

func TestCheckWorthinessActive(t *testing.T) {
	tests := []struct {
		name string
		cfg  CheckWorthiness
		want bool
	}{
		{"anthropic enabled with key", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "k"}, true},
		{"anthropic enabled without key degrades to off", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: ""}, false},
		{"anthropic disabled with key stays off", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: false, APIKey: "k"}, false},
		{"gemini with gemini key and no anthropic key stays active", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g"}, Enabled: true, APIKey: ""}, true},
		{"gemini without gemini key degrades to off", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderGemini}, Enabled: true, APIKey: "k"}, false},
		{"deepseek with deepseek key and no anthropic key stays active", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d"}, Enabled: true, APIKey: ""}, true},
		{"deepseek without deepseek key degrades to off", CheckWorthiness{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: true, APIKey: "k"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Active(); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadCrawl(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics, Category:Chemistry")
	t.Setenv("CRAWL_PROJECT", "simplewiki")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if got, want := len(c.Categories), 2; got != want {
		t.Fatalf("categories = %d, want %d", got, want)
	}
	if c.Categories[0] != "Category:Physics" || c.Categories[1] != "Category:Chemistry" {
		t.Errorf("categories = %v, want trimmed pair", c.Categories)
	}
	if c.Corpus != "simplewiki-crawl" {
		t.Errorf("corpus = %q, want simplewiki-crawl", c.Corpus)
	}
	if c.Project != "simplewiki" {
		t.Errorf("project = %q, want simplewiki", c.Project)
	}
	if c.MaxDepth != 1 || c.MaxPages != 5000 || !c.IncludeBody {
		t.Errorf("defaults wrong: depth=%d pages=%d body=%v", c.MaxDepth, c.MaxPages, c.IncludeBody)
	}
	if c.CheckpointPath != "/state/crawl-checkpoint.json" || c.ErrorBudget != 50 || c.GateFailClosed {
		t.Errorf("resilience defaults wrong: checkpoint=%q budget=%d failClosed=%v", c.CheckpointPath, c.ErrorBudget, c.GateFailClosed)
	}
}

func TestLoadCrawlResilienceKnobs(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_CHECKPOINT_PATH", "/data/cp.json")
	t.Setenv("CRAWL_ERROR_BUDGET", "7")
	t.Setenv("CRAWL_GATE_FAIL_MODE", "closed")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.CheckpointPath != "/data/cp.json" || c.ErrorBudget != 7 || !c.GateFailClosed {
		t.Fatalf("overrides wrong: checkpoint=%q budget=%d failClosed=%v", c.CheckpointPath, c.ErrorBudget, c.GateFailClosed)
	}
}

func TestLoadCrawlCheckpointDisabledWhenEmpty(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_CHECKPOINT_PATH", "")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.CheckpointPath != "" {
		t.Fatalf("checkpoint path = %q, want empty (resume disabled)", c.CheckpointPath)
	}
}

func TestLoadCrawlShardsCheckpointPath(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_SHARDS", "4")
	t.Setenv("CRAWL_SHARD_INDEX", "2")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	// Each shard gets its own checkpoint file so concurrent shards on one /state
	// volume do not clobber each other.
	if c.CheckpointPath != "/state/crawl-checkpoint.shard2.json" {
		t.Fatalf("sharded checkpoint = %q, want /state/crawl-checkpoint.shard2.json", c.CheckpointPath)
	}
}

func TestLoadCrawlRejectsBadGateFailMode(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_GATE_FAIL_MODE", "sometimes")
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl accepted an invalid CRAWL_GATE_FAIL_MODE")
	}
}

func TestLoadCrawlShardingDefaults(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.Shards != 1 || c.ShardIndex != 0 {
		t.Errorf("sharding defaults = shards %d index %d, want 1/0 (off)", c.Shards, c.ShardIndex)
	}
}

func TestLoadCrawlShardingValid(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_SHARDS", "4")
	t.Setenv("CRAWL_SHARD_INDEX", "2")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.Shards != 4 || c.ShardIndex != 2 {
		t.Errorf("sharding = shards %d index %d, want 4/2", c.Shards, c.ShardIndex)
	}
}

func TestLoadCrawlShardIndexOutOfRange(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_SHARDS", "2")
	t.Setenv("CRAWL_SHARD_INDEX", "2")
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl with CRAWL_SHARD_INDEX >= CRAWL_SHARDS = nil error, want error")
	}
}

func TestLoadCrawlInvalidShards(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_SHARDS", "0")
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl with CRAWL_SHARDS=0 = nil error, want error (min 1)")
	}
}

func TestLoadCrawlRequiresCategories(t *testing.T) {
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl with no CRAWL_CATEGORIES = nil error, want error")
	}
}

func TestLoadCrawlBlankCategories(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", " , ,")
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl with only blank categories = nil error, want error")
	}
}

func TestLoadCrawlCorpusOverride(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_PROJECT", "simplewiki")
	t.Setenv("CRAWL_CORPUS", "custom-corpus")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.Corpus != "custom-corpus" {
		t.Errorf("corpus = %q, want custom-corpus", c.Corpus)
	}
}

func TestLoadCrawlIncludeBodyFalse(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_INCLUDE_BODY", "false")
	c, err := LoadCrawl()
	if err != nil {
		t.Fatalf("LoadCrawl: %v", err)
	}
	if c.IncludeBody {
		t.Error("IncludeBody = true, want false")
	}
}

func TestLoadCrawlInvalidIncludeBody(t *testing.T) {
	t.Setenv("CRAWL_CATEGORIES", "Category:Physics")
	t.Setenv("CRAWL_INCLUDE_BODY", "notabool")
	if _, err := LoadCrawl(); err == nil {
		t.Fatal("LoadCrawl with bad CRAWL_INCLUDE_BODY = nil error, want error")
	}
}

func TestLoadCrawlCheckworthy(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    CrawlCheckworthy
		wantErr bool
	}{
		{
			name: "deepseek on by default with a deepseek key",
			// CRAWL_CHECKWORTHY left empty so the default (true) is exercised; with no
			// LLM_PROVIDER the default provider is DeepSeek, keyed on DEEPSEEK_API_KEY.
			env:  map[string]string{"CRAWL_CHECKWORTHY": "", "DEEPSEEK_API_KEY": "d-test"},
			want: CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d-test"}, Enabled: true, APIKey: "", Model: "", Concurrency: 8, RPM: 0},
		},
		{
			name: "anthropic on with an anthropic key",
			// The empty value also neutralizes any ambient export so the test is hermetic.
			env:  map[string]string{"CRAWL_CHECKWORTHY": "", "LLM_PROVIDER": "anthropic", "CHECKWORTHY_API_KEY": "sk-test"},
			want: CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "sk-test", Model: defaultStageModel, Concurrency: 8, RPM: 0},
		},
		{
			name: "gemini on with a gemini key, no anthropic key",
			env:  map[string]string{"CRAWL_CHECKWORTHY": "", "LLM_PROVIDER": "gemini", "GEMINI_API_KEY": "g-test"},
			want: CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g-test"}, Enabled: true, APIKey: "", Model: "", Concurrency: 8, RPM: 0},
		},
		{
			name:    "gemini on without a gemini key fails fast",
			env:     map[string]string{"CRAWL_CHECKWORTHY": "", "LLM_PROVIDER": "gemini", "GEMINI_API_KEY": "", "CHECKWORTHY_API_KEY": "sk-test"},
			wantErr: true,
		},
		{
			name: "deepseek default without a deepseek key fails fast",
			// CRAWL_CHECKWORTHY defaults to true, so a missing provider key is a hard
			// error. Both vars are pinned (gate on, no key) to stay hermetic.
			env:     map[string]string{"CRAWL_CHECKWORTHY": "", "DEEPSEEK_API_KEY": ""},
			wantErr: true,
		},
		{
			name: "disabled needs no key",
			env:  map[string]string{"CRAWL_CHECKWORTHY": "false"},
			want: CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: false, APIKey: "", Model: "", Concurrency: 8, RPM: 0},
		},
		{
			name: "overrides applied under explicit anthropic",
			env: map[string]string{
				"CRAWL_CHECKWORTHY":             "",
				"LLM_PROVIDER":                  "anthropic",
				"CHECKWORTHY_API_KEY":           "sk-test",
				"CRAWL_CHECKWORTHY_MODEL":       "claude-haiku-4-5",
				"CRAWL_CHECKWORTHY_CONCURRENCY": "16",
				"CRAWL_CHECKWORTHY_RPM":         "120",
			},
			want: CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "sk-test", Model: "claude-haiku-4-5", Concurrency: 16, RPM: 120},
		},
		{name: "non-bool enabled rejected", env: map[string]string{"CRAWL_CHECKWORTHY": "maybe"}, wantErr: true},
		{name: "zero concurrency rejected", env: map[string]string{"LLM_PROVIDER": "anthropic", "CHECKWORTHY_API_KEY": "sk-test", "CRAWL_CHECKWORTHY_CONCURRENCY": "0"}, wantErr: true},
		{name: "negative rpm rejected", env: map[string]string{"LLM_PROVIDER": "anthropic", "CHECKWORTHY_API_KEY": "sk-test", "CRAWL_CHECKWORTHY_RPM": "-1"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadCrawlCheckworthy()
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

func TestCrawlCheckworthyActive(t *testing.T) {
	tests := []struct {
		name string
		cfg  CrawlCheckworthy
		want bool
	}{
		{"anthropic enabled with key", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "k"}, true},
		{"anthropic enabled without key", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: ""}, false},
		{"anthropic disabled with key", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: false, APIKey: "k"}, false},
		{"gemini with gemini key and no anthropic key stays active", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g"}, Enabled: true, APIKey: ""}, true},
		{"gemini without gemini key degrades to off", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderGemini}, Enabled: true, APIKey: "k"}, false},
		{"deepseek with deepseek key and no anthropic key stays active", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d"}, Enabled: true, APIKey: ""}, true},
		{"deepseek without deepseek key degrades to off", CrawlCheckworthy{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: true, APIKey: "k"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Active(); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadCrawlQueueDefaultName(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://localhost")
	q, err := LoadCrawlQueue()
	if err != nil {
		t.Fatalf("LoadCrawlQueue: %v", err)
	}
	if q.VersionedName() != "crawl.chunks.v2" {
		t.Errorf("VersionedName = %q, want crawl.chunks.v1", q.VersionedName())
	}
}

func TestLoadCrawlQueueOverrideName(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://localhost")
	t.Setenv("RABBITMQ_CRAWL_QUEUE", "my.crawl")
	q, err := LoadCrawlQueue()
	if err != nil {
		t.Fatalf("LoadCrawlQueue: %v", err)
	}
	if q.VersionedName() != "my.crawl.v2" {
		t.Errorf("VersionedName = %q, want my.crawl.v1", q.VersionedName())
	}
}

func TestLoadCrawlWorkerDefaults(t *testing.T) {
	w, err := LoadCrawlWorker()
	if err != nil {
		t.Fatalf("LoadCrawlWorker: %v", err)
	}
	if w.Concurrency != 4 || w.MaxAttempts != 5 {
		t.Errorf("defaults wrong: concurrency=%d attempts=%d", w.Concurrency, w.MaxAttempts)
	}
}

func TestLoadCrawlWorkerOverrides(t *testing.T) {
	t.Setenv("CRAWL_WORKER_CONCURRENCY", "8")
	t.Setenv("CRAWL_WORKER_MAX_ATTEMPTS", "9")
	w, err := LoadCrawlWorker()
	if err != nil {
		t.Fatalf("LoadCrawlWorker: %v", err)
	}
	if w.Concurrency != 8 || w.MaxAttempts != 9 {
		t.Errorf("overrides wrong: concurrency=%d attempts=%d", w.Concurrency, w.MaxAttempts)
	}
}

func TestLoadVerifyPathDefaultsOff(t *testing.T) {
	t.Parallel()
	got, err := LoadVerifyPath()
	if err != nil {
		t.Fatalf("LoadVerifyPath: %v", err)
	}
	if got.Enabled {
		t.Error("verify path must default off")
	}
	if got.Active() {
		t.Error("verify path with no key must not be Active")
	}
	if got.MaxClaimsPerUnit != defaultVerifyMaxClaimsPerUnit || got.FastTau != defaultVerifyFastTau ||
		got.Concurrency != defaultVerifyConcurrency || got.QueueDepth != defaultVerifyQueueDepth ||
		got.FastDeadline != defaultVerifyFastDeadline || got.VerifyDeadline != defaultVerifyDeadline ||
		got.CacheTTL != defaultVerifyCacheTTL || got.RetrievalThreshold != defaultVerifyRetrievalThreshold ||
		got.CacheThreshold != defaultVerifyCacheThreshold || got.CacheMaxEntries != defaultVerifyCacheMaxEntries {
		t.Errorf("defaults wrong: %+v", got)
	}
	if got.Provider != LLMProviderDeepSeek {
		t.Errorf("provider = %q, want default %q", got.Provider, LLMProviderDeepSeek)
	}
}

func TestLoadVerifyPathIgnoresRetiredKnowledgeFallback(t *testing.T) {
	// The knowledge fallback is retired: a no-evidence claim is always
	// unverifiable, so the env var is no longer read and even a garbage value
	// cannot fail the load.
	t.Setenv("FACTCHECK_KNOWLEDGE_FALLBACK", "not-a-bool")
	if _, err := LoadVerifyPath(); err != nil {
		t.Fatalf("LoadVerifyPath with retired env set: %v", err)
	}
}

func TestLoadVerifyPathReadsGeminiSelection(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "g-test")
	got, err := LoadVerifyPath()
	if err != nil {
		t.Fatalf("LoadVerifyPath: %v", err)
	}
	if got.Provider != LLMProviderGemini {
		t.Errorf("provider = %q, want %q", got.Provider, LLMProviderGemini)
	}
	if got.GeminiAPIKey != "g-test" {
		t.Errorf("gemini key = %q, want g-test", got.GeminiAPIKey)
	}
}

func TestLoadVerifyPathRejectsUnknownProvider(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "mistral")
	if _, err := LoadVerifyPath(); err == nil {
		t.Fatal("expected an error for an unknown LLM_PROVIDER")
	}
}

func TestVerifyPathActive(t *testing.T) {
	tests := []struct {
		name string
		cfg  VerifyPath
		want bool
	}{
		{"anthropic enabled with anthropic key", VerifyPath{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: "k"}, true},
		{"anthropic enabled without key degrades to off", VerifyPath{LLMSelection: LLMSelection{Provider: LLMProviderAnthropic}, Enabled: true, APIKey: ""}, false},
		{"gemini with gemini key and no anthropic key stays active", VerifyPath{LLMSelection: LLMSelection{Provider: LLMProviderGemini, GeminiAPIKey: "g"}, Enabled: true, APIKey: ""}, true},
		{"gemini without gemini key degrades to off", VerifyPath{LLMSelection: LLMSelection{Provider: LLMProviderGemini}, Enabled: true, APIKey: "k"}, false},
		{"deepseek with deepseek key and no anthropic key stays active", VerifyPath{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek, DeepSeekAPIKey: "d"}, Enabled: true, APIKey: ""}, true},
		{"deepseek without deepseek key degrades to off", VerifyPath{LLMSelection: LLMSelection{Provider: LLMProviderDeepSeek}, Enabled: true, APIKey: "k"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Active(); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadVerifyPathActiveAndOverrides(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("FACTCHECK_VERIFY_PATH", "true")
	t.Setenv("FACTCHECK_VERIFY_API_KEY", "sk-test")
	t.Setenv("FACTCHECK_VERIFY_CONCURRENCY", "3")
	t.Setenv("FACTCHECK_VERIFY_QUEUE_DEPTH", "8")
	t.Setenv("FACTCHECK_VERIFY_FAST_TAU", "0.9")
	t.Setenv("FACTCHECK_VERIFY_CACHE_TTL", "45s")
	t.Setenv("FACTCHECK_VERIFY_CACHE_THRESHOLD", "0.88")
	t.Setenv("FACTCHECK_VERIFY_CACHE_MAX_ENTRIES", "256")
	t.Setenv("FACTCHECK_VERIFY_RETRIEVAL_THRESHOLD", "0.5")
	got, err := LoadVerifyPath()
	if err != nil {
		t.Fatalf("LoadVerifyPath: %v", err)
	}
	if !got.Active() {
		t.Fatal("enabled with a key must be Active")
	}
	if got.Concurrency != 3 || got.QueueDepth != 8 || got.FastTau != 0.9 || got.CacheTTL != 45*time.Second ||
		got.RetrievalThreshold != 0.5 || got.CacheThreshold != 0.88 || got.CacheMaxEntries != 256 {
		t.Errorf("overrides wrong: %+v", got)
	}
}

func TestLoadVerifyPathRejectsBadValues(t *testing.T) {
	tests := map[string]string{
		"FACTCHECK_VERIFY_CONCURRENCY":         "0",
		"FACTCHECK_VERIFY_FAST_TAU":            "1.5",
		"FACTCHECK_VERIFY_QUEUE_DEPTH":         "-1",
		"FACTCHECK_VERIFY_CACHE_TTL":           "-1s",
		"FACTCHECK_VERIFY_DEADLINE":            "0s",
		"FACTCHECK_VERIFY_FAST_DEADLINE":       "0s",
		"FACTCHECK_VERIFY_RETRIEVAL_THRESHOLD": "1.5",
		"FACTCHECK_VERIFY_CACHE_THRESHOLD":     "1.5",
		"FACTCHECK_VERIFY_CACHE_MAX_ENTRIES":   "0",
	}
	for key, val := range tests {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, val)
			if _, err := LoadVerifyPath(); err == nil {
				t.Fatalf("LoadVerifyPath with %s=%s = nil error, want error", key, val)
			}
		})
	}
}

func TestLoadSecondPassDefaultsOff(t *testing.T) {
	t.Parallel()
	got, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	if got.Enabled {
		t.Error("second pass must default off")
	}
	if got.Active() {
		t.Error("second pass with no key must not be Active")
	}
	if got.BandLo != defaultSecondPassBandLo || got.BandHi != defaultSecondPassBandHi || got.Deadline != defaultSecondPassDeadline {
		t.Errorf("defaults wrong: %+v", got)
	}
	if got.Model != defaultSecondPassModel {
		t.Errorf("model = %q, want default %q under deepseek", got.Model, defaultSecondPassModel)
	}
}

func TestLoadSecondPassActiveAndOverrides(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "anthropic")
	t.Setenv("FACTCHECK_SECOND_PASS", "true")
	t.Setenv("FACTCHECK_SECOND_PASS_API_KEY", "sk-reason")
	t.Setenv("FACTCHECK_SECOND_PASS_MODEL", "claude-opus-4-5")
	t.Setenv("FACTCHECK_SECOND_PASS_BAND_LO", "0.5")
	t.Setenv("FACTCHECK_SECOND_PASS_BAND_HI", "0.75")
	t.Setenv("FACTCHECK_SECOND_PASS_DEADLINE", "20s")
	got, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	if !got.Active() {
		t.Fatal("enabled with a key must be Active")
	}
	if got.Model != "claude-opus-4-5" {
		t.Errorf("model override = %q, want claude-opus-4-5", got.Model)
	}
	if got.BandLo != 0.5 || got.BandHi != 0.75 || got.Deadline != 20*time.Second {
		t.Errorf("overrides wrong: %+v", got)
	}
}

func TestLoadSecondPassDeepSeekKeyStaysActive(t *testing.T) {
	t.Setenv("FACTCHECK_SECOND_PASS", "true")
	t.Setenv("DEEPSEEK_API_KEY", "d-test")
	got, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	if !got.Active() {
		t.Fatal("second pass enabled with a deepseek key under the default provider must be Active")
	}
}

func TestLoadSecondPassRejectsBadValues(t *testing.T) {
	tests := map[string]string{
		"FACTCHECK_SECOND_PASS_BAND_LO":  "1.5",
		"FACTCHECK_SECOND_PASS_BAND_HI":  "-0.1",
		"FACTCHECK_SECOND_PASS_DEADLINE": "0s",
	}
	for key, val := range tests {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, val)
			if _, err := LoadSecondPass(); err == nil {
				t.Fatalf("LoadSecondPass with %s=%s = nil error, want error", key, val)
			}
		})
	}
}

func TestLoadSecondPassRejectsInvertedBand(t *testing.T) {
	t.Setenv("FACTCHECK_SECOND_PASS_BAND_LO", "0.8")
	t.Setenv("FACTCHECK_SECOND_PASS_BAND_HI", "0.4")
	if _, err := LoadSecondPass(); err == nil {
		t.Fatal("expected an error for an inverted second-pass band")
	}
}

// TestLoadFinalGateFallsBackToSecondPass proves the terminal gate inherits every
// unset knob from the already-loaded second pass: its enable flag, provider, key,
// model, and deadline, plus a trigger floor at the second pass's upper band and the
// default min-confidence.
func TestLoadFinalGateFallsBackToSecondPass(t *testing.T) {
	t.Setenv("FACTCHECK_SECOND_PASS", "true")
	t.Setenv("DEEPSEEK_API_KEY", "d-test")
	t.Setenv("FACTCHECK_SECOND_PASS_BAND_HI", "0.75")
	t.Setenv("FACTCHECK_SECOND_PASS_DEADLINE", "20s")
	sp, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	got, err := LoadFinalGate(sp)
	if err != nil {
		t.Fatalf("LoadFinalGate: %v", err)
	}
	if !got.Enabled || !got.Active() {
		t.Fatalf("gate must inherit the enabled+keyed second pass: %+v", got)
	}
	if got.Provider != LLMProviderDeepSeek {
		t.Errorf("provider = %q, want the second pass's deepseek", got.Provider)
	}
	if got.Model != defaultSecondPassModel {
		t.Errorf("model = %q, want the second pass's %q", got.Model, defaultSecondPassModel)
	}
	if got.TriggerBelow != 0.75 {
		t.Errorf("trigger floor = %v, want the second pass upper band 0.75", got.TriggerBelow)
	}
	if got.MinConfidence != defaultFinalGateMinConfidence {
		t.Errorf("min confidence = %v, want default %v", got.MinConfidence, defaultFinalGateMinConfidence)
	}
	if got.Deadline != 20*time.Second {
		t.Errorf("deadline = %v, want the second pass's 20s", got.Deadline)
	}
}

// TestLoadFinalGateOverrides proves each FACTCHECK_FINAL_GATE_* knob overrides its
// second-pass fallback, and the gate's own enable flag can turn it on independently.
func TestLoadFinalGateOverrides(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "d-test")
	t.Setenv("FACTCHECK_FINAL_GATE", "true")
	t.Setenv("FACTCHECK_FINAL_GATE_MODEL", "deepseek-v4-pro-max")
	t.Setenv("FACTCHECK_FINAL_GATE_TRIGGER_BELOW", "0.6")
	t.Setenv("FACTCHECK_FINAL_GATE_MIN_CONFIDENCE", "0.95")
	t.Setenv("FACTCHECK_FINAL_GATE_DEADLINE", "30s")
	sp, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	got, err := LoadFinalGate(sp)
	if err != nil {
		t.Fatalf("LoadFinalGate: %v", err)
	}
	if !got.Active() {
		t.Fatal("FACTCHECK_FINAL_GATE=true with a deepseek key must be Active")
	}
	if got.Model != "deepseek-v4-pro-max" {
		t.Errorf("model = %q, want the gate override", got.Model)
	}
	if got.TriggerBelow != 0.6 || got.MinConfidence != 0.95 || got.Deadline != 30*time.Second {
		t.Errorf("overrides wrong: %+v", got)
	}
}

// TestLoadFinalGateDecouplesProvider proves the gate can run on a different provider
// than the shared LLM_PROVIDER, and that switching providers drops the carried
// second-pass model so a stale model id can never reach a provider that does not know
// it (the operator must then name the gate's own model).
func TestLoadFinalGateDecouplesProvider(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "deepseek")
	t.Setenv("FACTCHECK_SECOND_PASS", "true")
	t.Setenv("DEEPSEEK_API_KEY", "d-test")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("FACTCHECK_FINAL_GATE_PROVIDER", "anthropic")
	t.Setenv("FACTCHECK_FINAL_GATE_API_KEY", "sk-gate")
	t.Setenv("FACTCHECK_FINAL_GATE_MODEL", "claude-opus-4-5")
	sp, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	got, err := LoadFinalGate(sp)
	if err != nil {
		t.Fatalf("LoadFinalGate: %v", err)
	}
	if got.Provider != LLMProviderAnthropic {
		t.Errorf("provider = %q, want the decoupled anthropic override", got.Provider)
	}
	if !got.Active() {
		t.Fatal("gate under anthropic with its own key must be Active")
	}
	if got.Model != "claude-opus-4-5" {
		t.Errorf("model = %q, want the gate override", got.Model)
	}
}

// TestLoadFinalGateRejectsBadThresholds proves an out-of-range threshold or a
// non-positive deadline fails fast (validated eagerly, like the second-pass band).
func TestLoadFinalGateRejectsBadThresholds(t *testing.T) {
	tests := map[string]string{
		"FACTCHECK_FINAL_GATE_TRIGGER_BELOW":  "1.5",
		"FACTCHECK_FINAL_GATE_MIN_CONFIDENCE": "-0.1",
		"FACTCHECK_FINAL_GATE_DEADLINE":       "0s",
	}
	for key, val := range tests {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, val)
			sp, err := LoadSecondPass()
			if err != nil {
				t.Fatalf("LoadSecondPass: %v", err)
			}
			if _, err := LoadFinalGate(sp); err == nil {
				t.Fatalf("LoadFinalGate with %s=%s = nil error, want error", key, val)
			}
		})
	}
}

// TestLoadFinalGateActiveRejectsUnknownProvider proves an ACTIVE gate (enabled and
// keyed) with an unsupported provider fails fast rather than silently proceeding.
func TestLoadFinalGateActiveRejectsUnknownProvider(t *testing.T) {
	t.Setenv("FACTCHECK_FINAL_GATE", "true")
	t.Setenv("FACTCHECK_FINAL_GATE_PROVIDER", "openai")
	// An unknown provider resolves its key via the Anthropic slot, so this key makes
	// the gate Active and the provider guard fire.
	t.Setenv("FACTCHECK_FINAL_GATE_API_KEY", "sk-gate")
	sp, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	if _, err := LoadFinalGate(sp); err == nil {
		t.Fatal("LoadFinalGate must reject an unknown provider on an active gate")
	}
}

// TestLoadFinalGateBadProviderDegradesOffWhenInactive proves a bad provider override
// never bricks boot when the gate will not run - whether the gate is disabled or
// enabled-but-keyless (Active()==false either way), matching the keyless-degrades-off
// contract every optional LLM stage keeps.
func TestLoadFinalGateBadProviderDegradesOffWhenInactive(t *testing.T) {
	cases := map[string]map[string]string{
		"disabled":            {"FACTCHECK_FINAL_GATE_PROVIDER": "openai"},
		"enabled but keyless": {"FACTCHECK_FINAL_GATE": "true", "FACTCHECK_FINAL_GATE_PROVIDER": "openai"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			sp, err := LoadSecondPass()
			if err != nil {
				t.Fatalf("LoadSecondPass: %v", err)
			}
			got, err := LoadFinalGate(sp)
			if err != nil {
				t.Fatalf("LoadFinalGate must not error on a bad provider for an inactive gate: %v", err)
			}
			if got.Active() {
				t.Fatal("gate must be inactive")
			}
		})
	}
}

// TestLoadFinalGateActiveRequiresModel proves an active gate under a provider with no
// default reasoning model (Anthropic here) fails fast instead of silently running the
// cheap default stage model.
func TestLoadFinalGateActiveRequiresModel(t *testing.T) {
	t.Setenv("FACTCHECK_FINAL_GATE", "true")
	t.Setenv("FACTCHECK_FINAL_GATE_PROVIDER", "anthropic")
	t.Setenv("FACTCHECK_FINAL_GATE_API_KEY", "sk-gate")
	// No FACTCHECK_FINAL_GATE_MODEL: anthropic has no reasoning default here.
	sp, err := LoadSecondPass()
	if err != nil {
		t.Fatalf("LoadSecondPass: %v", err)
	}
	if _, err := LoadFinalGate(sp); err == nil {
		t.Fatal("LoadFinalGate must require an explicit model for an active anthropic gate")
	}
}

func TestLoadPolitical(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		wantEnabled    bool
		wantLocale     domain.Locale
		wantMinResults int
		wantCuratedTau float64
		wantErr        bool
	}{
		{
			name:           "disabled by default keeps english locale",
			env:            map[string]string{},
			wantEnabled:    false,
			wantLocale:     domain.LocaleEnglish,
			wantMinResults: 1,
			wantCuratedTau: defaultPoliticalCuratedTau,
		},
		{
			name:           "enabled selects french locale",
			env:            map[string]string{"FACTCHECK_POLITICAL": "true"},
			wantEnabled:    true,
			wantLocale:     domain.LocaleFrench,
			wantMinResults: 1,
			wantCuratedTau: defaultPoliticalCuratedTau,
		},
		{
			name:           "explicit false keeps english locale",
			env:            map[string]string{"FACTCHECK_POLITICAL": "false"},
			wantEnabled:    false,
			wantLocale:     domain.LocaleEnglish,
			wantMinResults: 1,
			wantCuratedTau: defaultPoliticalCuratedTau,
		},
		{
			name:           "router min results override",
			env:            map[string]string{"FACTCHECK_POLITICAL": "true", "FACTCHECK_POLITICAL_ROUTER_MIN_RESULTS": "3"},
			wantEnabled:    true,
			wantLocale:     domain.LocaleFrench,
			wantMinResults: 3,
			wantCuratedTau: defaultPoliticalCuratedTau,
		},
		{
			name:           "curated tau override",
			env:            map[string]string{"FACTCHECK_POLITICAL": "true", "FACTCHECK_POLITICAL_CURATED_TAU": "0.7"},
			wantEnabled:    true,
			wantLocale:     domain.LocaleFrench,
			wantMinResults: 1,
			wantCuratedTau: 0.7,
		},
		{
			name:    "non-boolean fails",
			env:     map[string]string{"FACTCHECK_POLITICAL": "maybe"},
			wantErr: true,
		},
		{
			name:    "non-positive router min results fails",
			env:     map[string]string{"FACTCHECK_POLITICAL": "true", "FACTCHECK_POLITICAL_ROUTER_MIN_RESULTS": "0"},
			wantErr: true,
		},
		{
			name:    "out-of-range curated tau fails",
			env:     map[string]string{"FACTCHECK_POLITICAL": "true", "FACTCHECK_POLITICAL_CURATED_TAU": "1.5"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadPolitical()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Enabled != tc.wantEnabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tc.wantEnabled)
			}
			if got.Locale() != tc.wantLocale {
				t.Errorf("Locale() = %q, want %q", got.Locale(), tc.wantLocale)
			}
			if got.RouterMinResults != tc.wantMinResults {
				t.Errorf("RouterMinResults = %d, want %d", got.RouterMinResults, tc.wantMinResults)
			}
			if got.CuratedTau != tc.wantCuratedTau {
				t.Errorf("CuratedTau = %v, want %v", got.CuratedTau, tc.wantCuratedTau)
			}
		})
	}
}

// TestPoliticalActive asserts the political verify path activates only when the
// flag is on and the verify path it layers onto is itself active, so it degrades
// gracefully rather than failing to start when the verify path is off.
func TestPoliticalActive(t *testing.T) {
	tests := []struct {
		name         string
		enabled      bool
		verifyActive bool
		want         bool
	}{
		{"both on activates", true, true, true},
		{"flag off stays inactive", false, true, false},
		{"verify path off stays inactive", true, false, false},
		{"both off stays inactive", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Political{Enabled: tc.enabled}
			if got := p.Active(tc.verifyActive); got != tc.want {
				t.Errorf("Active(%v) = %v, want %v", tc.verifyActive, got, tc.want)
			}
		})
	}
}

// TestPoliticalRouterLang asserts the routed source query language tracks the
// political locale, so the source packs and the live stages share one language.
func TestPoliticalRouterLang(t *testing.T) {
	if got := (Political{Enabled: true}).RouterLang(); got != domain.LocaleFrench.LanguageCode() {
		t.Errorf("RouterLang() on = %q, want %q", got, domain.LocaleFrench.LanguageCode())
	}
	if got := (Political{Enabled: false}).RouterLang(); got != domain.LocaleEnglish.LanguageCode() {
		t.Errorf("RouterLang() off = %q, want %q", got, domain.LocaleEnglish.LanguageCode())
	}
}

func TestLoadFactCheckQueueDefaultName(t *testing.T) {
	t.Setenv("RABBITMQ_URL", "amqp://localhost")
	q, err := LoadFactCheckQueue()
	if err != nil {
		t.Fatalf("LoadFactCheckQueue: %v", err)
	}
	if q.VersionedName() != "factcheck.claims.v2" {
		t.Errorf("VersionedName = %q, want factcheck.claims.v1", q.VersionedName())
	}
}

func TestLoadFactCheckArchive(t *testing.T) {
	t.Run("defaults to the broadened topic set and outlet allowlist", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		got, err := LoadFactCheckArchive()
		if err != nil {
			t.Fatalf("LoadFactCheckArchive: %v", err)
		}
		// Materially more than the fixed ~19-topic legacy set, plus publisher streams.
		if len(got.Topics) <= 19 {
			t.Errorf("default topics = %d, want materially more than 19", len(got.Topics))
		}
		if len(got.PublisherSites) == 0 {
			t.Errorf("default publisher sites empty, want the outlet allowlist")
		}
		if got.Language != "fr" {
			t.Errorf("language = %q, want fr", got.Language)
		}
		if got.CheckpointPath == "" {
			t.Errorf("checkpoint path empty, want a default")
		}
	})
	t.Run("overrides", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		t.Setenv("FACTCHECK_QUERIES", "chômage, retraites")
		t.Setenv("FACTCHECK_PUBLISHER_SITES", "lemonde.fr")
		t.Setenv("FACTCHECK_LANGUAGE", "en")
		t.Setenv("FACTCHECK_MAX_PAGES", "3")
		t.Setenv("FACTCHECK_MAX_AGE_DAYS", "30")
		t.Setenv("FACTCHECK_CHECKPOINT_PATH", "/tmp/cp.json")
		got, err := LoadFactCheckArchive()
		if err != nil {
			t.Fatalf("LoadFactCheckArchive: %v", err)
		}
		if len(got.Topics) != 2 || got.Topics[0] != "chômage" || got.Topics[1] != "retraites" {
			t.Errorf("topics = %v", got.Topics)
		}
		if len(got.PublisherSites) != 1 || got.PublisherSites[0] != "lemonde.fr" {
			t.Errorf("publisher sites = %v", got.PublisherSites)
		}
		if got.Language != "en" || got.MaxPages != 3 || got.MaxAgeDays != 30 || got.CheckpointPath != "/tmp/cp.json" {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
	t.Run("empty-but-present publisher sites falls back to default (compose ships them empty)", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		t.Setenv("FACTCHECK_PUBLISHER_SITES", "")
		got, err := LoadFactCheckArchive()
		if err != nil {
			t.Fatalf("LoadFactCheckArchive: %v", err)
		}
		if len(got.PublisherSites) == 0 {
			t.Errorf("empty-but-present must keep the default outlet allowlist, got %v", got.PublisherSites)
		}
	})
	t.Run("sentinel none disables publisher streams", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		t.Setenv("FACTCHECK_PUBLISHER_SITES", "none")
		got, err := LoadFactCheckArchive()
		if err != nil {
			t.Fatalf("LoadFactCheckArchive: %v", err)
		}
		if len(got.PublisherSites) != 0 {
			t.Errorf("sentinel 'none' should disable publisher streams, got %v", got.PublisherSites)
		}
	})
	t.Run("missing key", func(t *testing.T) {
		if _, err := LoadFactCheckArchive(); err == nil {
			t.Fatal("expected an error for missing FACTCHECK_API_KEY")
		}
	})
	t.Run("empty-but-present queries falls back to default rotation (no crash-loop)", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		t.Setenv("FACTCHECK_QUERIES", "")
		got, err := LoadFactCheckArchive()
		if err != nil {
			t.Fatalf("empty FACTCHECK_QUERIES must not error: %v", err)
		}
		if len(got.Topics) <= 19 {
			t.Errorf("empty queries should fall back to the broadened default, got %d", len(got.Topics))
		}
	})
	t.Run("all-blank queries override also falls back", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		t.Setenv("FACTCHECK_QUERIES", " , , ")
		got, err := LoadFactCheckArchive()
		if err != nil {
			t.Fatalf("LoadFactCheckArchive: %v", err)
		}
		if len(got.Topics) <= 19 {
			t.Errorf("all-blank override should fall back to default, got %d", len(got.Topics))
		}
	})
	t.Run("bad max pages", func(t *testing.T) {
		t.Setenv("FACTCHECK_API_KEY", "k")
		t.Setenv("FACTCHECK_MAX_PAGES", "-1")
		if _, err := LoadFactCheckArchive(); err == nil {
			t.Fatal("expected an error for negative FACTCHECK_MAX_PAGES")
		}
	})
}

func TestLoadDataCommonsArchive(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		got, err := LoadDataCommonsArchive()
		if err != nil {
			t.Fatalf("LoadDataCommonsArchive: %v", err)
		}
		if got.FeedURL != defaultDataCommonsFeedURL {
			t.Errorf("feed url = %q, want default", got.FeedURL)
		}
		if len(got.OutletAllowlist) != len(defaultDataCommonsOutlets) {
			t.Errorf("allowlist = %v, want the default French set", got.OutletAllowlist)
		}
		if got.MaxItems != 0 {
			t.Errorf("max items = %d, want 0", got.MaxItems)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		t.Setenv("DATACOMMONS_FEED_URL", "https://example.test/feed.json")
		t.Setenv("DATACOMMONS_OUTLET_ALLOWLIST", "afp.com, lemonde.fr")
		t.Setenv("DATACOMMONS_MAX_ITEMS", "5")
		got, err := LoadDataCommonsArchive()
		if err != nil {
			t.Fatalf("LoadDataCommonsArchive: %v", err)
		}
		if got.FeedURL != "https://example.test/feed.json" {
			t.Errorf("feed url = %q", got.FeedURL)
		}
		if len(got.OutletAllowlist) != 2 || got.OutletAllowlist[0] != "afp.com" || got.OutletAllowlist[1] != "lemonde.fr" {
			t.Errorf("allowlist = %v, want [afp.com lemonde.fr]", got.OutletAllowlist)
		}
		if got.MaxItems != 5 {
			t.Errorf("max items = %d, want 5", got.MaxItems)
		}
	})
	t.Run("empty-but-present allowlist keeps the French default (never worldwide)", func(t *testing.T) {
		t.Setenv("DATACOMMONS_OUTLET_ALLOWLIST", "")
		got, err := LoadDataCommonsArchive()
		if err != nil {
			t.Fatalf("LoadDataCommonsArchive: %v", err)
		}
		if len(got.OutletAllowlist) != len(defaultDataCommonsOutlets) {
			t.Errorf("empty-but-present must keep the vetted French default, got %v", got.OutletAllowlist)
		}
	})
	t.Run("sentinel * ingests every outlet", func(t *testing.T) {
		t.Setenv("DATACOMMONS_OUTLET_ALLOWLIST", "*")
		got, err := LoadDataCommonsArchive()
		if err != nil {
			t.Fatalf("LoadDataCommonsArchive: %v", err)
		}
		if len(got.OutletAllowlist) != 0 {
			t.Errorf("sentinel '*' should ingest all (empty allowlist), got %v", got.OutletAllowlist)
		}
	})
	t.Run("bad max items", func(t *testing.T) {
		t.Setenv("DATACOMMONS_MAX_ITEMS", "-1")
		if _, err := LoadDataCommonsArchive(); err == nil {
			t.Fatal("expected an error for negative max items")
		}
	})
}

func TestLoadClaimReviewSites(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		got, err := LoadClaimReviewSites()
		if err != nil {
			t.Fatalf("LoadClaimReviewSites: %v", err)
		}
		if len(got.Outlets) < 3 {
			t.Errorf("outlets = %d, want at least 3 allowlisted French outlets", len(got.Outlets))
		}
		for _, o := range got.Outlets {
			if o.Host == "" || o.Sitemap == "" {
				t.Errorf("outlet missing host/sitemap: %+v", o)
			}
		}
		if got.UserAgent == "" || got.MinDelay <= 0 || got.MaxURLsPerOutlet <= 0 {
			t.Errorf("defaults not set: %+v", got)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		t.Setenv("CLAIMREVIEW_USER_AGENT", "my-bot")
		t.Setenv("CLAIMREVIEW_MIN_DELAY_MS", "500")
		t.Setenv("CLAIMREVIEW_MAX_URLS", "10")
		got, err := LoadClaimReviewSites()
		if err != nil {
			t.Fatalf("LoadClaimReviewSites: %v", err)
		}
		if got.UserAgent != "my-bot" || got.MinDelay != 500*time.Millisecond || got.MaxURLsPerOutlet != 10 {
			t.Errorf("overrides not applied: %+v", got)
		}
	})
}

func TestLoadClaimsKGSeed(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		got, err := LoadClaimsKGSeed()
		if err != nil {
			t.Fatalf("LoadClaimsKGSeed: %v", err)
		}
		if got.Enabled {
			t.Error("seed enabled by default, want disabled")
		}
		if got.Vintage != "2023" {
			t.Errorf("vintage = %q, want 2023", got.Vintage)
		}
	})
	t.Run("armed", func(t *testing.T) {
		t.Setenv("CLAIMSKG_SEED_ENABLED", "true")
		t.Setenv("CLAIMSKG_SEED_FILE", "/data/claimskg.csv")
		t.Setenv("CLAIMSKG_SEED_TSV", "true")
		got, err := LoadClaimsKGSeed()
		if err != nil {
			t.Fatalf("LoadClaimsKGSeed: %v", err)
		}
		if !got.Enabled || got.SeedFile != "/data/claimskg.csv" || !got.TSV {
			t.Errorf("armed seed config wrong: %+v", got)
		}
	})
}

func TestLoadDataCommonsArchiveFormat(t *testing.T) {
	t.Run("default datafeed", func(t *testing.T) {
		got, err := LoadDataCommonsArchive()
		if err != nil {
			t.Fatalf("LoadDataCommonsArchive: %v", err)
		}
		if got.Format != "datafeed" {
			t.Errorf("format = %q, want datafeed", got.Format)
		}
	})
	t.Run("ndjson override", func(t *testing.T) {
		t.Setenv("DATACOMMONS_FEED_FORMAT", "ndjson")
		got, err := LoadDataCommonsArchive()
		if err != nil || got.Format != "ndjson" {
			t.Fatalf("format = %q err=%v, want ndjson", got.Format, err)
		}
	})
	t.Run("bad format", func(t *testing.T) {
		t.Setenv("DATACOMMONS_FEED_FORMAT", "xml")
		if _, err := LoadDataCommonsArchive(); err == nil {
			t.Fatal("expected an error for an unknown feed format")
		}
	})
}

func TestLoadCrawlAlerts(t *testing.T) {
	tests := []struct {
		name       string
		webhook    string
		wantURL    string
		wantActive bool
	}{
		{name: "unset is inactive noop", webhook: "", wantURL: "", wantActive: false},
		{
			name:       "set is active",
			webhook:    "https://hooks.slack.com/services/T/B/X",
			wantURL:    "https://hooks.slack.com/services/T/B/X",
			wantActive: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SLACK_WEBHOOK_URL", tc.webhook)
			got := LoadCrawlAlerts()
			if got.WebhookURL != tc.wantURL {
				t.Errorf("WebhookURL = %q, want %q", got.WebhookURL, tc.wantURL)
			}
			if got.Active() != tc.wantActive {
				t.Errorf("Active() = %v, want %v", got.Active(), tc.wantActive)
			}
		})
	}
}
