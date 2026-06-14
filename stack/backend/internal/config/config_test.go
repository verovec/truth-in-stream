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
			},
			want: Match{TopK: 10, ScoreThreshold: 0.75, EvidenceTopK: 3, EvidenceThreshold: 0.8, MaxResults: 6, EmbedConcurrency: 2, Timeout: 30 * time.Second, ConfidenceClusterSize: 3, ConfidenceLeadWeight: 0.9, ConfidenceBodyWeight: 0.4},
		},
		{
			name: "negative threshold accepted",
			env:  map[string]string{"MATCH_SCORE_THRESHOLD": "-1"},
			want: Match{TopK: 5, ScoreThreshold: -1, EvidenceTopK: 5, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0.6},
		},
		{
			name: "evidence retrieval can be disabled",
			env:  map[string]string{"MATCH_EVIDENCE_TOP_K": "0"},
			want: Match{TopK: 5, ScoreThreshold: 0.5, EvidenceTopK: 0, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0.6},
		},
		{
			name: "zero body weight accepted disables body evidence",
			env:  map[string]string{"MATCH_CONFIDENCE_BODY_WEIGHT": "0"},
			want: Match{TopK: 5, ScoreThreshold: 0.5, EvidenceTopK: 5, EvidenceThreshold: 0.6, MaxResults: 5, EmbedConcurrency: 4, Timeout: 10 * time.Second, ConfidenceClusterSize: 5, ConfidenceLeadWeight: 1, ConfidenceBodyWeight: 0},
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

func TestLoadPrecheck(t *testing.T) {
	defaults := Precheck{Enabled: true, MinWords: 4, CoverageThreshold: 0.4, WikiCoverageEnabled: true, WikiCoverageThreshold: 0.46}
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
			},
			want: Precheck{Enabled: false, MinWords: 6, CoverageThreshold: 0.6, WikiCoverageEnabled: false, WikiCoverageThreshold: 0.5},
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
			want: WikiEmbed{BatchSize: 128, Concurrency: 4, MaxRetries: 6, RequestsPerMinute: 0, HTTPTimeout: 30 * time.Second, MaintenanceWorkMem: "512MB", MaxParallelWorkers: 7},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"WIKI_EMBED_BATCH_SIZE":           "256",
				"WIKI_EMBED_CONCURRENCY":          "8",
				"WIKI_EMBED_MAX_RETRIES":          "3",
				"WIKI_EMBED_RPM":                  "120",
				"WIKI_EMBED_HTTP_TIMEOUT":         "90s",
				"WIKI_EMBED_MAINTENANCE_WORK_MEM": "2GB",
				"WIKI_EMBED_MAX_PARALLEL_WORKERS": "4",
			},
			want: WikiEmbed{BatchSize: 256, Concurrency: 8, MaxRetries: 3, RequestsPerMinute: 120, HTTPTimeout: 90 * time.Second, MaintenanceWorkMem: "2GB", MaxParallelWorkers: 4},
		},
		{name: "negative rpm rejected", env: map[string]string{"WIKI_EMBED_RPM": "-1"}, wantErr: true},
		{name: "http timeout zero rejected", env: map[string]string{"WIKI_EMBED_HTTP_TIMEOUT": "0"}, wantErr: true},
		{name: "http timeout malformed rejected", env: map[string]string{"WIKI_EMBED_HTTP_TIMEOUT": "soon"}, wantErr: true},
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
			want: Queue{URL: "amqp://guest:guest@localhost:5672/", Name: "embedding.jobs", MaxPriority: 10, Prefetch: 1, Version: "1", KnownVersions: []string{"1"}},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"RABBITMQ_URL":          "amqps://user:pass@broker:5671/",
				"RABBITMQ_QUEUE":        "embedding.priority",
				"RABBITMQ_MAX_PRIORITY": "255",
				"RABBITMQ_PREFETCH":     "16",
			},
			want: Queue{URL: "amqps://user:pass@broker:5671/", Name: "embedding.priority", MaxPriority: 255, Prefetch: 16, Version: "1", KnownVersions: []string{"1"}},
		},
		{
			name: "version list takes the newest as active",
			env: map[string]string{
				"RABBITMQ_URL":            "amqp://localhost",
				"RABBITMQ_QUEUE_VERSIONS": "1, 2, 20260612",
			},
			want: Queue{URL: "amqp://localhost", Name: "embedding.jobs", MaxPriority: 10, Prefetch: 1, Version: "20260612", KnownVersions: []string{"1", "2", "20260612"}},
		},
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
	defaults := EmbedWorker{Concurrency: 4, MaxAttempts: 5, HTTPTimeout: 30 * time.Second, RequestsPerMinute: 0, EmbedMaxRetries: 6}
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
				"EMBED_WORKER_MAX_ATTEMPTS":      "3",
				"EMBED_WORKER_HTTP_TIMEOUT":      "45s",
				"EMBED_WORKER_RPM":               "120",
				"EMBED_WORKER_EMBED_MAX_RETRIES": "2",
			},
			want: EmbedWorker{Concurrency: 8, MaxAttempts: 3, HTTPTimeout: 45 * time.Second, RequestsPerMinute: 120, EmbedMaxRetries: 2},
		},
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
	defaults := Live{Concurrency: 4, QueueDepth: 32}
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
			env:  map[string]string{"LIVE_CONCURRENCY": "8", "LIVE_QUEUE_DEPTH": "64"},
			want: Live{Concurrency: 8, QueueDepth: 64},
		},
		{name: "zero concurrency rejected", env: map[string]string{"LIVE_CONCURRENCY": "0"}, wantErr: true},
		{name: "negative concurrency rejected", env: map[string]string{"LIVE_CONCURRENCY": "-1"}, wantErr: true},
		{name: "non-numeric concurrency rejected", env: map[string]string{"LIVE_CONCURRENCY": "lots"}, wantErr: true},
		{name: "zero queue depth rejected", env: map[string]string{"LIVE_QUEUE_DEPTH": "0"}, wantErr: true},
		{name: "negative queue depth rejected", env: map[string]string{"LIVE_QUEUE_DEPTH": "-4"}, wantErr: true},
		{name: "non-numeric queue depth rejected", env: map[string]string{"LIVE_QUEUE_DEPTH": "deep"}, wantErr: true},
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
	defaults := Consistency{Enabled: false, APIKey: "", Model: "claude-haiku-4-5-20251001", TopK: 3, SimilarityFloor: 0.6}
	tests := []struct {
		name    string
		env     map[string]string
		want    Consistency
		wantErr bool
	}{
		{
			name: "off by default",
			env:  map[string]string{},
			want: defaults,
		},
		{
			name: "enabled with key and overrides",
			env: map[string]string{
				"CONSISTENCY_ENABLED":          "true",
				"CONSISTENCY_API_KEY":          "sk-test",
				"CONSISTENCY_MODEL":            "claude-haiku-4-5",
				"CONSISTENCY_TOP_K":            "5",
				"CONSISTENCY_SIMILARITY_FLOOR": "0.72",
			},
			want: Consistency{Enabled: true, APIKey: "sk-test", Model: "claude-haiku-4-5", TopK: 5, SimilarityFloor: 0.72},
		},
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
		{"enabled with key", Consistency{Enabled: true, APIKey: "k"}, true},
		{"enabled without key degrades to off", Consistency{Enabled: true, APIKey: ""}, false},
		{"disabled with key stays off", Consistency{Enabled: false, APIKey: "k"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Active(); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}
