// Package config loads service configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/queue"
	"github.com/verovec/truth-in-stream/backend/internal/rerank"
)

// DefaultEmbeddingModel is the voyage model used for both ingest and query
// embeddings. The same model must be used on both sides or similarity scores
// are meaningless. voyage-4-large is the canonical model: it outputs 1024 dims
// (matching the pinned index) and its batch endpoint works, where base
// voyage-4's currently hangs on any multi-input batch. It is exported so the
// seed tooling and its cache-coverage test key the offline cache under the same
// string the running stack embeds with - a drift here resurfaces as a silent
// cache miss (the failure this card fixed).
const DefaultEmbeddingModel = "voyage-4-large"

// defaultTranscriptionModel is the AssemblyAI Universal-3 Pro streaming model.
// AssemblyAI is the sole speech-to-text provider: live streams and imported
// videos alike are transcribed over its realtime diarizing WebSocket.
const defaultTranscriptionModel = "u3-rt-pro"

// defaultDemoMediaDir is where the server reads and serves the bundled demo
// clip from, relative to the working directory.
const defaultDemoMediaDir = "demo"

// LLM provider selection. Every LLM-backed stage (stance, check-worthiness,
// claim decomposition, claim typing, the credibility verifier, the ingestion
// fact-checkability gate) reads one shared provider choice from LLM_PROVIDER and,
// when that choice is Gemini, the key from GEMINI_API_KEY, or when DeepSeek, the
// key from DEEPSEEK_API_KEY. LLM_PROVIDER defaults to "deepseek", which runs every
// stage on DeepSeek's cheap chat model; an unknown value fails fast at startup
// rather than silently falling back. The per-stage model and the Anthropic
// per-stage key vars are unchanged; only the provider and the Gemini/DeepSeek keys
// are global. GEMINI_API_KEY and DEEPSEEK_API_KEY are secrets and are never logged.
const (
	// LLMProviderDeepSeek runs every stage on DeepSeek's OpenAI-compatible chat
	// model, the default when LLM_PROVIDER is unset, keyed on DEEPSEEK_API_KEY.
	LLMProviderDeepSeek = "deepseek"
	// LLMProviderAnthropic runs every stage on the Anthropic Claude transport,
	// keyed on each stage's per-stage Anthropic key.
	LLMProviderAnthropic = "anthropic"
	// LLMProviderGemini routes every stage to Google Gemini, keyed on
	// GEMINI_API_KEY.
	LLMProviderGemini = "gemini"
)

// LLMSelection is the shared provider choice and global provider keys threaded
// into every LLM-backed stage's configuration. Provider is the validated
// LLM_PROVIDER value (default LLMProviderDeepSeek); GeminiAPIKey is read from
// GEMINI_API_KEY and DeepSeekAPIKey from DEEPSEEK_API_KEY, each required only when
// its provider is selected and a stage is active. Neither is ever logged.
type LLMSelection struct {
	Provider       string
	GeminiAPIKey   string
	DeepSeekAPIKey string
}

// providerKey returns the key the selected provider needs, given the stage's
// Anthropic per-stage key. Under Gemini it is GeminiAPIKey, under DeepSeek (the
// default) it is DeepSeekAPIKey, and under Anthropic it is the per-stage key. It
// is the single source of the provider-key choice so the loaders and Active()
// checks cannot drift in which secret a provider reads.
func (s LLMSelection) providerKey(anthropicKey string) string {
	switch s.Provider {
	case LLMProviderGemini:
		return s.GeminiAPIKey
	case LLMProviderDeepSeek:
		return s.DeepSeekAPIKey
	default:
		return anthropicKey
	}
}

// hasKey reports whether the selected provider has the key it needs, given the
// stage's Anthropic per-stage key. Every stage's Active() routes its key-presence
// check through this so an enabled-but-keyless feature degrades to off under
// whichever provider is selected - selecting DeepSeek and supplying only
// DEEPSEEK_API_KEY keeps the stage active, where checking the Anthropic key alone
// would silently disable it.
func (s LLMSelection) hasKey(anthropicKey string) bool {
	return s.providerKey(anthropicKey) != ""
}

// defaultStageModel is the cheapest, fastest current Claude model - the shared
// Anthropic default for every LLM-backed stage's env layer (the per-stage
// *_MODEL knobs all default to it). Each provider's adapter keeps its own default
// for the other backends.
const defaultStageModel = "claude-haiku-4-5-20251001"

// defaultModel returns the per-stage default model for the selected provider: the
// shared Anthropic model under Anthropic, and "" under any other provider so that
// provider's adapter applies its own default (DeepSeek and Gemini name different
// models than Claude, so threading the Anthropic model would send an unknown
// model to them). An explicit per-stage *_MODEL override always takes precedence.
func (s LLMSelection) defaultModel() string {
	if s.Provider == LLMProviderAnthropic {
		return defaultStageModel
	}
	return ""
}

// secondPassModel returns the per-provider default reasoning model for the deeper
// second pass: the larger DeepSeek reasoning tier under DeepSeek (the default
// provider), and "" under any other provider so the operator must name a reasoning
// model the provider knows (Anthropic and Gemini have no second-pass default here,
// and threading DeepSeek's id would send an unknown model). An explicit
// FACTCHECK_SECOND_PASS_MODEL override always takes precedence.
func (s LLMSelection) secondPassModel() string {
	if s.Provider == LLMProviderDeepSeek {
		return defaultSecondPassModel
	}
	return ""
}

// loadLLMSelection reads the shared LLM provider choice from the environment.
// LLM_PROVIDER defaults to deepseek and is validated against the known set, so an
// unknown provider crashes the process at startup with a clear message rather than
// degrading silently. The Gemini and DeepSeek keys are read but never logged.
func loadLLMSelection() (LLMSelection, error) {
	provider := strings.ToLower(strings.TrimSpace(getenv("LLM_PROVIDER", LLMProviderDeepSeek)))
	switch provider {
	case LLMProviderDeepSeek, LLMProviderAnthropic, LLMProviderGemini:
	default:
		return LLMSelection{}, fmt.Errorf("config: LLM_PROVIDER %q is not a known provider (want %q, %q, or %q)", provider, LLMProviderDeepSeek, LLMProviderAnthropic, LLMProviderGemini)
	}
	return LLMSelection{
		Provider:       provider,
		GeminiAPIKey:   getenv("GEMINI_API_KEY", ""),
		DeepSeekAPIKey: getenv("DEEPSEEK_API_KEY", ""),
	}, nil
}

// Config holds the runtime configuration for the server.
type Config struct {
	Port        string
	DatabaseURL string
	// DemoMediaDir is the directory the bundled demo clip is served from and
	// transcribed out of; the player and the pipeline read the same file.
	DemoMediaDir string
	// CORSAllowedOrigin is the browser origin permitted to call the API
	// cross-origin. Empty in production, where the frontend and API share an
	// origin and CORS is not needed; set to the dev frontend origin locally.
	CORSAllowedOrigin string
}

// Load reads configuration from the environment, applying defaults and
// failing fast when a required variable is missing.
func Load() (Config, error) {
	dbURL, err := requireEnv("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	return Config{
		Port:              getenv("PORT", "8080"),
		DatabaseURL:       dbURL,
		DemoMediaDir:      getenv("DEMO_MEDIA_DIR", defaultDemoMediaDir),
		CORSAllowedOrigin: os.Getenv("CORS_ALLOWED_ORIGIN"),
	}, nil
}

// Embedding holds the configuration for the Voyage embedding provider.
type Embedding struct {
	APIKey string
	Model  string
	Dim    int
}

// EmbeddingModel resolves the embedding model name from the environment:
// EMBEDDING_MODEL when set, otherwise DefaultEmbeddingModel. It needs no API key
// so the offline seed tooling (which embeds from the committed cache) and
// LoadEmbedding share one resolution rule - they can never key the cache under a
// different string than the running stack embeds with.
func EmbeddingModel() string {
	return getenv("EMBEDDING_MODEL", DefaultEmbeddingModel)
}

// LoadEmbedding reads the embedding provider configuration from the
// environment. EMBEDDING_API_KEY is required; the model defaults to
// voyage-4-large and the dimension is pinned to the claim store's EmbeddingDim. An
// EMBEDDING_DIM that disagrees with the pinned dimension is a fatal
// misconfiguration rather than a silent re-ingest hazard.
func LoadEmbedding() (Embedding, error) {
	apiKey, err := requireEnv("EMBEDDING_API_KEY")
	if err != nil {
		return Embedding{}, err
	}
	e := Embedding{
		APIKey: apiKey,
		Model:  EmbeddingModel(),
		Dim:    domain.EmbeddingDim,
	}
	if raw := os.Getenv("EMBEDDING_DIM"); raw != "" {
		dim, err := strconv.Atoi(raw)
		if err != nil {
			return Embedding{}, fmt.Errorf("config: EMBEDDING_DIM %q: %w", raw, err)
		}
		if dim != domain.EmbeddingDim {
			return Embedding{}, fmt.Errorf("config: EMBEDDING_DIM %d must equal the pinned dimension %d", dim, domain.EmbeddingDim)
		}
	}
	return e, nil
}

// Transcription holds the configuration for the AssemblyAI Universal-3 Pro
// streaming speech-to-text provider, the single transcriber for both live
// streams and imported videos. MaxSpeakers, when positive, hints the diarizer
// at the expected speaker count; zero leaves it provider-default.
type Transcription struct {
	APIKey      string
	Model       string
	MaxSpeakers int
}

// LoadTranscription reads the transcription configuration from the environment.
// TRANSCRIPTION_API_KEY is required; TRANSCRIPTION_MODEL defaults to u3-rt-pro;
// TRANSCRIPTION_MAX_SPEAKERS is optional and must be non-negative.
func LoadTranscription() (Transcription, error) {
	apiKey, err := requireEnv("TRANSCRIPTION_API_KEY")
	if err != nil {
		return Transcription{}, err
	}
	maxSpeakers, err := intEnv("TRANSCRIPTION_MAX_SPEAKERS", 0, 0, math.MaxInt32)
	if err != nil {
		return Transcription{}, err
	}
	return Transcription{
		APIKey:      apiKey,
		Model:       getenv("TRANSCRIPTION_MODEL", defaultTranscriptionModel),
		MaxSpeakers: maxSpeakers,
	}, nil
}

// Session defaults: 24h keeps the single operator signed in for a working day
// plus slack without leaving stolen cookies valid for long. 32 bytes is the
// minimum secret size for an HMAC-SHA256 key with full strength.
const (
	defaultSessionTTL      = 24 * time.Hour
	minSessionSecretLength = 32
)

// Auth holds the single-operator authentication configuration. SecureCookie
// is computed here, where it is testable, so no wiring layer ever negates the
// AUTH_INSECURE_COOKIE flag by hand.
type Auth struct {
	Email         string
	PasswordHash  string
	SessionSecret string
	SessionTTL    time.Duration
	SecureCookie  bool
}

// LoadAuth reads the authentication configuration from the environment. The
// operator email, the argon2id password hash, and the session-signing secret
// are required; the plaintext password is never read from anywhere. The
// session cookie is Secure unless AUTH_INSECURE_COOKIE explicitly opts out
// for local HTTP development.
func LoadAuth() (Auth, error) {
	email, err := requireEnv("AUTH_EMAIL")
	if err != nil {
		return Auth{}, err
	}
	hash, err := requireEnv("AUTH_PASSWORD_HASH")
	if err != nil {
		return Auth{}, err
	}
	secret, err := requireEnv("SESSION_SECRET")
	if err != nil {
		return Auth{}, err
	}
	if len(secret) < minSessionSecretLength {
		return Auth{}, fmt.Errorf("config: SESSION_SECRET must be at least %d bytes, got %d", minSessionSecretLength, len(secret))
	}
	ttl, err := positiveDurationEnv("SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return Auth{}, err
	}
	a := Auth{
		Email:         email,
		PasswordHash:  hash,
		SessionSecret: secret,
		SessionTTL:    ttl,
		SecureCookie:  true,
	}
	if raw := os.Getenv("AUTH_INSECURE_COOKIE"); raw != "" {
		insecure, err := strconv.ParseBool(raw)
		if err != nil {
			return Auth{}, fmt.Errorf("config: AUTH_INSECURE_COOKIE %q: %w", raw, err)
		}
		a.SecureCookie = !insecure
	}
	return a, nil
}

// LoadLegacyPasswordLogin reports whether the retired password-session login is
// re-enabled, gated by LEGACY_PASSWORD_LOGIN (default off). The /api subtree is
// gated by the Keycloak identity in every environment; this flag only restores
// the /api/login and /api/logout password routes for an environment that has no
// Keycloak yet. When off (the default, including production), the password
// machinery is never wired and the AUTH_* / SESSION_SECRET env vars are not read.
func LoadLegacyPasswordLogin() (bool, error) {
	enabled, err := boolEnv("LEGACY_PASSWORD_LOGIN")
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// Keycloak defaults target the local dev stack (docker-compose.yml publishes
// Keycloak on :8081 importing stack/keycloak/realm.json). Production overrides
// the issuer and client id from config/Secrets Manager so the same backend
// validates against the operator-managed Keycloak. The JWKS path is Keycloak's
// fixed OIDC certs endpoint under the issuer; it is derived from the issuer
// rather than configured separately so the two cannot drift, with an explicit
// override for an unusual topology.
const (
	defaultKeycloakIssuer   = "http://localhost:8081/realms/truth-in-stream"
	defaultKeycloakClientID = "truth-in-stream-web"
	// keycloakCertsPath is Keycloak's OIDC JWKS endpoint, appended to the issuer
	// to build the JWKS URL. It is fixed by the OIDC discovery document Keycloak
	// serves, not configurable per realm.
	keycloakCertsPath = "/protocol/openid-connect/certs"
)

// Keycloak holds the OIDC validation configuration for the identity provider.
// Issuer is the exact issuer string tokens carry and the realm advertises;
// ClientID is the authorized party the public web client sets; JWKSURL is where
// the signing keys are fetched and cached from. Issuer and ClientID default to
// the local dev realm so the stack validates out of the box; production sets
// them to the operator's Keycloak. The signature/issuer/azp validation logic
// lives in internal/auth.
type Keycloak struct {
	Issuer   string
	ClientID string
	JWKSURL  string
	// AdditionalClientIDs are extra authorized parties (azp) the verifier accepts
	// beyond the web ClientID: service-account clients that authenticate with the
	// client-credentials grant carry their own azp. The tvcapture worker uses the
	// tv-capture service client, so KEYCLOAK_ADDITIONAL_CLIENT_IDS is set to that
	// client id wherever the worker runs. Comma-separated; empty keeps the
	// verifier single-client.
	AdditionalClientIDs []string
}

// LoadKeycloak reads the Keycloak OIDC configuration from the environment.
// KEYCLOAK_ISSUER and KEYCLOAK_CLIENT_ID default to the local dev realm;
// KEYCLOAK_JWKS_URL defaults to the issuer's certs endpoint and is overridable
// only for an unusual topology (e.g. an internal JWKS host distinct from the
// public issuer). The defaults are non-empty, so the validator always has an
// issuer and authorized party to check against and never accepts tokens from
// anywhere; a trailing slash on the issuer is trimmed so the derived JWKS URL is
// well formed.
func LoadKeycloak() Keycloak {
	issuer := strings.TrimRight(getenv("KEYCLOAK_ISSUER", defaultKeycloakIssuer), "/")
	var additional []string
	for _, p := range strings.Split(os.Getenv("KEYCLOAK_ADDITIONAL_CLIENT_IDS"), ",") {
		if v := strings.TrimSpace(p); v != "" {
			additional = append(additional, v)
		}
	}
	return Keycloak{
		Issuer:              issuer,
		ClientID:            getenv("KEYCLOAK_CLIENT_ID", defaultKeycloakClientID),
		JWKSURL:             getenv("KEYCLOAK_JWKS_URL", issuer+keycloakCertsPath),
		AdditionalClientIDs: additional,
	}
}

// keycloakTokenPath is the OIDC token endpoint the tvcapture worker's
// client-credentials grant posts to, a sibling of the certs path.
const keycloakTokenPath = "/protocol/openid-connect/token"

const (
	// defaultTVCaptureClientID is the Keycloak service-account client the capture
	// worker authenticates as; it carries the scoped tv-capture realm role (not
	// blanket admin), which authorizes only the TV feed and recording endpoints.
	defaultTVCaptureClientID = "tv-capture"
	// defaultTVCaptureBackendURL is the backend API base the worker calls (channel
	// list, presign, register, prune) and dials the feed WebSocket on. Compose and
	// the deploy host override it with the internal backend address.
	defaultTVCaptureBackendURL = "http://localhost:8080"
	// defaultTVCaptureWorkDir is where segments are written before upload; the
	// container mounts a volume here so a crash leaves partial segments for the
	// startup salvage pass.
	defaultTVCaptureWorkDir = "/work"
)

// TVCapture configures the tvcapture worker: how it authenticates, which backend
// it talks to, and the capture/archive/retention tunables. When Enabled is
// false the worker idles (it does not exit, avoiding a restart loop under
// restart: unless-stopped) and no secret is required. ClientSecret is read from
// the environment only and never logged.
type TVCapture struct {
	Enabled         bool
	BackendBaseURL  string
	TokenURL        string
	ClientID        string
	ClientSecret    string
	SegmentDuration time.Duration
	RetentionDays   int
	FeedStall       time.Duration
	PollInterval    time.Duration
	WorkDir         string
	StreamlinkPath  string
	FFmpegPath      string
	SlackWebhookURL string
}

// Active reports whether capture is turned on. A worker built from an inactive
// config idles without requiring credentials.
func (c TVCapture) Active() bool { return c.Enabled }

// LoadTVCapture reads the tvcapture worker configuration. TV_CAPTURE_ENABLED
// gates the whole worker (default off). When enabled, TV_CAPTURE_CLIENT_SECRET
// is required (the service-account credential); the rest default. The token
// endpoint is derived from KEYCLOAK_ISSUER unless TV_CAPTURE_TOKEN_URL overrides
// it. Bad values fail fast at startup.
func LoadTVCapture() (TVCapture, error) {
	enabled, err := boolEnvDefault("TV_CAPTURE_ENABLED", false)
	if err != nil {
		return TVCapture{}, err
	}
	segmentSeconds, err := intEnv("TV_SEGMENT_SECONDS", 3600, 60, 86400)
	if err != nil {
		return TVCapture{}, err
	}
	retentionDays, err := intEnv("TV_RECORDING_RETENTION_DAYS", 30, 1, 3650)
	if err != nil {
		return TVCapture{}, err
	}
	stallSeconds, err := intEnv("TV_FEED_STALL_SECONDS", 60, 5, 3600)
	if err != nil {
		return TVCapture{}, err
	}
	pollSeconds, err := intEnv("TV_CAPTURE_POLL_SECONDS", 30, 5, 3600)
	if err != nil {
		return TVCapture{}, err
	}
	tokenURL := os.Getenv("TV_CAPTURE_TOKEN_URL")
	if tokenURL == "" {
		issuer := strings.TrimRight(getenv("KEYCLOAK_ISSUER", defaultKeycloakIssuer), "/")
		tokenURL = issuer + keycloakTokenPath
	}
	cfg := TVCapture{
		Enabled:         enabled,
		BackendBaseURL:  strings.TrimRight(getenv("TV_CAPTURE_BACKEND_URL", defaultTVCaptureBackendURL), "/"),
		TokenURL:        tokenURL,
		ClientID:        getenv("TV_CAPTURE_CLIENT_ID", defaultTVCaptureClientID),
		ClientSecret:    os.Getenv("TV_CAPTURE_CLIENT_SECRET"),
		SegmentDuration: time.Duration(segmentSeconds) * time.Second,
		RetentionDays:   retentionDays,
		FeedStall:       time.Duration(stallSeconds) * time.Second,
		PollInterval:    time.Duration(pollSeconds) * time.Second,
		WorkDir:         getenv("TV_CAPTURE_WORK_DIR", defaultTVCaptureWorkDir),
		StreamlinkPath:  getenv("TV_STREAMLINK_PATH", "streamlink"),
		FFmpegPath:      getenv("TV_FFMPEG_PATH", "ffmpeg"),
		SlackWebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
	}
	if enabled && cfg.ClientSecret == "" {
		return TVCapture{}, fmt.Errorf("config: TV_CAPTURE_CLIENT_SECRET is required when TV_CAPTURE_ENABLED is set")
	}
	return cfg, nil
}

// defaultAnalysisCacheTTL is how long a completed video's cached analysis lives
// before it expires and a replay re-runs the pipeline. 24h gives an instant
// replay for a day after a video finishes without pinning stale verdicts in the
// cache forever. ANALYSIS_CACHE_TTL overrides it.
const defaultAnalysisCacheTTL = 24 * time.Hour

// AnalysisCache holds the analysis-cache configuration. RedisURL is the Redis or
// Valkey connection string (a redis:// or rediss:// URL parsed by redis.ParseURL);
// empty disables caching entirely and the service uses the no-op cache, behaving
// exactly as it does today. TTL bounds how long a cached analysis lives. RedisURL
// can carry a password, so it is a secret: read from the environment only, never
// logged.
type AnalysisCache struct {
	RedisURL string
	TTL      time.Duration
}

// Enabled reports whether Redis caching is configured: a non-empty RedisURL.
// Wiring keys off this so an unset URL degrades to the no-op cache rather than
// failing to start.
func (a AnalysisCache) Enabled() bool {
	return a.RedisURL != ""
}

// LoadAnalysisCache reads the analysis-cache configuration from the environment.
// REDIS_URL is optional: when unset the cache is disabled and the no-op store is
// used. ANALYSIS_CACHE_TTL overrides the 24h default and must be positive (a
// non-positive TTL has no valid expiry window). REDIS_URL carries a secret and is
// never logged.
func LoadAnalysisCache() (AnalysisCache, error) {
	ttl, err := positiveDurationEnv("ANALYSIS_CACHE_TTL", defaultAnalysisCacheTTL)
	if err != nil {
		return AnalysisCache{}, err
	}
	return AnalysisCache{
		RedisURL: os.Getenv("REDIS_URL"),
		TTL:      ttl,
	}, nil
}

// Match defaults: top-5 keeps responses focused, 0.5 cosine similarity drops
// unrelated text without hiding genuine paraphrases, 4 concurrent embed calls
// stay well inside the Voyage rate limits, and 10s bounds a single segment
// end to end. Evidence retrieval pulls 5 Wikipedia chunks at a higher 0.6
// threshold (the corpus is far larger, so a stricter bar avoids loosely related
// noise), and the merged result is capped at 5 across both corpora. Confidence
// scoring aggregates the top-5 matches; a lead chunk corroborates at full weight
// and a body chunk at 0.6, since a lead summary is higher-signal than buried
// prose.
const (
	defaultMatchTopK                  = 5
	defaultMatchScoreThreshold        = 0.5
	defaultMatchEvidenceTopK          = 5
	defaultMatchEvidenceThreshold     = 0.6
	defaultMatchMaxResults            = 5
	defaultMatchEmbedConcurrency      = 4
	defaultMatchTimeout               = 10 * time.Second
	defaultMatchConfidenceClusterSize = 5
	defaultMatchConfidenceLeadWeight  = 1.0
	defaultMatchConfidenceBodyWeight  = 0.6
	// Hybrid retrieval (VER-195) is on by default: fusing a French lexical
	// full-text branch with the vector branch catches exact figures, dates, and
	// named entities dense embeddings blur. LexicalTopK over-fetches the lexical
	// pool per corpus (~4x the vector TopK of 5) so a lexically exact but
	// cosine-distant passage still earns a rank to fuse; RRFK is the conventional
	// Reciprocal Rank Fusion constant. Both defaults are starting points to be
	// re-tuned from the retrieval eval.
	defaultMatchHybridSearch = true
	defaultMatchLexicalTopK  = 20
	defaultMatchRRFK         = 60
	// Per-corpus HNSW ef_search (VER-202). 0 keeps pgvector's per-connection
	// session default (the long-standing hot-path behavior); a positive value
	// raises the per-query candidate list for that corpus alone, trading latency
	// for recall, routed through the store's existing per-query searchTuned
	// plumbing. The claims and evidence corpora carry independent knobs because a
	// small trusted corpus and a large noisy one warrant different budgets. Both
	// default to 0 (unchanged behavior); raise only with eval/latency evidence.
	defaultMatchClaimsEfSearch   = 0
	defaultMatchEvidenceEfSearch = 0
	// maxHNSWEfSearch is pgvector's documented upper bound for hnsw.ef_search
	// (valid range 1..1000). 0 is accepted as "keep the session default", so the
	// config bound is [0, maxHNSWEfSearch]. Kept as a local constant so config
	// stays free of a store-package dependency.
	maxHNSWEfSearch = 1000
	// Retrieval reranking (VER-226) is on by default since the vector-first defaults (VER-230); MATCH_RERANK=false is the kill-switch. The candidate pool of 20
	// per corpus matches the lexical over-fetch above, and the timeout keeps the
	// rerank call well inside the verify fast-deadline so a slow rerank degrades
	// to the fused order instead of stalling a verdict. The model default lives
	// in the rerank package (rerank.DefaultModel).
	defaultRerankCandidates = 20
	defaultRerankTimeout    = 800 * time.Millisecond
	// maxRerankCandidates is the rerank API's per-request document ceiling.
	maxRerankCandidates = 1000
	// Telemetry recorder defaults: a small in-memory buffer between the live
	// loop and the writer, flushed at least once a second.
	defaultTelemetryQueueDepth = 256
	defaultTelemetryFlushEvery = time.Second
)

// Match holds the segment matching configuration across the curated claims and
// Wikipedia evidence corpora, plus the confidence-scoring knobs that aggregate a
// statement's matched cluster into a corroboration score.
type Match struct {
	TopK                  int
	ScoreThreshold        float64
	EvidenceTopK          int
	EvidenceThreshold     float64
	MaxResults            int
	EmbedConcurrency      int
	Timeout               time.Duration
	ConfidenceClusterSize int
	ConfidenceLeadWeight  float64
	ConfidenceBodyWeight  float64
	HybridSearch          bool
	LexicalTopK           int
	RRFK                  int
	// ClaimsEfSearch and EvidenceEfSearch are the per-corpus HNSW ef_search
	// values threaded into each corpus's retrieval; 0 keeps the session default.
	ClaimsEfSearch   int
	EvidenceEfSearch int
	// RecencyHalfLife, when positive, decays a dated evidence hit's
	// corroboration weight by half per half-life of age; 0 (the default) keeps
	// scoring untouched. Undated evidence is never decayed.
	RecencyHalfLife time.Duration
}

// LoadMatch reads the matching configuration from the environment, applying
// defaults and failing fast on values that would make matching meaningless
// (out-of-range k or concurrency, a threshold outside cosine similarity's
// [-1, 1] range, a non-positive timeout, a confidence weight outside [0, 1]).
// MATCH_EVIDENCE_TOP_K 0 disables Wikipedia evidence retrieval.
func LoadMatch() (Match, error) {
	m := Match{
		TopK:                  defaultMatchTopK,
		ScoreThreshold:        defaultMatchScoreThreshold,
		EvidenceTopK:          defaultMatchEvidenceTopK,
		EvidenceThreshold:     defaultMatchEvidenceThreshold,
		MaxResults:            defaultMatchMaxResults,
		EmbedConcurrency:      defaultMatchEmbedConcurrency,
		Timeout:               defaultMatchTimeout,
		ConfidenceClusterSize: defaultMatchConfidenceClusterSize,
		ConfidenceLeadWeight:  defaultMatchConfidenceLeadWeight,
		ConfidenceBodyWeight:  defaultMatchConfidenceBodyWeight,
		HybridSearch:          defaultMatchHybridSearch,
		LexicalTopK:           defaultMatchLexicalTopK,
		RRFK:                  defaultMatchRRFK,
		ClaimsEfSearch:        defaultMatchClaimsEfSearch,
		EvidenceEfSearch:      defaultMatchEvidenceEfSearch,
	}
	var err error
	if m.TopK, err = intEnv("MATCH_TOP_K", m.TopK, 1, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.ScoreThreshold, err = thresholdEnv("MATCH_SCORE_THRESHOLD", m.ScoreThreshold); err != nil {
		return Match{}, err
	}
	if raw := os.Getenv("MATCH_SCORE_THRESHOLD"); raw != "" {
		threshold, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Match{}, fmt.Errorf("config: MATCH_SCORE_THRESHOLD %q: %w", raw, err)
		}
		if !domain.ValidCosineThreshold(threshold) {
			return Match{}, fmt.Errorf("config: MATCH_SCORE_THRESHOLD %v outside cosine similarity range [-1, 1]", threshold)
		}
		m.ScoreThreshold = threshold
	}
	if m.EvidenceTopK, err = intEnv("MATCH_EVIDENCE_TOP_K", m.EvidenceTopK, 0, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.EvidenceThreshold, err = thresholdEnv("MATCH_EVIDENCE_SCORE_THRESHOLD", m.EvidenceThreshold); err != nil {
		return Match{}, err
	}
	if m.MaxResults, err = intEnv("MATCH_MAX_RESULTS", m.MaxResults, 1, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.EmbedConcurrency, err = intEnv("MATCH_EMBED_CONCURRENCY", m.EmbedConcurrency, 1, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.Timeout, err = positiveDurationEnv("MATCH_TIMEOUT", m.Timeout); err != nil {
		return Match{}, err
	}
	if m.ConfidenceClusterSize, err = intEnv("MATCH_CONFIDENCE_CLUSTER_SIZE", m.ConfidenceClusterSize, 1, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.ConfidenceLeadWeight, err = floatEnv("MATCH_CONFIDENCE_LEAD_WEIGHT", m.ConfidenceLeadWeight); err != nil {
		return Match{}, err
	}
	if m.ConfidenceBodyWeight, err = floatEnv("MATCH_CONFIDENCE_BODY_WEIGHT", m.ConfidenceBodyWeight); err != nil {
		return Match{}, err
	}
	if m.HybridSearch, err = boolEnvDefault("MATCH_HYBRID_SEARCH", m.HybridSearch); err != nil {
		return Match{}, err
	}
	if m.LexicalTopK, err = intEnv("MATCH_LEXICAL_TOP_K", m.LexicalTopK, 1, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.RRFK, err = intEnv("MATCH_RRF_K", m.RRFK, 1, math.MaxInt32); err != nil {
		return Match{}, err
	}
	if m.ClaimsEfSearch, err = intEnv("MATCH_CLAIMS_EF_SEARCH", m.ClaimsEfSearch, 0, maxHNSWEfSearch); err != nil {
		return Match{}, err
	}
	if m.EvidenceEfSearch, err = intEnv("MATCH_EVIDENCE_EF_SEARCH", m.EvidenceEfSearch, 0, maxHNSWEfSearch); err != nil {
		return Match{}, err
	}
	if m.RecencyHalfLife, err = durationEnvAllowZero("MATCH_RECENCY_HALF_LIFE", 0); err != nil {
		return Match{}, err
	}
	return m, nil
}

// Telemetry configures the asynchronous per-claim pipeline telemetry recorder
// (VER-229): one analytical claim_checks row per live decision, written in
// batches off the hot path. On by default because it is pure additive
// observability - it changes no verdict and its growth is bounded by sampling
// and the retention sweep; TELEMETRY_ENABLED=false switches it off entirely.
type Telemetry struct {
	Enabled    bool
	QueueDepth int
	FlushEvery time.Duration
	SampleRate float64
}

// LoadTelemetry reads the telemetry configuration from the environment,
// failing fast on a sample rate outside (0, 1] or non-positive bounds.
func LoadTelemetry() (Telemetry, error) {
	t := Telemetry{Enabled: true, QueueDepth: defaultTelemetryQueueDepth, FlushEvery: defaultTelemetryFlushEvery, SampleRate: 1}
	var err error
	if t.Enabled, err = boolEnvDefault("TELEMETRY_ENABLED", t.Enabled); err != nil {
		return Telemetry{}, err
	}
	if t.QueueDepth, err = intEnv("TELEMETRY_QUEUE_DEPTH", t.QueueDepth, 1, math.MaxInt32); err != nil {
		return Telemetry{}, err
	}
	if t.FlushEvery, err = positiveDurationEnv("TELEMETRY_FLUSH_INTERVAL", t.FlushEvery); err != nil {
		return Telemetry{}, err
	}
	if t.SampleRate, err = floatEnv("TELEMETRY_SAMPLE_RATE", t.SampleRate); err != nil {
		return Telemetry{}, err
	}
	if t.SampleRate <= 0 || t.SampleRate > 1 {
		return Telemetry{}, fmt.Errorf("config: TELEMETRY_SAMPLE_RATE %v outside (0, 1]", t.SampleRate)
	}
	return t, nil
}

// Rerank configures the optional cross-encoder rerank stage of retrieval
// (MATCH_RERANK): the fused candidate pool is re-scored by the Voyage rerank
// API and relevance decides which candidates survive the final cut. The key
// falls back to EMBEDDING_API_KEY because both call the same Voyage account;
// RERANK_API_KEY overrides it when the accounts differ. Following the
// enabled-and-keyed Active convention, an enabled-but-keyless stage degrades
// to the fused order rather than failing boot.
type Rerank struct {
	Enabled    bool
	Model      string
	APIKey     string
	Candidates int
	Timeout    time.Duration
}

// Active reports whether reranking should be wired: enabled with a usable key.
func (r Rerank) Active() bool {
	return r.Enabled && r.APIKey != ""
}

// LoadRerank reads the rerank configuration from the environment, applying
// defaults and failing fast on a candidate pool outside the API's per-request
// ceiling or a non-positive timeout.
func LoadRerank() (Rerank, error) {
	r := Rerank{
		Model:      getenv("MATCH_RERANK_MODEL", rerank.DefaultModel),
		Candidates: defaultRerankCandidates,
		Timeout:    defaultRerankTimeout,
	}
	var err error
	if r.Enabled, err = boolEnvDefault("MATCH_RERANK", true); err != nil {
		return Rerank{}, err
	}
	if r.Candidates, err = intEnv("MATCH_RERANK_CANDIDATES", r.Candidates, 1, maxRerankCandidates); err != nil {
		return Rerank{}, err
	}
	if r.Timeout, err = positiveDurationEnv("MATCH_RERANK_TIMEOUT", r.Timeout); err != nil {
		return Rerank{}, err
	}
	r.APIKey = os.Getenv("RERANK_API_KEY")
	if r.APIKey == "" {
		r.APIKey = os.Getenv("EMBEDDING_API_KEY")
	}
	return r, nil
}

// Debug-search defaults: 10 neighbors is enough to eyeball corpus coverage
// without flooding the bar, and 10s bounds one embed-plus-search round trip.
const (
	defaultDebugSearchTopK    = 10
	defaultDebugSearchTimeout = 10 * time.Second
)

// DebugSearch holds the developer wiki-search probe configuration. Enabled is
// off unless DEBUG_WIKI_SEARCH opts in; the route and its WebSocket only exist
// when it is true, so the probe is unreachable in production. TopK caps the
// neighbors returned and Timeout bounds one query.
type DebugSearch struct {
	Enabled bool
	TopK    int
	Timeout time.Duration
}

// LoadDebugFactCheck reports whether the operator fact-check detail view is
// enabled, gated by DEBUG_FACT_CHECK (default off), mirroring how
// DEBUG_WIKI_SEARCH gates the wiki-search probe. When off, the live per-claim
// result frame carries only the source label and omits the per-passage evidence
// detail, so the detailed payload is never emitted in production.
func LoadDebugFactCheck() (bool, error) {
	enabled, err := boolEnv("DEBUG_FACT_CHECK")
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// LoadDebugSearch reads the developer wiki-search configuration from the
// environment. DEBUG_WIKI_SEARCH gates the whole feature (default off);
// DEBUG_WIKI_SEARCH_TOP_K and DEBUG_WIKI_SEARCH_TIMEOUT tune it when enabled.
func LoadDebugSearch() (DebugSearch, error) {
	enabled, err := boolEnv("DEBUG_WIKI_SEARCH")
	if err != nil {
		return DebugSearch{}, err
	}
	d := DebugSearch{
		Enabled: enabled,
		TopK:    defaultDebugSearchTopK,
		Timeout: defaultDebugSearchTimeout,
	}
	if d.TopK, err = intEnv("DEBUG_WIKI_SEARCH_TOP_K", d.TopK, 1, math.MaxInt32); err != nil {
		return DebugSearch{}, err
	}
	if d.Timeout, err = positiveDurationEnv("DEBUG_WIKI_SEARCH_TIMEOUT", d.Timeout); err != nil {
		return DebugSearch{}, err
	}
	return d, nil
}

// Precheck defaults: a 4-word minimum drops bare fragments while keeping short
// real claims, and a 0.4 cosine coverage floor sits below the 0.5 match
// threshold so coverage only skips clearly-uncovered claims - a covered claim
// can still yield no confident match, but an uncovered one is never forced into
// a verdict. The gate is on by default; precision over recall is the point.
const (
	defaultPrecheckMinWords          = 4
	defaultPrecheckCoverageThreshold = 0.4
	// defaultPrecheckWikiCoverageThreshold is the wiki corpus's coverage floor.
	// Coverage answers "is this topic present in the corpus at all", a strictly
	// lower bar than the evidence/match threshold (defaultMatchEvidenceThreshold),
	// which answers "is this hit strong enough to drive a verdict"; the two must
	// not be conflated, and the floor inherited from the latter is what wrongly
	// skipped grounded segments before VER-67. It is calibrated instead to the
	// band the seeded corpus actually retrieves in: with
	// voyage-4-large (input_type=query) on-topic factual statements top out at
	// 0.51-0.65 (e.g. "Weed is a serious drug..." -> Legality of cannabis 0.5143)
	// while off-topic conversational filler tops out at 0.32-0.42. 0.46 sits in
	// that separation gap, admitting the on-topic band while still rejecting
	// filler; the old 0.6 sat above the on-topic band entirely.
	// PRECHECK_WIKI_COVERAGE_THRESHOLD overrides it.
	defaultPrecheckWikiCoverageThreshold = 0.46
	// defaultPrecheckCoverageEfSearch raises hnsw.ef_search for the coverage probe
	// above pgvector's session default. Coverage decides whether a segment is
	// checkable at all, so missing the true nearest neighbor is a false "not
	// covered"; the VER-173 benchmark showed 200 reaches full recall for a
	// marginal latency cost. It matches the former service-side coverageEfSearch
	// constant, now env-tunable (VER-202). PRECHECK_COVERAGE_EF_SEARCH overrides
	// it; 0 keeps the session default.
	defaultPrecheckCoverageEfSearch = 200
)

// Precheck holds the check-worthiness gate configuration. Enabled toggles the
// whole gate; MinWords bounds the claim-worthiness fragment filter;
// CoverageThreshold is the minimum curated-claims similarity a claim must reach
// to be checked. WikiCoverageEnabled adds the embedded wiki corpus as a second
// coverage source (PRECHECK_WIKI_COVERAGE_ENABLED, default on), and
// WikiCoverageThreshold (PRECHECK_WIKI_COVERAGE_THRESHOLD) is its own floor,
// calibrated to the wiki corpus's retrieval band rather than inherited from the
// evidence threshold; a segment is covered when either corpus clears its floor.
type Precheck struct {
	Enabled               bool
	MinWords              int
	CoverageThreshold     float64
	WikiCoverageEnabled   bool
	WikiCoverageThreshold float64
	// CoverageEfSearch is the HNSW ef_search the coverage probe runs at across
	// both coverage corpora. It defaults to 200, the former hard-coded probe
	// budget, now tunable from the environment. Unlike the matcher's per-corpus
	// ef_search knobs, 0 does NOT keep pgvector's session default here: the
	// coverage stage applies its recall-critical 200 default when the value is
	// non-positive (see service.defaultCoverageEfSearch).
	CoverageEfSearch int
}

// LoadPrecheck reads the precheck-gate configuration from the environment,
// applying defaults and failing fast on a coverage threshold outside cosine
// similarity's [-1, 1] range or a non-positive minimum word count.
func LoadPrecheck() (Precheck, error) {
	p := Precheck{
		Enabled:               true,
		MinWords:              defaultPrecheckMinWords,
		CoverageThreshold:     defaultPrecheckCoverageThreshold,
		WikiCoverageEnabled:   true,
		WikiCoverageThreshold: defaultPrecheckWikiCoverageThreshold,
		CoverageEfSearch:      defaultPrecheckCoverageEfSearch,
	}
	if raw := os.Getenv("PRECHECK_ENABLED"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Precheck{}, fmt.Errorf("config: PRECHECK_ENABLED %q: %w", raw, err)
		}
		p.Enabled = enabled
	}
	var err error
	if p.MinWords, err = intEnv("PRECHECK_MIN_WORDS", p.MinWords, 1, math.MaxInt32); err != nil {
		return Precheck{}, err
	}
	if raw := os.Getenv("PRECHECK_COVERAGE_THRESHOLD"); raw != "" {
		threshold, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Precheck{}, fmt.Errorf("config: PRECHECK_COVERAGE_THRESHOLD %q: %w", raw, err)
		}
		if !domain.ValidCosineThreshold(threshold) {
			return Precheck{}, fmt.Errorf("config: PRECHECK_COVERAGE_THRESHOLD %v outside cosine similarity range [-1, 1]", threshold)
		}
		p.CoverageThreshold = threshold
	}
	if raw := os.Getenv("PRECHECK_WIKI_COVERAGE_ENABLED"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return Precheck{}, fmt.Errorf("config: PRECHECK_WIKI_COVERAGE_ENABLED %q: %w", raw, err)
		}
		p.WikiCoverageEnabled = enabled
	}
	if p.WikiCoverageThreshold, err = thresholdEnv("PRECHECK_WIKI_COVERAGE_THRESHOLD", p.WikiCoverageThreshold); err != nil {
		return Precheck{}, err
	}
	if p.CoverageEfSearch, err = intEnv("PRECHECK_COVERAGE_EF_SEARCH", p.CoverageEfSearch, 0, maxHNSWEfSearch); err != nil {
		return Precheck{}, err
	}
	return p, nil
}

// Live analysis defaults. Concurrency 4 is the long-standing in-flight scoring
// bound. QueueDepth 32 buffers a burst of ready units behind the workers so a
// short surge of fast speech is scored rather than shed; only sustained demand
// past worker-plus-queue capacity falls back to not_checked. These are the
// env-layer defaults; the service package keeps matching library defaults for
// direct construction (service.NewLiveAnalyzer) and must stay in sync with them.
const (
	defaultLiveConcurrency = 4
	defaultLiveQueueDepth  = 32
	// defaultLiveMaxSentences caps one analysis unit at a few sentences: enough
	// surrounding speech for the decomposer to extract claims a lone sentence
	// leaves ambiguous, while a verdict still reads as one thought. The service
	// package keeps a matching library default (service.defaultMaxSentences) and
	// the two must stay in sync.
	defaultLiveMaxSentences = 4
)

// Live holds the live-analyzer scoring configuration. Concurrency is the number
// of verdict workers; QueueDepth is the bounded backlog those workers drain
// before a ready unit is shed to not_checked; MaxSentences caps how many
// sentences accumulate into one analysis unit before it is scored.
type Live struct {
	Concurrency  int
	QueueDepth   int
	MaxSentences int
}

// LoadLive reads the live-analyzer configuration from the environment, applying
// defaults and failing fast when concurrency, queue depth, or the sentence cap
// is not a positive integer (a zero pool or zero buffer would stall or drop
// every statement; a zero cap would flush on every segment).
func LoadLive() (Live, error) {
	l := Live{Concurrency: defaultLiveConcurrency, QueueDepth: defaultLiveQueueDepth, MaxSentences: defaultLiveMaxSentences}
	var err error
	if l.Concurrency, err = intEnv("LIVE_CONCURRENCY", l.Concurrency, 1, math.MaxInt32); err != nil {
		return Live{}, err
	}
	if l.QueueDepth, err = intEnv("LIVE_QUEUE_DEPTH", l.QueueDepth, 1, math.MaxInt32); err != nil {
		return Live{}, err
	}
	if l.MaxSentences, err = intEnv("LIVE_MAX_SENTENCES", l.MaxSentences, 1, math.MaxInt32); err != nil {
		return Live{}, err
	}
	return l, nil
}

// Intra-speaker consistency defaults. TopK 3 caps stance calls per statement;
// SimilarityFloor 0.6 keeps the stance check off topically-unrelated prior
// statements. These are the env-layer defaults; the service and stance packages
// keep matching library defaults for direct construction and must stay in sync
// with them. The stance model defaults to defaultStageModel (see defaultModel).
const (
	defaultConsistencyTopK  = 3
	defaultConsistencyFloor = 0.6
)

// Consistency holds the live intra-speaker consistency configuration. The
// feature is off unless Enabled is true and APIKey is set: with no key, live
// analysis behaves exactly as before, emitting no consistency flags. APIKey is
// a secret and comes from the environment only - never logged. Model selects
// the stance model; TopK and SimilarityFloor tune detection.
type Consistency struct {
	LLMSelection
	Enabled         bool
	APIKey          string
	Model           string
	TopK            int
	SimilarityFloor float64
}

// Active reports whether the consistency feature should be wired: it is enabled
// and has the API key it needs. Wiring keys off this so an enabled-but-keyless
// configuration degrades to off rather than failing to start.
func (c Consistency) Active() bool {
	return c.Enabled && c.hasKey(c.APIKey)
}

// LoadConsistency reads the intra-speaker consistency configuration from the
// environment, applying defaults and failing fast on an out-of-range top-k or
// similarity floor. The secret is read but never logged.
func LoadConsistency() (Consistency, error) {
	c := Consistency{
		TopK:            defaultConsistencyTopK,
		SimilarityFloor: defaultConsistencyFloor,
	}
	llmSel, err := loadLLMSelection()
	if err != nil {
		return Consistency{}, err
	}
	c.LLMSelection = llmSel
	if c.Enabled, err = boolEnv("CONSISTENCY_ENABLED"); err != nil {
		return Consistency{}, err
	}
	c.APIKey = getenv("CONSISTENCY_API_KEY", "")
	c.Model = getenv("CONSISTENCY_MODEL", llmSel.defaultModel())
	if c.TopK, err = intEnv("CONSISTENCY_TOP_K", c.TopK, 1, math.MaxInt32); err != nil {
		return Consistency{}, err
	}
	if c.SimilarityFloor, err = thresholdEnv("CONSISTENCY_SIMILARITY_FLOOR", c.SimilarityFloor); err != nil {
		return Consistency{}, err
	}
	// The floor is a lower bound on useful topical similarity, so it lives in
	// [0, 1]: a negative cosine floor would invite stance checks on
	// anti-correlated statements, the opposite of the topical gate's purpose.
	if c.SimilarityFloor < 0 {
		return Consistency{}, fmt.Errorf("config: CONSISTENCY_SIMILARITY_FLOOR %v must be in [0, 1]", c.SimilarityFloor)
	}
	return c, nil
}

// Retrieve-then-verify defaults. The path is on by default since the
// vector-first defaults (VER-230) and inactive without an API key, so a
// keyless deployment keeps the old gate-and-match path. Decomposition and verification both run on the selected provider's
// default fast model. MaxClaimsPerUnit caps a unit's fan-out at 4 atomic claims. FastTau 0.85
// is the curated near-match similarity at or above which the fast path borrows a
// verdict with no LLM call - a high bar, since a borrowed verdict bypasses
// reasoning. The verify pool is 2 workers with a 4-deep queue, smaller than the
// fast pool because each verify call is an LLM round-trip; FastDeadline 800ms
// bounds decompose-plus-retrieve and VerifyDeadline 4s bounds one verify call,
// matching the spec's tiers. CacheTTL 30s collapses a recurring talking point
// repeated within the window. These are the env-layer defaults; the service
// package validates the same bounds for direct construction.
const (
	defaultVerifyMaxClaimsPerUnit = 4
	defaultVerifyFastTau          = 0.85
	defaultVerifyConcurrency      = 2
	defaultVerifyQueueDepth       = 4
	defaultVerifyFastDeadline     = 800 * time.Millisecond
	defaultVerifyDeadline         = 4 * time.Second
	defaultVerifyCacheTTL         = 30 * time.Second
	// defaultVerifyCacheThreshold is the cosine-similarity bar the semantic claim
	// cache (VER-202) requires before it replays a cached verdict for a new claim.
	// The cache keys on the claim's query embedding: a paraphrase of a recent
	// talking point embeds very close to the original and reuses its verdict with
	// no verifier call, while a genuinely different claim stays below the bar and
	// is verified afresh. The default is deliberately high (precision over recall)
	// so a near-duplicate must be truly near before it shares a verdict, guarding
	// against a false cache share. FACTCHECK_VERIFY_CACHE_THRESHOLD overrides it.
	defaultVerifyCacheThreshold = 0.95
	// defaultVerifyCacheMaxEntries bounds the in-process semantic cache so a long
	// session cannot grow it without limit; the oldest entries are evicted first
	// once the bound is reached. FACTCHECK_VERIFY_CACHE_MAX_ENTRIES overrides it.
	defaultVerifyCacheMaxEntries = 1024
	// defaultVerifyRetrievalThreshold is the cosine floor for the evidence the
	// verify path retrieves and hands the LLM verifier. It is deliberately lower
	// than the legacy match/evidence threshold (defaultMatchEvidenceThreshold,
	// 0.6): that bar answers "is this hit strong enough to *borrow* a verdict by
	// similarity", where precision matters, whereas the verifier wants *recall* -
	// pull the plausible passages and let the model judge them. The legacy 0.6
	// "sat above the on-topic band entirely" (see defaultPrecheckWikiCoverageThreshold):
	// on-topic factual statements retrieve in 0.51-0.65 with voyage-4-large query
	// embeddings (e.g. "Weed is a serious drug..." -> Legality of cannabis 0.5143)
	// while off-topic filler tops out at 0.32-0.42. 0.45 sits in that separation
	// gap, admitting the on-topic band the verifier needs while still rejecting
	// filler; at 0.6 the verify path retrieves nothing for these statements and
	// every claim short-circuits to a no-evidence not_enough_info verdict.
	// FACTCHECK_VERIFY_RETRIEVAL_THRESHOLD overrides it.
	defaultVerifyRetrievalThreshold = 0.45
	// defaultSecondPassModel is the larger DeepSeek reasoning tier the deeper
	// second pass escalates to. It is materially more expensive per token than the
	// flash default, which is exactly why the second pass is off the hot path and
	// flag-gated. It is only the default under DeepSeek; FACTCHECK_SECOND_PASS_MODEL
	// overrides it, and under another provider the operator must set a model the
	// provider knows.
	defaultSecondPassModel = "deepseek-v4-pro"
	// defaultSecondPassBandLo and defaultSecondPassBandHi bound the fast-verdict
	// confidence band that qualifies for a deeper second look: above the on-topic
	// floor a verdict is too uncertain to act on, and below high confidence it is
	// not yet settled - the genuinely-hard-to-adjudicate middle is where a stronger
	// reasoner earns its cost. A confident verdict (>= hi) is already trusted and a
	// weak one (< lo) is better fixed by retrieval than by a bigger model.
	defaultSecondPassBandLo = 0.45
	defaultSecondPassBandHi = 0.8
	// defaultSecondPassDeadline bounds one reasoning reverify call. It is longer
	// than the fast verify deadline because a thinking model deliberates before
	// answering, but still bounded so a slow or stuck reasoner can never tie up a
	// claim's worker indefinitely.
	defaultSecondPassDeadline = 12 * time.Second
	// defaultFinalGateMinConfidence is the grounded confidence a terminal-gate
	// re-judgment must reach before it is allowed to REPLACE the pipeline's weak
	// verdict. It is deliberately high: the gate is the last word on an otherwise
	// unverifiable or low-confidence claim, so it adopts the reasoner only when the
	// deeper model is both grounded and near-certain, and otherwise leaves the honest
	// weak verdict in place. FACTCHECK_FINAL_GATE_MIN_CONFIDENCE overrides it.
	defaultFinalGateMinConfidence = 0.90
)

// VerifyPath holds the retrieve-then-verify configuration. The path is wired only
// when Enabled and APIKey is set (Active): with no key it degrades to the legacy
// gate-and-match path rather than failing to start. APIKey is a secret and comes
// from the environment only - never logged. Model selects both the decomposer and
// verifier model; MaxClaimsPerUnit caps fan-out; FastTau is the fast-path borrow
// threshold; Concurrency/QueueDepth size the verify pool; FastDeadline and
// VerifyDeadline bound the two tiers; CacheTTL is the repeated-claim cache window
// (0 disables it).
type VerifyPath struct {
	LLMSelection
	Enabled          bool
	APIKey           string
	Model            string
	MaxClaimsPerUnit int
	FastTau          float64
	Concurrency      int
	QueueDepth       int
	FastDeadline     time.Duration
	VerifyDeadline   time.Duration
	CacheTTL         time.Duration
	// CacheThreshold is the cosine-similarity bar the semantic claim cache
	// requires before it replays a cached verdict for a new claim's embedding, and
	// CacheMaxEntries bounds the cache's size (oldest evicted first). They matter
	// only when CacheTTL is positive (the cache is enabled).
	CacheThreshold  float64
	CacheMaxEntries int
	// RetrievalThreshold is the cosine floor for the evidence the verify path
	// retrieves and feeds the verifier. It is a recall bar, lower than the legacy
	// borrow-by-similarity threshold, so the on-topic band is retrieved rather than
	// discarded before the verifier ever sees it.
	RetrievalThreshold float64
}

// Active reports whether the retrieve-then-verify path should be wired: it is
// enabled and has the API key its decomposer and verifier need. Wiring keys off
// this so an enabled-but-keyless configuration degrades to the legacy path
// rather than failing to start.
func (v VerifyPath) Active() bool {
	return v.Enabled && v.hasKey(v.APIKey)
}

// LoadVerifyPath reads the retrieve-then-verify configuration from the
// environment, applying defaults and failing fast on out-of-range bounds (a
// non-positive pool or deadline, a fast tau outside cosine similarity's [-1, 1]
// range, a negative queue depth or cache ttl). FACTCHECK_VERIFY_PATH is the
// feature's kill-switch (default on since VER-230). The secret is read but
// never logged.
func LoadVerifyPath() (VerifyPath, error) {
	v := VerifyPath{
		MaxClaimsPerUnit:   defaultVerifyMaxClaimsPerUnit,
		FastTau:            defaultVerifyFastTau,
		Concurrency:        defaultVerifyConcurrency,
		QueueDepth:         defaultVerifyQueueDepth,
		FastDeadline:       defaultVerifyFastDeadline,
		VerifyDeadline:     defaultVerifyDeadline,
		CacheTTL:           defaultVerifyCacheTTL,
		CacheThreshold:     defaultVerifyCacheThreshold,
		CacheMaxEntries:    defaultVerifyCacheMaxEntries,
		RetrievalThreshold: defaultVerifyRetrievalThreshold,
	}
	llmSel, err := loadLLMSelection()
	if err != nil {
		return VerifyPath{}, err
	}
	v.LLMSelection = llmSel
	if v.Enabled, err = boolEnvDefault("FACTCHECK_VERIFY_PATH", true); err != nil {
		return VerifyPath{}, err
	}
	v.APIKey = getenv("FACTCHECK_VERIFY_API_KEY", "")
	v.Model = getenv("FACTCHECK_VERIFY_MODEL", llmSel.defaultModel())
	if v.MaxClaimsPerUnit, err = intEnv("FACTCHECK_VERIFY_MAX_CLAIMS_PER_UNIT", v.MaxClaimsPerUnit, 1, math.MaxInt32); err != nil {
		return VerifyPath{}, err
	}
	if v.FastTau, err = thresholdEnv("FACTCHECK_VERIFY_FAST_TAU", v.FastTau); err != nil {
		return VerifyPath{}, err
	}
	if v.RetrievalThreshold, err = thresholdEnv("FACTCHECK_VERIFY_RETRIEVAL_THRESHOLD", v.RetrievalThreshold); err != nil {
		return VerifyPath{}, err
	}
	if v.Concurrency, err = intEnv("FACTCHECK_VERIFY_CONCURRENCY", v.Concurrency, 1, math.MaxInt32); err != nil {
		return VerifyPath{}, err
	}
	if v.QueueDepth, err = intEnv("FACTCHECK_VERIFY_QUEUE_DEPTH", v.QueueDepth, 0, math.MaxInt32); err != nil {
		return VerifyPath{}, err
	}
	if v.FastDeadline, err = positiveDurationEnv("FACTCHECK_VERIFY_FAST_DEADLINE", v.FastDeadline); err != nil {
		return VerifyPath{}, err
	}
	if v.VerifyDeadline, err = positiveDurationEnv("FACTCHECK_VERIFY_DEADLINE", v.VerifyDeadline); err != nil {
		return VerifyPath{}, err
	}
	// 0 disables the repeated-claim cache; a positive value is the collapse window.
	if v.CacheTTL, err = durationEnvAllowZero("FACTCHECK_VERIFY_CACHE_TTL", v.CacheTTL); err != nil {
		return VerifyPath{}, err
	}
	if v.CacheThreshold, err = thresholdEnv("FACTCHECK_VERIFY_CACHE_THRESHOLD", v.CacheThreshold); err != nil {
		return VerifyPath{}, err
	}
	if v.CacheMaxEntries, err = intEnv("FACTCHECK_VERIFY_CACHE_MAX_ENTRIES", v.CacheMaxEntries, 1, math.MaxInt32); err != nil {
		return VerifyPath{}, err
	}
	return v, nil
}

// SecondPass holds the deeper-reasoner second-pass configuration. It is a gated
// upgrade to the verify path: when off (the default) or keyless, the verify path
// runs its single fast pass exactly as before. When active, an evidence-grounded
// fast verdict whose confidence sits in the [BandLo, BandHi] band is re-judged by a
// stronger reasoning Model out of the live hot path. Enabled gates the feature;
// APIKey is the reasoner's secret (env only, never logged); Model selects the
// larger reasoning tier; BandLo/BandHi bound the qualifying confidence band; and
// Deadline bounds one reverify call.
type SecondPass struct {
	LLMSelection
	Enabled  bool
	APIKey   string
	Model    string
	BandLo   float64
	BandHi   float64
	Deadline time.Duration
}

// Active reports whether the second pass should be wired: it is enabled and has
// the API key its reasoner needs. Wiring keys off this so an enabled-but-keyless
// configuration degrades to the single-pass verify path rather than failing to
// start, matching every other optional LLM stage.
func (s SecondPass) Active() bool {
	return s.Enabled && s.hasKey(s.APIKey)
}

// LoadSecondPass reads the deeper-reasoner second-pass configuration from the
// environment, applying defaults and failing fast on an out-of-range band (outside
// [0,1] or inverted) or a non-positive deadline. FACTCHECK_SECOND_PASS gates the
// whole feature (default off). The secret is read but never logged.
func LoadSecondPass() (SecondPass, error) {
	s := SecondPass{
		BandLo:   defaultSecondPassBandLo,
		BandHi:   defaultSecondPassBandHi,
		Deadline: defaultSecondPassDeadline,
	}
	llmSel, err := loadLLMSelection()
	if err != nil {
		return SecondPass{}, err
	}
	s.LLMSelection = llmSel
	if s.Enabled, err = boolEnv("FACTCHECK_SECOND_PASS"); err != nil {
		return SecondPass{}, err
	}
	s.APIKey = getenv("FACTCHECK_SECOND_PASS_API_KEY", "")
	s.Model = getenv("FACTCHECK_SECOND_PASS_MODEL", llmSel.secondPassModel())
	if s.BandLo, err = floatEnv("FACTCHECK_SECOND_PASS_BAND_LO", s.BandLo); err != nil {
		return SecondPass{}, err
	}
	if s.BandHi, err = floatEnv("FACTCHECK_SECOND_PASS_BAND_HI", s.BandHi); err != nil {
		return SecondPass{}, err
	}
	if s.BandLo > s.BandHi {
		return SecondPass{}, fmt.Errorf("config: FACTCHECK_SECOND_PASS band low %v is above high %v", s.BandLo, s.BandHi)
	}
	if s.Deadline, err = positiveDurationEnv("FACTCHECK_SECOND_PASS_DEADLINE", s.Deadline); err != nil {
		return SecondPass{}, err
	}
	return s, nil
}

// FinalGate configures the terminal reasoning gate (VER-192), the evolution of the
// mid-band second pass into a last-resort adjudicator. Where the second pass took a
// mid-confidence evidence verdict for a deeper second opinion, the terminal gate runs
// only when the pipeline's best verdict is WEAK - unverifiable, or confidence below
// TriggerBelow - and adopts the reasoner's re-judgment only when it is grounded AND
// reaches MinConfidence, otherwise leaving the pipeline's honest weak verdict in place.
// Its provider is decoupled from the shared LLM_PROVIDER (FACTCHECK_FINAL_GATE_PROVIDER)
// and falls back to the second pass's settings, so the expensive reasoner can run on a
// different backend than the hot-path stages. Enabled gates the feature; APIKey is the
// reasoner's Anthropic per-stage secret (env only, never logged); Model selects the
// reasoning tier; Deadline bounds one reasoning call.
type FinalGate struct {
	LLMSelection
	Enabled       bool
	APIKey        string
	Model         string
	TriggerBelow  float64
	MinConfidence float64
	Deadline      time.Duration
}

// Active reports whether the terminal gate should be wired: it is enabled and has the
// key its reasoner needs under the gate's (possibly overridden) provider. Wiring keys
// off this so an enabled-but-keyless configuration degrades to the single-pass verify
// path rather than failing to start, matching every other optional LLM stage.
func (g FinalGate) Active() bool {
	return g.Enabled && g.hasKey(g.APIKey)
}

// LoadFinalGate reads the terminal-gate configuration from the environment, layered
// over the already-loaded second pass so every knob falls back to its FACTCHECK_SECOND_PASS_*
// equivalent: an operator who only set the second pass keeps a working gate, and the
// FACTCHECK_FINAL_GATE_* knobs override piece by piece. It fails fast on an out-of-range
// threshold or a non-positive deadline, and - only when the gate is actually enabled -
// on an unknown provider override or an active gate with no reasoning model. A disabled
// gate never bricks boot on a bad provider it will never use. The secret is read but
// never logged.
func LoadFinalGate(sp SecondPass) (FinalGate, error) {
	g := FinalGate{
		LLMSelection:  sp.LLMSelection,
		Enabled:       sp.Enabled,
		APIKey:        sp.APIKey,
		Model:         sp.Model,
		TriggerBelow:  sp.BandHi,
		MinConfidence: defaultFinalGateMinConfidence,
		Deadline:      sp.Deadline,
	}
	var err error

	if g.Enabled, err = boolEnvDefault("FACTCHECK_FINAL_GATE", g.Enabled); err != nil {
		return FinalGate{}, err
	}

	// Provider decoupling: FACTCHECK_FINAL_GATE_PROVIDER overrides the shared
	// LLM_PROVIDER for the terminal gate only, so the costly reasoner can run on a
	// different backend than the hot path. Unset -> the second pass's provider. The
	// provider keys stay the global GEMINI_API_KEY/DEEPSEEK_API_KEY.
	provider := strings.ToLower(strings.TrimSpace(getenv("FACTCHECK_FINAL_GATE_PROVIDER", g.Provider)))
	providerChanged := provider != g.Provider
	g.Provider = provider

	g.APIKey = getenv("FACTCHECK_FINAL_GATE_API_KEY", g.APIKey)

	// Model default: keep the operator's second-pass model when the provider is
	// unchanged; when the gate switches providers, the carried model names a tier the
	// new provider does not know, so fall back to that provider's reasoning default
	// (empty for non-DeepSeek).
	modelFallback := g.Model
	if providerChanged {
		modelFallback = g.secondPassModel()
	}
	g.Model = getenv("FACTCHECK_FINAL_GATE_MODEL", modelFallback)

	// Both configuration guards fire only when the gate is Active (enabled AND keyed),
	// so a disabled or keyless gate degrades to off rather than bricking boot for a
	// feature it will never run - the same keyless-degrades-off contract every other
	// optional LLM stage keeps. An unknown provider must not silently proceed, and an
	// active gate with no model would run the provider's cheap default stage model
	// instead of the intended reasoner (Anthropic/Gemini name no reasoning default
	// here), quietly defeating the gate.
	if g.Active() {
		switch g.Provider {
		case LLMProviderDeepSeek, LLMProviderAnthropic, LLMProviderGemini:
		default:
			return FinalGate{}, fmt.Errorf("config: FACTCHECK_FINAL_GATE_PROVIDER %q is not a known provider (want %q, %q, or %q)", g.Provider, LLMProviderDeepSeek, LLMProviderAnthropic, LLMProviderGemini)
		}
		if g.Model == "" {
			return FinalGate{}, fmt.Errorf("config: FACTCHECK_FINAL_GATE_MODEL is required under provider %q (it has no default reasoning model)", g.Provider)
		}
	}

	if g.TriggerBelow, err = floatEnv("FACTCHECK_FINAL_GATE_TRIGGER_BELOW", g.TriggerBelow); err != nil {
		return FinalGate{}, err
	}
	if g.TriggerBelow < 0 || g.TriggerBelow > 1 {
		return FinalGate{}, fmt.Errorf("config: FACTCHECK_FINAL_GATE_TRIGGER_BELOW %v is outside [0, 1]", g.TriggerBelow)
	}
	if g.MinConfidence, err = floatEnv("FACTCHECK_FINAL_GATE_MIN_CONFIDENCE", g.MinConfidence); err != nil {
		return FinalGate{}, err
	}
	if g.MinConfidence < 0 || g.MinConfidence > 1 {
		return FinalGate{}, fmt.Errorf("config: FACTCHECK_FINAL_GATE_MIN_CONFIDENCE %v is outside [0, 1]", g.MinConfidence)
	}
	if g.Deadline, err = positiveDurationEnv("FACTCHECK_FINAL_GATE_DEADLINE", g.Deadline); err != nil {
		return FinalGate{}, err
	}
	return g, nil
}

// CheckWorthiness holds the model stage of the check-worthiness gate. It is
// on by default since the vector-first defaults (VER-230): keyless or switched
// off, the deterministic heuristic alone decides claim-worthiness as before.
// When active, a model judges whether a heuristic-accepted declarative is a
// check-worthy public claim rather than casual small talk. APIKey is a secret
// and comes from the environment only - never logged. Model selects the
// classifier model.
type CheckWorthiness struct {
	LLMSelection
	Enabled bool
	APIKey  string
	Model   string
}

// Active reports whether the model classifier should be wired: it is enabled and
// has the API key it needs. Wiring keys off this so an enabled-but-keyless
// configuration degrades to the heuristic-only gate rather than failing to
// start.
func (c CheckWorthiness) Active() bool {
	return c.Enabled && c.hasKey(c.APIKey)
}

// LoadCheckWorthiness reads the model check-worthiness configuration from the
// environment, applying defaults. The secret is read but never logged.
func LoadCheckWorthiness() (CheckWorthiness, error) {
	c := CheckWorthiness{}
	llmSel, err := loadLLMSelection()
	if err != nil {
		return CheckWorthiness{}, err
	}
	c.LLMSelection = llmSel
	if c.Enabled, err = boolEnvDefault("CHECKWORTHINESS_ENABLED", true); err != nil {
		return CheckWorthiness{}, err
	}
	c.APIKey = getenv("CHECKWORTHINESS_API_KEY", "")
	c.Model = getenv("CHECKWORTHINESS_MODEL", llmSel.defaultModel())
	return c, nil
}

// defaultPoliticalRouterMinResults is the floor below which a routed source's
// result is considered thin and the Router broadens to web search. One
// authoritative passage is enough to keep an authoritative answer; below it the
// open web fills in.
const defaultPoliticalRouterMinResults = 1

// defaultPoliticalCuratedTau is the cosine similarity at or above which a curated
// two-axis political claim is borrowed with no LLM call (its literal verdict,
// manipulation flags, and real source resolve the statement instantly). It mirrors
// the legacy curated-borrow bar (defaultVerifyFastTau, 0.85): a high bar, since a
// borrowed verdict bypasses reasoning.
const defaultPoliticalCuratedTau = 0.85

// Political holds the French/EU political fact-checking mode flag and the routing
// knob the capstone (VER-103) wires behind it. The whole redesign rides
// FACTCHECK_POLITICAL (default on since VER-230, the product's French
// political mode): with the flag off the
// locale is the default English behavior and the verify path (when active) runs its
// credibility-only stage unchanged, and with it on the live LLM stages prompt and
// reason in French, the transcriber biases toward French, and the verify path's
// per-claim stage routes through the political pipeline (classify -> route+retrieve
// -> two-axis verify). RouterMinResults is the thin-result floor below which the
// router broadens to web search.
type Political struct {
	Enabled          bool
	RouterMinResults int
	// CuratedTau is the cosine similarity at or above which the political verify
	// path borrows a curated two-axis claim (literal verdict + manipulation flags +
	// real source) without an LLM call. It is a cosine similarity in [-1, 1].
	CuratedTau float64
	// CuratedMaxAge, when positive, stops the fast-path borrowing a curated
	// verdict checked longer ago than this; the claim runs the normal
	// route+verify path instead. 0 (the default) disables the guard, keeping
	// today's behavior until the age policy is deliberately turned on.
	CuratedMaxAge time.Duration
}

// Active reports whether the political verify path should be wired: the political
// flag is on and the retrieve-then-verify path it layers onto is itself active
// (enabled with an API key). With the flag off, or the verify path inactive, the
// political pipeline is not constructed and the live path behaves exactly as it
// does today (credibility-only verify when the verify path is on, legacy
// gate-and-match when it is off). Wiring keys off this so an enabled-but-verify-off
// configuration degrades gracefully rather than failing to start.
func (p Political) Active(verifyActive bool) bool {
	return p.Enabled && verifyActive
}

// Locale resolves the language the live stages run in: French when the political
// mode is on, the default English locale otherwise. It is the single source the
// transcription session and the LLM-stage adapters read, so they cannot drift
// onto different languages within one run.
func (p Political) Locale() domain.Locale {
	if p.Enabled {
		return domain.LocaleFrench
	}
	return domain.LocaleEnglish
}

// RouterLang is the BCP-47 language threaded onto every routed source query,
// derived from the political locale so the source packs and the live stages cannot
// drift onto different languages within one run.
func (p Political) RouterLang() string {
	return p.Locale().LanguageCode()
}

// LoadPolitical reads the political fact-checking mode flag and routing knob from
// the environment. FACTCHECK_POLITICAL gates the whole feature (default off); an
// unparseable value fails fast rather than silently defaulting.
// FACTCHECK_POLITICAL_ROUTER_MIN_RESULTS overrides the thin-result floor and must
// be positive (a non-positive floor would treat every result as thin and stampede
// the web fallback).
func LoadPolitical() (Political, error) {
	enabled, err := boolEnvDefault("FACTCHECK_POLITICAL", true)
	if err != nil {
		return Political{}, err
	}
	minResults, err := intEnv("FACTCHECK_POLITICAL_ROUTER_MIN_RESULTS", defaultPoliticalRouterMinResults, 1, math.MaxInt32)
	if err != nil {
		return Political{}, err
	}
	curatedTau, err := thresholdEnv("FACTCHECK_POLITICAL_CURATED_TAU", defaultPoliticalCuratedTau)
	if err != nil {
		return Political{}, err
	}
	curatedMaxAge, err := durationEnvAllowZero("FACTCHECK_POLITICAL_CURATED_MAX_AGE", 0)
	if err != nil {
		return Political{}, err
	}
	return Political{Enabled: enabled, RouterMinResults: minResults, CuratedTau: curatedTau, CuratedMaxAge: curatedMaxAge}, nil
}

// CrawlAlerts holds the ingestion-fleet Slack alerting configuration. WebhookURL
// is the Slack incoming-webhook crawl runs announce themselves to; it is a secret
// sourced from the environment only and never logged. Empty disables alerting, so
// local runs without Slack are unaffected.
type CrawlAlerts struct {
	WebhookURL string
	// RunMetricsNamespace, when set, is the CloudWatch namespace a producer emits a
	// per-source RunSuccess metric to on a finished run, so a "no successful run in
	// 24h" alarm can page. Empty disables the metric (local and dev runs stay
	// AWS-free); RUN_METRICS_NAMESPACE sets it.
	RunMetricsNamespace string
}

// Active reports whether crawl alerts should post to Slack: it has a webhook URL.
// The notifier wiring keys off this so an unset URL degrades to a silent no-op
// rather than failing to start.
func (c CrawlAlerts) Active() bool {
	return c.WebhookURL != ""
}

// LoadCrawlAlerts reads the ingestion-fleet Slack alerting configuration from the
// environment. SLACK_WEBHOOK_URL is optional: when unset, alerting is a silent
// no-op. It carries the webhook secret, so it is never logged.
func LoadCrawlAlerts() CrawlAlerts {
	return CrawlAlerts{
		WebhookURL:          os.Getenv("SLACK_WEBHOOK_URL"),
		RunMetricsNamespace: os.Getenv("RUN_METRICS_NAMESPACE"),
	}
}

// Scheduler defaults. Each source defaults DISABLED, so the always-on scheduler
// service idles on a plain `docker compose up` and never starts paid ingestion
// until an operator opts a source in with SCHEDULE_<SOURCE>_ENABLED=true - the
// same cost-safety convention the paid wiki profiles follow. The jitter spreads
// concurrently-due sources to avoid a thundering herd on the shared broker and
// upstream APIs. Per-source cron defaults live in the connector registry, not
// here, so config stays registry-agnostic and a new source adds no knob to this
// file.
const (
	defaultScheduleJitter = 30 * time.Second
	maxScheduleJitter     = time.Hour
)

// ScheduleSpec is one schedulable source's identity, supplied by the connector
// registry: its name, the SCHEDULE_<PREFIX>_* env prefix derived from that name,
// and the default cron the registry declares. Passing it in keeps config free of
// any dependency on the registry while still reading a per-source env knob for
// every registered source.
type ScheduleSpec struct {
	Name        string
	EnvPrefix   string
	DefaultCron string
}

// ScheduleSource is one source's resolved scheduling configuration: whether it is
// enabled and the cron spec it fires on. The cron spec is validated by the
// scheduler at startup, so a malformed spec fails fast.
type ScheduleSource struct {
	Enabled bool
	Cron    string
}

// Schedule holds the ingestion fleet's per-source scheduling configuration keyed
// by source name, plus the shared jitter. It is read from the environment only;
// an invalid cron spec is rejected when the scheduler parses it at startup, and an
// invalid jitter is rejected here.
type Schedule struct {
	Sources map[string]ScheduleSource
	Jitter  time.Duration
}

// Source returns the resolved config for a source name and whether it was among
// the specs LoadSchedule was given.
func (s Schedule) Source(name string) (ScheduleSource, bool) {
	src, ok := s.Sources[name]
	return src, ok
}

// LoadSchedule reads the scheduler configuration for the given registry-supplied
// specs. Each source defaults disabled; SCHEDULE_<PREFIX>_ENABLED=true opts it in
// and SCHEDULE_<PREFIX>_CRON overrides its registry-default cadence. SCHEDULE_JITTER
// bounds the random per-run spread (default 30s, capped at 1h). A bad boolean or
// jitter fails fast; a bad cron spec is caught by the scheduler when it parses the
// registry.
func LoadSchedule(specs []ScheduleSpec) (Schedule, error) {
	jitter, err := boundedDurationEnv("SCHEDULE_JITTER", defaultScheduleJitter, maxScheduleJitter)
	if err != nil {
		return Schedule{}, err
	}

	sources := make(map[string]ScheduleSource, len(specs))
	for _, spec := range specs {
		enabled, err := boolEnv("SCHEDULE_" + spec.EnvPrefix + "_ENABLED")
		if err != nil {
			return Schedule{}, err
		}
		sources[spec.Name] = ScheduleSource{
			Enabled: enabled,
			Cron:    getenv("SCHEDULE_"+spec.EnvPrefix+"_CRON", spec.DefaultCron),
		}
	}
	return Schedule{Sources: sources, Jitter: jitter}, nil
}

// thresholdEnv reads a cosine-similarity threshold, falling back when unset and
// rejecting values (including NaN) outside [-1, 1].
func thresholdEnv(key string, fallback float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	// The inverted comparison also rejects NaN, which ParseFloat accepts and
	// which would otherwise disable the filter entirely.
	if !(v >= -1 && v <= 1) {
		return 0, fmt.Errorf("config: %s %v outside cosine similarity range [-1, 1]", key, v)
	}
	return v, nil
}

// defaultWikiCorpus is the local and CI target; enwiki only matters once the
// stack is deployed with storage sized for it.
const defaultWikiCorpus = "simplewiki"

// wikiCorpusRe matches Wikipedia dump database names ("simplewiki",
// "enwiki", "frwiki", ...). Only "<lang>wiki" Wikipedias are supported: the
// ingestion pipeline builds article URLs as <lang>.wikipedia.org, so other
// Wikimedia projects (wiktionaries, wikidata) and underscore dump names
// ("zh_yuewiki", whose real host hyphenates the lang code) would get dead
// source links.
var wikiCorpusRe = regexp.MustCompile(`^[a-z][a-z0-9]*wiki$`)

// Wiki holds the Wikipedia ingestion configuration.
type Wiki struct {
	Corpus string
}

// LoadWiki reads the Wikipedia sync configuration from the environment.
// WIKI_CORPUS defaults to simplewiki and must look like a Wikimedia dump
// name, since it is interpolated into download URLs.
func LoadWiki() (Wiki, error) {
	corpus := getenv("WIKI_CORPUS", defaultWikiCorpus)
	if !wikiCorpusRe.MatchString(corpus) {
		return Wiki{}, fmt.Errorf("config: WIKI_CORPUS %q is not a valid dump name", corpus)
	}
	return Wiki{Corpus: corpus}, nil
}

// Bulk index-build defaults. 512MB maintenance_work_mem builds the simplewiki
// HNSW in memory and is safe on a small instance - raise it for enwiki - and
// seven parallel workers matches pgvector's index-build guidance. (The embedding
// itself runs on the worker fleet, tuned by EMBED_WORKER_*, so the bulk path no
// longer carries its own batch/concurrency/retry knobs.)
const (
	defaultWikiEmbedMaintenanceWorkMem = "512MB"
	defaultWikiEmbedMaxParallelWorkers = 7
	// maxVoyageInputsPerRequest is Voyage's documented per-request input cap.
	maxVoyageInputsPerRequest = 1000
	// maxVoyageTokensPerRequest is Voyage's documented per-request token ceiling
	// for voyage-4-large (docs.voyageai.com, verified 2026-07); a request over it
	// returns HTTP 400, so a batch's token budget must stay under it.
	maxVoyageTokensPerRequest = 120000
)

// workMemRe matches a Postgres memory size like "512MB" or "2GB". It guards
// the value before it reaches set_config, rejecting typos and anything that is
// not a bare size literal.
var workMemRe = regexp.MustCompile(`^[1-9][0-9]*(kB|MB|GB|TB)$`)

// WikiEmbed holds the bulk index-build configuration. MaintenanceWorkMem and
// MaxParallelWorkers tune the post-load HNSW index build the atomic bulk finalize
// runs; the embedding itself is fanned out to the worker fleet (EMBED_WORKER_*),
// so this no longer carries batch/concurrency/retry/timeout knobs.
type WikiEmbed struct {
	MaintenanceWorkMem string
	MaxParallelWorkers int
}

// LoadWikiEmbed reads the bulk index-build configuration from the environment,
// applying defaults and failing fast on values that would produce invalid
// index-build settings.
func LoadWikiEmbed() (WikiEmbed, error) {
	w := WikiEmbed{
		MaintenanceWorkMem: defaultWikiEmbedMaintenanceWorkMem,
		MaxParallelWorkers: defaultWikiEmbedMaxParallelWorkers,
	}
	var err error
	if w.MaxParallelWorkers, err = intEnv("WIKI_EMBED_MAX_PARALLEL_WORKERS", w.MaxParallelWorkers, 0, math.MaxInt32); err != nil {
		return WikiEmbed{}, err
	}
	if raw := os.Getenv("WIKI_EMBED_MAINTENANCE_WORK_MEM"); raw != "" {
		if !workMemRe.MatchString(raw) {
			return WikiEmbed{}, fmt.Errorf("config: WIKI_EMBED_MAINTENANCE_WORK_MEM %q is not a Postgres memory size like 512MB or 2GB", raw)
		}
		w.MaintenanceWorkMem = raw
	}
	return w, nil
}

// Bulk-enqueue producer defaults. A 1000-row keyset page keeps each staging
// scan cheap while publishing a large corpus. The fleet's progress is polled
// every 5s - frequent enough to be observable, light on the database - and a run
// aborts if the remaining count stands still for 30m: long enough to ride out a
// slow or rate-limited Voyage tier, yet bounded so a dead fleet cannot hang the
// producer forever.
const (
	defaultWikiEnqueueBatchSize  = 1000
	defaultWikiDrainPollInterval = 5 * time.Second
	defaultWikiDrainStallTimeout = 30 * time.Minute
)

// WikiProducer holds the bulk-enqueue producer configuration. EnqueueBatchSize
// is how many staging rows are read per keyset scan while publishing embedding
// jobs; DrainPollInterval is how often the producer polls the remaining count
// while the worker fleet embeds; DrainStallTimeout is how long that count may
// stand still before the run aborts as a stalled fleet (it must be at least the
// poll interval).
type WikiProducer struct {
	EnqueueBatchSize  int
	DrainPollInterval time.Duration
	DrainStallTimeout time.Duration
}

// LoadWikiProducer reads the bulk-enqueue producer configuration from the
// environment, applying defaults and failing fast on values that would loop or
// hang. It carries no secret: the broker URL loads from LoadQueue.
func LoadWikiProducer() (WikiProducer, error) {
	w := WikiProducer{
		EnqueueBatchSize:  defaultWikiEnqueueBatchSize,
		DrainPollInterval: defaultWikiDrainPollInterval,
		DrainStallTimeout: defaultWikiDrainStallTimeout,
	}
	var err error
	if w.EnqueueBatchSize, err = intEnv("WIKI_ENQUEUE_BATCH_SIZE", w.EnqueueBatchSize, 1, math.MaxInt32); err != nil {
		return WikiProducer{}, err
	}
	if w.DrainPollInterval, err = positiveDurationEnv("WIKI_DRAIN_POLL_INTERVAL", w.DrainPollInterval); err != nil {
		return WikiProducer{}, err
	}
	if w.DrainStallTimeout, err = positiveDurationEnv("WIKI_DRAIN_STALL_TIMEOUT", w.DrainStallTimeout); err != nil {
		return WikiProducer{}, err
	}
	if w.DrainStallTimeout < w.DrainPollInterval {
		return WikiProducer{}, fmt.Errorf("config: WIKI_DRAIN_STALL_TIMEOUT %s must be at least WIKI_DRAIN_POLL_INTERVAL %s", w.DrainStallTimeout, w.DrainPollInterval)
	}
	return w, nil
}

// Clustering-job defaults. K is the number of topic clusters - 64 gives a useful
// spread over a simplewiki-sized corpus while keeping each Lloyd iteration
// (O(n*k*dim)) tractable; raise it for a larger corpus. 20 iterations is well
// past where spherical k-means converges on embedding data. The seed is fixed so
// re-running the job over an unchanged corpus reproduces the same clustering (the
// idempotency the batch step needs); change it only to explore a different
// initialization. Read pages of 5000 chunks balance round-trips against memory;
// writes go back 1000 rows per batch.
const (
	defaultWikiClusterK          = 64
	defaultWikiClusterMaxIters   = 20
	defaultWikiClusterSeed       = 1
	defaultWikiClusterReadBatch  = 5000
	defaultWikiClusterWriteBatch = 1000
)

// WikiCluster holds the offline clustering-job configuration. K is the cluster
// count and MaxIters the Lloyd iteration cap; Seed makes the k-means++
// initialization deterministic so re-runs are idempotent; ReadBatch and
// WriteBatch size the keyset read of embeddings and the batched write-back of
// cluster ids and importance scores.
type WikiCluster struct {
	K          int
	MaxIters   int
	Seed       uint64
	ReadBatch  int
	WriteBatch int
}

// LoadWikiCluster reads the clustering-job configuration from the environment,
// applying defaults and failing fast on out-of-range values. It carries no
// secret: the job reads embeddings from the database and writes scores back.
func LoadWikiCluster() (WikiCluster, error) {
	w := WikiCluster{
		K:          defaultWikiClusterK,
		MaxIters:   defaultWikiClusterMaxIters,
		Seed:       defaultWikiClusterSeed,
		ReadBatch:  defaultWikiClusterReadBatch,
		WriteBatch: defaultWikiClusterWriteBatch,
	}
	var err error
	if w.K, err = intEnv("WIKI_CLUSTER_K", w.K, 1, math.MaxInt32); err != nil {
		return WikiCluster{}, err
	}
	if w.MaxIters, err = intEnv("WIKI_CLUSTER_MAX_ITERS", w.MaxIters, 1, math.MaxInt32); err != nil {
		return WikiCluster{}, err
	}
	seed, err := intEnv("WIKI_CLUSTER_SEED", defaultWikiClusterSeed, 0, math.MaxInt32)
	if err != nil {
		return WikiCluster{}, err
	}
	w.Seed = uint64(seed)
	if w.ReadBatch, err = intEnv("WIKI_CLUSTER_READ_BATCH", w.ReadBatch, 1, math.MaxInt32); err != nil {
		return WikiCluster{}, err
	}
	if w.WriteBatch, err = intEnv("WIKI_CLUSTER_WRITE_BATCH", w.WriteBatch, 1, math.MaxInt32); err != nil {
		return WikiCluster{}, err
	}
	return w, nil
}

// Storage presign defaults: a 15-minute upload window is long enough for a
// browser PUT of a large video yet short enough to limit a leaked URL, and a
// 1-hour playback window covers a viewing session without constant re-signing.
// The eu-west-3 default matches the project's deployment region.
const (
	defaultStoragePutTTL = 15 * time.Minute
	defaultStorageGetTTL = time.Hour
	defaultStorageRegion = "eu-west-3"
	// maxStoragePresignTTL is the SigV4 ceiling for a presigned URL (7 days).
	maxStoragePresignTTL = 7 * 24 * time.Hour
)

// Storage holds the object-storage configuration for uploaded media. An empty
// Endpoint targets real AWS S3 and resolves credentials through the default
// chain (the ECS task role in production); a non-empty Endpoint with static
// credentials and UsePathStyle targets a MinIO container in local development.
// PublicEndpoint, when set, is the browser-facing host the presigned upload and
// playback URLs are signed against; it only differs from Endpoint in local
// development, where the backend reaches MinIO at a Docker hostname the browser
// cannot resolve.
type Storage struct {
	Endpoint       string
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	UsePathStyle   bool
	PutTTL         time.Duration
	GetTTL         time.Duration
}

// LoadStorage reads the object-storage configuration from the environment.
// STORAGE_BUCKET is required; the region defaults to eu-west-3 and the presign
// TTLs to 15m (upload) and 1h (playback). Static credentials are all-or-nothing
// so a half-set pair fails fast rather than silently falling back to the
// default credential chain.
func LoadStorage() (Storage, error) {
	bucket, err := requireEnv("STORAGE_BUCKET")
	if err != nil {
		return Storage{}, err
	}
	s := Storage{
		Endpoint:       os.Getenv("STORAGE_ENDPOINT"),
		PublicEndpoint: os.Getenv("STORAGE_PUBLIC_ENDPOINT"),
		Region:         getenv("STORAGE_REGION", defaultStorageRegion),
		Bucket:         bucket,
		AccessKey:      os.Getenv("STORAGE_ACCESS_KEY"),
		SecretKey:      os.Getenv("STORAGE_SECRET_KEY"),
		PutTTL:         defaultStoragePutTTL,
		GetTTL:         defaultStorageGetTTL,
	}
	if (s.AccessKey == "") != (s.SecretKey == "") {
		return Storage{}, errors.New("config: STORAGE_ACCESS_KEY and STORAGE_SECRET_KEY must be set together")
	}
	if s.UsePathStyle, err = boolEnv("STORAGE_USE_PATH_STYLE"); err != nil {
		return Storage{}, err
	}
	if s.PutTTL, err = boundedDurationEnv("STORAGE_PRESIGN_PUT_TTL", s.PutTTL, maxStoragePresignTTL); err != nil {
		return Storage{}, err
	}
	if s.GetTTL, err = boundedDurationEnv("STORAGE_PRESIGN_GET_TTL", s.GetTTL, maxStoragePresignTTL); err != nil {
		return Storage{}, err
	}
	return s, nil
}

// defaultUploadMaxBytes caps a declared upload size at 2 GiB: large enough for a
// long clip, small enough to reject a runaway declaration before any object is
// stored.
const defaultUploadMaxBytes int64 = 2 << 30

// Upload holds the video-upload constraints applied by the upload API.
type Upload struct {
	MaxBytes int64
}

// LoadUpload reads the upload configuration from the environment. UPLOAD_MAX_BYTES
// overrides the 2 GiB default and must be a positive integer.
func LoadUpload() (Upload, error) {
	maxBytes := defaultUploadMaxBytes
	if raw := os.Getenv("UPLOAD_MAX_BYTES"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Upload{}, fmt.Errorf("config: UPLOAD_MAX_BYTES %q: %w", raw, err)
		}
		if v <= 0 {
			return Upload{}, fmt.Errorf("config: UPLOAD_MAX_BYTES must be positive, got %d", v)
		}
		maxBytes = v
	}
	return Upload{MaxBytes: maxBytes}, nil
}

// Document API defaults: a 30 MB PDF covers long reports while bounding
// storage and abuse; 1500 sentences bounds the LLM cost of one analysis run; a
// 30-minute analysis timeout bounds one whole run (most sentences are gated out,
// so real runs are far shorter, but a full check-worthy document under the
// verify pool can take many minutes).
const (
	defaultDocumentMaxSizeBytes    int64 = 30 << 20
	defaultDocumentMaxSentences          = 1500
	defaultDocumentAnalysisTimeout       = 30 * time.Minute
)

// Documents holds the PDF document constraints applied by the documents API.
type Documents struct {
	MaxSizeBytes    int64
	MaxSentences    int
	AnalysisTimeout time.Duration
}

// LoadDocuments reads the document configuration from the environment.
// DOCUMENT_MAX_SIZE_BYTES and DOCUMENT_MAX_SENTENCES override the defaults and
// must be positive integers; DOCUMENT_ANALYSIS_TIMEOUT overrides the run timeout
// and must be a positive duration.
func LoadDocuments() (Documents, error) {
	cfg := Documents{
		MaxSizeBytes:    defaultDocumentMaxSizeBytes,
		MaxSentences:    defaultDocumentMaxSentences,
		AnalysisTimeout: defaultDocumentAnalysisTimeout,
	}
	if raw := os.Getenv("DOCUMENT_MAX_SIZE_BYTES"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Documents{}, fmt.Errorf("config: DOCUMENT_MAX_SIZE_BYTES %q: %w", raw, err)
		}
		if v <= 0 {
			return Documents{}, fmt.Errorf("config: DOCUMENT_MAX_SIZE_BYTES must be positive, got %d", v)
		}
		cfg.MaxSizeBytes = v
	}
	if raw := os.Getenv("DOCUMENT_MAX_SENTENCES"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Documents{}, fmt.Errorf("config: DOCUMENT_MAX_SENTENCES %q: %w", raw, err)
		}
		if v <= 0 {
			return Documents{}, fmt.Errorf("config: DOCUMENT_MAX_SENTENCES must be positive, got %d", v)
		}
		cfg.MaxSentences = v
	}
	if raw := os.Getenv("DOCUMENT_ANALYSIS_TIMEOUT"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			return Documents{}, fmt.Errorf("config: DOCUMENT_ANALYSIS_TIMEOUT %q: %w", raw, err)
		}
		if v <= 0 {
			return Documents{}, fmt.Errorf("config: DOCUMENT_ANALYSIS_TIMEOUT must be positive, got %s", v)
		}
		cfg.AnalysisTimeout = v
	}
	return cfg, nil
}

// YouTube ingest defaults: a 2 GiB download cap matches the upload cap; a
// 15-minute timeout covers a long clip over a slow connection without letting a
// stuck download pin a worker indefinitely; the downloader defaults to yt-dlp on
// PATH.
const (
	defaultYouTubeMaxBytes int64 = 2 << 30
	defaultYouTubeTimeout        = 15 * time.Minute
	defaultYouTubeBinary         = "yt-dlp"
)

// YouTube holds the YouTube ingest configuration. BinaryPath is the yt-dlp
// executable; MaxBytes bounds a single download; Timeout bounds the whole
// download-upload run.
type YouTube struct {
	BinaryPath string
	MaxBytes   int64
	Timeout    time.Duration
}

// LoadYouTube reads the YouTube ingest configuration from the environment,
// applying defaults and failing fast on a non-positive size bound or timeout.
func LoadYouTube() (YouTube, error) {
	y := YouTube{
		BinaryPath: getenv("YOUTUBE_DOWNLOADER_PATH", defaultYouTubeBinary),
		MaxBytes:   defaultYouTubeMaxBytes,
		Timeout:    defaultYouTubeTimeout,
	}
	if raw := os.Getenv("YOUTUBE_MAX_DOWNLOAD_BYTES"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return YouTube{}, fmt.Errorf("config: YOUTUBE_MAX_DOWNLOAD_BYTES %q: %w", raw, err)
		}
		if v <= 0 {
			return YouTube{}, fmt.Errorf("config: YOUTUBE_MAX_DOWNLOAD_BYTES must be positive, got %d", v)
		}
		y.MaxBytes = v
	}
	var err error
	if y.Timeout, err = positiveDurationEnv("YOUTUBE_DOWNLOAD_TIMEOUT", y.Timeout); err != nil {
		return YouTube{}, err
	}
	return y, nil
}

// Queue defaults. The queue name is shared by the producer and the worker fleet.
// A max priority of 10 gives enough distinct bands for the importance mapping a
// later card introduces without the per-message memory a 255-band queue costs. A
// prefetch of 1 gives the fleet fair dispatch: the broker hands a worker one
// unacknowledged message at a time, so a slow worker does not hoard a backlog.
const (
	defaultQueueName        = "embedding.jobs"
	defaultQueueMaxPriority = 10
	defaultQueuePrefetch    = 1
	// defaultQueueVersion is the single active version when none is configured. It
	// is 2 because dead-letter routing (VER-188) added x-dead-letter-* arguments to
	// every queue: those arguments are fixed at declaration and cannot be changed on
	// a live queue, so declaring the pre-existing v1 queue with them would fail a 406
	// PRECONDITION_FAILED and crash the fleet. Bumping the version declares fresh
	// v2 queues (embedding.jobs.v2, ...) with the dead-letter topology and leaves the
	// old v1 queues untouched. A worker consumes only the active (newest) version, so
	// any messages still queued under v1 at cutover are not auto-drained; an operator
	// with a v1 backlog drains it with a one-off consumer against the old queue name
	// before removing it (fresh environments and empty queues need nothing).
	defaultQueueVersion = "2"
	// defaultCrawlQueueName is the base queue the category crawler publishes to,
	// kept separate from the embedding-jobs queue so the crawl worker and the
	// dump-pipeline fleet never consume each other's messages.
	defaultCrawlQueueName = "crawl.chunks"
	// defaultFactCheckQueueName is the base queue the fact-check-archive producer
	// publishes curated-claim jobs to, kept separate from the wiki crawl and
	// embedding queues so the fact-check worker never consumes a wiki chunk.
	defaultFactCheckQueueName = "factcheck.claims"
	// defaultScrutinsQueueName is the base queue the scrutins-archive producer
	// publishes per-scrutin jobs to, kept separate from every other queue so the
	// scrutins worker never consumes a wiki chunk or curated claim.
	defaultScrutinsQueueName = "scrutins.votes"
	// defaultQueueDLQEnabled routes a rejected message to a dead-letter queue by
	// default, so an unprocessable message is parked and inspectable rather than
	// silently discarded.
	defaultQueueDLQEnabled = true
	// defaultQueueMinBackoff and defaultQueueMaxBackoff bound the transport redial
	// wait after a broker drop; the transport applies these same defaults when a
	// value is zero, and they are surfaced here so the knobs have a documented home.
	defaultQueueMinBackoff = 250 * time.Millisecond
	defaultQueueMaxBackoff = 30 * time.Second
)

// Queue holds the RabbitMQ embedding-job queue configuration. URL is required
// and carries the broker credentials, so it is sourced from the environment
// only and never logged. MaxPriority sets the queue's x-max-priority ceiling and
// Prefetch the per-consumer unacknowledged-message limit (0 leaves it unbounded).
//
// Versions drives the versioned-queue convention: every queue name embeds an
// explicit version (Name.v<version>) so the system can roll to a new message
// schema by draining the old version while a new one fills, without losing
// messages. The list is ordered oldest-first; Version is the active (newest)
// version that the producer publishes to and a worker consumes, and KnownVersions
// is every configured version so a worker can reject a message stamped with a
// version it does not understand rather than mis-process it. Adding a version is a
// configuration change, no new transport code.
type Queue struct {
	URL           string
	Name          string
	MaxPriority   uint8
	Prefetch      int
	Version       string
	KnownVersions []string

	// DLQEnabled routes a rejected message to a companion dead-letter queue
	// instead of discarding it. It defaults on and must hold the same value for
	// every process that declares the queue, or the declarations conflict; one
	// environment value forwarded to producers and consumers keeps them in step.
	// ReconnectMinBackoff and ReconnectMaxBackoff bound the transport's redial
	// wait after a broker drop.
	DLQEnabled          bool
	ReconnectMinBackoff time.Duration
	ReconnectMaxBackoff time.Duration
}

// VersionedName is the broker queue name for the active version: the base name
// with an explicit .v<version> suffix. Producer and worker both resolve it from
// the same configuration, so they bind to the same queue.
func (q Queue) VersionedName() string {
	return q.Name + ".v" + q.Version
}

// ClientConfig builds the transport-layer queue.Config for this queue, binding to
// the active versioned name and carrying the resilience knobs (DLQ routing,
// reconnect backoff bounds) so every producer and consumer declares an identical
// topology. Prefetch is passed per call because a consumer sizes it to its own
// concurrency while a producer does not consume; zero leaves it unbounded.
func (q Queue) ClientConfig(prefetch int) queue.Config {
	return queue.Config{
		URL:         q.URL,
		QueueName:   q.VersionedName(),
		Version:     q.Version,
		MaxPriority: q.MaxPriority,
		Prefetch:    prefetch,
		DisableDLQ:  !q.DLQEnabled,
		MinBackoff:  q.ReconnectMinBackoff,
		MaxBackoff:  q.ReconnectMaxBackoff,
	}
}

// LoadQueue reads the broker configuration from the environment. RABBITMQ_URL is
// required; the queue name defaults to embedding.jobs, the max priority to 10
// (validated to [1, 255]) and the prefetch to 1 (validated non-negative). The
// versions default to a single "2"; RABBITMQ_QUEUE_VERSIONS overrides them as a
// comma-separated, oldest-first list, with the newest taken as the active
// version. Bad values fail fast at startup rather than surfacing as a broker
// error later.
func LoadQueue() (Queue, error) {
	return loadQueue("RABBITMQ_QUEUE", defaultQueueName)
}

// LoadCrawlQueue reads the category-crawl broker configuration. It shares the
// broker URL, priority, prefetch, and version machinery with LoadQueue but binds
// to its own base queue name (RABBITMQ_CRAWL_QUEUE, default crawl.chunks), so the
// crawl producer and worker never share a queue with the dump-pipeline fleet.
func LoadCrawlQueue() (Queue, error) {
	return loadQueue("RABBITMQ_CRAWL_QUEUE", defaultCrawlQueueName)
}

// LoadFactCheckQueue reads the fact-check-archive broker configuration. It shares
// the broker URL, priority, prefetch, and version machinery with LoadQueue but
// binds to its own base queue name (RABBITMQ_FACTCHECK_QUEUE, default
// factcheck.claims), so the fact-check producer and worker never share a queue
// with the wiki crawl or dump-pipeline fleets.
func LoadFactCheckQueue() (Queue, error) {
	return loadQueue("RABBITMQ_FACTCHECK_QUEUE", defaultFactCheckQueueName)
}

// LoadScrutinsQueue reads the scrutins-archive broker configuration. It shares
// the broker URL, priority, prefetch, and version machinery with LoadQueue but
// binds to its own base queue name (RABBITMQ_SCRUTINS_QUEUE, default
// scrutins.votes), so the scrutins producer and worker never share a queue with
// the wiki crawl, fact-check, or dump-pipeline fleets.
func LoadScrutinsQueue() (Queue, error) {
	return loadQueue("RABBITMQ_SCRUTINS_QUEUE", defaultScrutinsQueueName)
}

// loadQueue reads the broker configuration, taking the base queue name from
// nameEnv (defaulting to nameDefault) so the embedding and crawl queues share one
// loader yet bind to distinct queues.
func loadQueue(nameEnv, nameDefault string) (Queue, error) {
	url, err := requireEnv("RABBITMQ_URL")
	if err != nil {
		return Queue{}, err
	}
	// A message priority is a single byte, so the queue's x-max-priority ceiling
	// is math.MaxUint8.
	maxPriority, err := intEnv("RABBITMQ_MAX_PRIORITY", defaultQueueMaxPriority, 1, math.MaxUint8)
	if err != nil {
		return Queue{}, err
	}
	// The AMQP prefetch_count field is a uint16; cap the value here rather than
	// let a larger number silently truncate when the consumer sets QoS.
	prefetch, err := intEnv("RABBITMQ_PREFETCH", defaultQueuePrefetch, 0, math.MaxUint16)
	if err != nil {
		return Queue{}, err
	}
	versions, err := queueVersions(getenv("RABBITMQ_QUEUE_VERSIONS", defaultQueueVersion))
	if err != nil {
		return Queue{}, err
	}
	dlqEnabled, err := boolEnvDefault("RABBITMQ_DLQ_ENABLED", defaultQueueDLQEnabled)
	if err != nil {
		return Queue{}, err
	}
	minBackoff, err := positiveDurationEnv("RABBITMQ_RECONNECT_MIN_BACKOFF", defaultQueueMinBackoff)
	if err != nil {
		return Queue{}, err
	}
	maxBackoff, err := positiveDurationEnv("RABBITMQ_RECONNECT_MAX_BACKOFF", defaultQueueMaxBackoff)
	if err != nil {
		return Queue{}, err
	}
	if maxBackoff < minBackoff {
		return Queue{}, fmt.Errorf("config: RABBITMQ_RECONNECT_MAX_BACKOFF (%s) must be at least RABBITMQ_RECONNECT_MIN_BACKOFF (%s)", maxBackoff, minBackoff)
	}
	return Queue{
		URL:                 url,
		Name:                getenv(nameEnv, nameDefault),
		MaxPriority:         uint8(maxPriority),
		Prefetch:            prefetch,
		Version:             versions[len(versions)-1],
		KnownVersions:       versions,
		DLQEnabled:          dlqEnabled,
		ReconnectMinBackoff: minBackoff,
		ReconnectMaxBackoff: maxBackoff,
	}, nil
}

// queueVersions parses the comma-separated version list, oldest-first. Each token
// is a queue-name suffix, so it must be a non-empty run of letters, digits,
// underscores, or dashes (no dot, which separates the suffix from the base name);
// duplicates are an operator mistake worth failing on rather than silently
// collapsing. The list must be non-empty.
func queueVersions(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	versions := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			return nil, fmt.Errorf("config: RABBITMQ_QUEUE_VERSIONS has an empty version in %q", raw)
		}
		if !isQueueVersion(v) {
			return nil, fmt.Errorf("config: RABBITMQ_QUEUE_VERSIONS version %q must be letters, digits, '_' or '-'", v)
		}
		if _, dup := seen[v]; dup {
			return nil, fmt.Errorf("config: RABBITMQ_QUEUE_VERSIONS has duplicate version %q", v)
		}
		seen[v] = struct{}{}
		versions = append(versions, v)
	}
	return versions, nil
}

func isQueueVersion(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Embedding-worker defaults. A worker embeds one chunk per job, so a 30s HTTP
// timeout is ample and a concurrency of 4 keeps a single replica well under
// Voyage's per-key limits while the fleet scales by replica count. A delivery is
// tried five times before it is dropped with a log, and the embedder retries a
// transient Voyage 429 or timeout six times within each of those attempts.
const (
	defaultEmbedWorkerConcurrency       = 4
	defaultEmbedWorkerBatchSize         = 128
	defaultEmbedWorkerBatchWait         = 200 * time.Millisecond
	defaultEmbedWorkerMaxAttempts       = 5
	defaultEmbedWorkerHTTPTimeout       = 30 * time.Second
	defaultEmbedWorkerRequestsPerMinute = 0
	defaultEmbedWorkerEmbedMaxRetries   = 6
	// defaultEmbedWorkerMaxBatchTokens packs each Voyage call to 80% of the
	// per-request token ceiling for voyage-4-large, leaving headroom for the
	// worker's character-based token estimate to under-count.
	defaultEmbedWorkerMaxBatchTokens = maxVoyageTokensPerRequest * 8 / 10
)

// EmbedWorker holds the embedding-worker configuration. Concurrency bounds the
// batches one replica embeds in parallel; BatchSize is the most chunks embedded
// in one Voyage call (capped at Voyage's per-request input limit) and BatchWait
// bounds how long a partial batch waits for more before it is sent anyway, so a
// quiet queue still drains; MaxAttempts is the per-job delivery budget before a
// persistent failure is dropped with a log; HTTPTimeout bounds each Voyage
// request; RequestsPerMinute optionally paces outbound requests onto a tier's
// budget (0 = unpaced); EmbedMaxRetries is the embedder's internal retry count
// for a transient Voyage 429 or network timeout.
// MaxBatchTokens caps a batch's estimated token count so a provider call stays
// under Voyage's per-request token ceiling; an over-budget batch is split before
// the call rather than failing it.
type EmbedWorker struct {
	Concurrency       int
	BatchSize         int
	BatchWait         time.Duration
	MaxAttempts       int
	MaxBatchTokens    int
	HTTPTimeout       time.Duration
	RequestsPerMinute int
	EmbedMaxRetries   int
}

// LoadEmbedWorker reads the embedding-worker configuration from the environment,
// applying defaults and failing fast on out-of-range values. It carries no
// secret: the broker URL and embedding key load from LoadQueue and LoadEmbedding.
func LoadEmbedWorker() (EmbedWorker, error) {
	w, err := loadWorkerCommon("EMBED_WORKER", defaultWorker())
	if err != nil {
		return EmbedWorker{}, err
	}
	// Capped at Voyage's per-request input limit so a batch never exceeds what the
	// provider accepts in one call. BatchSize/BatchWait are the embedding worker's
	// alone - the crawl worker embeds one chunk per job and ignores them.
	if w.BatchSize, err = intEnv("EMBED_WORKER_BATCH_SIZE", w.BatchSize, 1, maxVoyageInputsPerRequest); err != nil {
		return EmbedWorker{}, err
	}
	if w.BatchWait, err = positiveDurationEnv("EMBED_WORKER_BATCH_WAIT", w.BatchWait); err != nil {
		return EmbedWorker{}, err
	}
	// Capped at Voyage's per-request token ceiling so a packed batch never exceeds
	// what the provider accepts in one call; the worker splits an over-budget batch
	// before sending it.
	if w.MaxBatchTokens, err = intEnv("EMBED_WORKER_MAX_BATCH_TOKENS", w.MaxBatchTokens, 1, maxVoyageTokensPerRequest); err != nil {
		return EmbedWorker{}, err
	}
	return w, nil
}

// LoadCrawlWorker reads the crawl-worker configuration (CRAWL_WORKER_*). It
// reuses the EmbedWorker shape and shared defaults; the crawl worker embeds one
// chunk per job, so it never reads BatchSize/BatchWait, which keep their
// defaults. Only the env prefix differs from LoadEmbedWorker.
func LoadCrawlWorker() (EmbedWorker, error) {
	return loadWorkerCommon("CRAWL_WORKER", defaultWorker())
}

// LoadScrutinsWorker reads the scrutins-worker configuration (SCRUTINS_WORKER_*).
// It reuses the EmbedWorker shape and shared defaults for concurrency, attempts,
// and pacing; the scrutins worker parses and upserts a scrutin per job and never
// embeds, so it reads only Concurrency and MaxAttempts (the HTTP-timeout and
// embed-retry fields keep their defaults and are unused). Only the env prefix
// differs from LoadEmbedWorker.
func LoadScrutinsWorker() (EmbedWorker, error) {
	return loadWorkerCommon("SCRUTINS_WORKER", defaultWorker())
}

// maxWorkerIdleTimeout caps the drain-to-idle window so a typo cannot pin a
// consumer host up for an absurd stretch, billing while it waits to idle out.
const maxWorkerIdleTimeout = 24 * time.Hour

// LoadWorkerIdle reads the shared consumer drain-to-idle window from
// WORKER_IDLE_TIMEOUT. Zero - the default, and the local default - disables idle
// exit: a worker runs until SIGTERM, exactly as before. A positive value turns on
// drain-to-idle: a worker whose queue yields no delivery for the window exits
// cleanly (reporting what it drained through the existing consumer stop alert),
// which the cloud consumer host keys its self-stop on once every worker has idled
// out. The window is capped so an over-long value cannot strand an idle host
// running.
func LoadWorkerIdle() (time.Duration, error) {
	idle, err := durationEnvAllowZero("WORKER_IDLE_TIMEOUT", 0)
	if err != nil {
		return 0, err
	}
	if idle > maxWorkerIdleTimeout {
		return 0, fmt.Errorf("config: WORKER_IDLE_TIMEOUT %s exceeds the maximum %s", idle, maxWorkerIdleTimeout)
	}
	return idle, nil
}

// defaultWorker is the worker configuration before any environment override.
func defaultWorker() EmbedWorker {
	return EmbedWorker{
		Concurrency:       defaultEmbedWorkerConcurrency,
		BatchSize:         defaultEmbedWorkerBatchSize,
		MaxBatchTokens:    defaultEmbedWorkerMaxBatchTokens,
		BatchWait:         defaultEmbedWorkerBatchWait,
		MaxAttempts:       defaultEmbedWorkerMaxAttempts,
		HTTPTimeout:       defaultEmbedWorkerHTTPTimeout,
		RequestsPerMinute: defaultEmbedWorkerRequestsPerMinute,
		EmbedMaxRetries:   defaultEmbedWorkerEmbedMaxRetries,
	}
}

// loadWorkerCommon reads the worker fields shared by the embedding and crawl
// workers (concurrency, attempts, HTTP timeout, RPM pacing, embedder retries)
// under the given env prefix, applying the defaults in w and failing fast on an
// out-of-range value.
func loadWorkerCommon(prefix string, w EmbedWorker) (EmbedWorker, error) {
	var err error
	if w.Concurrency, err = intEnv(prefix+"_CONCURRENCY", w.Concurrency, 1, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	if w.MaxAttempts, err = intEnv(prefix+"_MAX_ATTEMPTS", w.MaxAttempts, 1, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	if w.HTTPTimeout, err = positiveDurationEnv(prefix+"_HTTP_TIMEOUT", w.HTTPTimeout); err != nil {
		return EmbedWorker{}, err
	}
	// 0 disables pacing; a positive value caps outbound requests per minute so a
	// constrained tier is not overrun.
	if w.RequestsPerMinute, err = intEnv(prefix+"_RPM", w.RequestsPerMinute, 0, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	if w.EmbedMaxRetries, err = intEnv(prefix+"_EMBED_MAX_RETRIES", w.EmbedMaxRetries, 1, math.MaxInt32); err != nil {
		return EmbedWorker{}, err
	}
	return w, nil
}

// boolEnv reads an optional boolean feature flag, defaulting to false when
// unset. Every such flag in this config is opt-in, so a false default is the
// shared contract rather than a per-caller choice.
func boolEnv(key string) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	return v, nil
}

// boolEnvDefault reads a boolean environment variable, returning fallback when
// unset. It exists for flags that default to true, where boolEnv's unset-is-false
// rule would invert the intended default.
func boolEnvDefault(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	return v, nil
}

// boundedDurationEnv reads a positive Go duration, applying fallback when unset
// and rejecting values above maximum.
func boundedDurationEnv(key string, fallback, maximum time.Duration) (time.Duration, error) {
	d, err := positiveDurationEnv(key, fallback)
	if err != nil {
		return 0, err
	}
	if d > maximum {
		return 0, fmt.Errorf("config: %s %s exceeds the maximum %s", key, d, maximum)
	}
	return d, nil
}

// Delta-sync defaults. RecentChanges retains 30 days on Wikimedia wikis
// ($wgRCMaxAge), so a checkpoint older than that cannot be caught up
// incrementally and the run must fall back to a bulk re-ingest; the retention
// window is the hard ceiling. BulkFraction is the share of the corpus that, once
// exceeded by one window's change set, makes a bulk re-run (which rebuilds the
// HNSW index from scratch) preferable to incremental inserts.
const (
	defaultWikiDeltaRetentionDays = 30
	maxWikiDeltaRetentionDays     = 30
	defaultWikiDeltaBulkFraction  = 0.25
)

// WikiDelta holds the periodic delta-sync configuration. RetentionDays bounds
// how stale a checkpoint may be before delta is refused; BulkFraction is the
// change-set share of the corpus above which a bulk re-run is recommended.
type WikiDelta struct {
	RetentionDays int
	BulkFraction  float64
}

// LoadWikiDelta reads the delta-sync configuration from the environment,
// applying defaults and failing fast on out-of-range values. RetentionDays is
// capped at the API's 30-day RecentChanges window; BulkFraction is a [0,1]
// share of the corpus.
func LoadWikiDelta() (WikiDelta, error) {
	w := WikiDelta{RetentionDays: defaultWikiDeltaRetentionDays, BulkFraction: defaultWikiDeltaBulkFraction}
	var err error
	if w.RetentionDays, err = intEnv("WIKI_DELTA_RETENTION_DAYS", w.RetentionDays, 1, maxWikiDeltaRetentionDays); err != nil {
		return WikiDelta{}, err
	}
	if w.BulkFraction, err = floatEnv("WIKI_DELTA_BULK_FRACTION", w.BulkFraction); err != nil {
		return WikiDelta{}, err
	}
	return w, nil
}

// Crawl-ingestion defaults: crawl one subcategory level deep, cap at a few
// thousand pages so an unattended run is bounded, and ingest body prose by
// default since lead-only is the explicit opt-out.
const (
	defaultCrawlMaxDepth = 1
	defaultCrawlMaxPages = 5000
	// defaultCrawlCheckpointPath is the state file a crawl records resolved pages
	// in so a rerun resumes; it sits on the same /state volume as the scrutins
	// marker. It may be set empty to disable resume.
	defaultCrawlCheckpointPath = "/state/crawl-checkpoint.json"
	// defaultCrawlErrorBudget is how many pages a run may skip (an extract or
	// publish failure) before it fails, so a single bad page or transient blip does
	// not discard a whole crawl.
	defaultCrawlErrorBudget = 50
)

// Crawl configures the category crawler. Categories are the seed category titles
// (e.g. "Category:Physics"); Project is the wiki project whose Action API is
// queried and whose host builds article URLs (defaults to WIKI_CORPUS); Corpus is
// the provenance tag written to evidence_chunks.source (defaults to "<project>-crawl"
// so crawl rows are isolated from the dump corpus's delta checkpoint); MaxDepth
// bounds subcategory recursion (0 = direct pages only); MaxPages caps distinct
// pages collected; IncludeBody adds kind='body' chunks alongside the lead. Shards
// and ShardIndex partition Categories across parallel producers: with Shards > 1 a
// producer crawls only the categories at positions congruent to ShardIndex (mod
// Shards), so N producers given distinct indices crawl disjoint categories without
// duplicating API work. Shards defaults to 1 (sharding off).
type Crawl struct {
	Categories  []string
	Project     string
	Corpus      string
	MaxDepth    int
	MaxPages    int
	IncludeBody bool
	Shards      int
	ShardIndex  int
	// CheckpointPath is the resume state file (empty disables resume); ErrorBudget
	// is how many pages may be skipped before the run fails; GateFailClosed holds a
	// chunk whose fact-checkability gate errored (rather than publishing it) so a
	// rerun retries it.
	CheckpointPath string
	ErrorBudget    int
	GateFailClosed bool
}

// LoadCrawl reads the category-crawl configuration. CRAWL_CATEGORIES is required
// (a comma-separated list of category titles); the rest default. Bad values fail
// fast at startup.
func LoadCrawl() (Crawl, error) {
	raw, err := requireEnv("CRAWL_CATEGORIES")
	if err != nil {
		return Crawl{}, err
	}
	categories := make([]string, 0)
	for _, p := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(p); v != "" {
			categories = append(categories, v)
		}
	}
	if len(categories) == 0 {
		return Crawl{}, fmt.Errorf("config: CRAWL_CATEGORIES %q has no category", raw)
	}

	project := getenv("CRAWL_PROJECT", getenv("WIKI_CORPUS", defaultWikiCorpus))
	corpus := getenv("CRAWL_CORPUS", project+"-crawl")

	maxDepth, err := intEnv("CRAWL_MAX_DEPTH", defaultCrawlMaxDepth, 0, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}
	maxPages, err := intEnv("CRAWL_MAX_PAGES", defaultCrawlMaxPages, 1, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}

	includeBody := true
	if v := os.Getenv("CRAWL_INCLUDE_BODY"); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return Crawl{}, fmt.Errorf("config: CRAWL_INCLUDE_BODY %q: %w", v, perr)
		}
		includeBody = b
	}

	shards, err := intEnv("CRAWL_SHARDS", 1, 1, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}
	shardIndex, err := intEnv("CRAWL_SHARD_INDEX", 0, 0, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}
	if shardIndex >= shards {
		return Crawl{}, fmt.Errorf("config: CRAWL_SHARD_INDEX %d out of range for CRAWL_SHARDS %d (must be 0..%d)", shardIndex, shards, shards-1)
	}

	checkpointPath := defaultCrawlCheckpointPath
	if raw, ok := os.LookupEnv("CRAWL_CHECKPOINT_PATH"); ok {
		checkpointPath = raw
	}
	// Sharded producers share one /state volume, so give each shard its own
	// checkpoint file; a single file would be clobbered by concurrent atomic writes.
	if shards > 1 && checkpointPath != "" {
		checkpointPath = shardCheckpointPath(checkpointPath, shardIndex)
	}
	errorBudget, err := intEnv("CRAWL_ERROR_BUDGET", defaultCrawlErrorBudget, 0, math.MaxInt32)
	if err != nil {
		return Crawl{}, err
	}
	gateFailClosed, err := crawlGateFailClosed()
	if err != nil {
		return Crawl{}, err
	}

	return Crawl{
		Categories:     categories,
		Project:        project,
		Corpus:         corpus,
		MaxDepth:       maxDepth,
		MaxPages:       maxPages,
		IncludeBody:    includeBody,
		Shards:         shards,
		ShardIndex:     shardIndex,
		CheckpointPath: checkpointPath,
		ErrorBudget:    errorBudget,
		GateFailClosed: gateFailClosed,
	}, nil
}

// shardCheckpointPath inserts a per-shard suffix before the extension so each
// sharded producer writes its own checkpoint file (crawl-checkpoint.shard2.json).
func shardCheckpointPath(path string, index int) string {
	ext := filepath.Ext(path)
	return fmt.Sprintf("%s.shard%d%s", strings.TrimSuffix(path, ext), index, ext)
}

// crawlGateFailClosed parses CRAWL_GATE_FAIL_MODE: "open" (default) publishes a
// chunk whose gate call errored, "closed" holds it for a rerun. Any other value is
// an operator mistake worth failing on.
func crawlGateFailClosed() (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("CRAWL_GATE_FAIL_MODE"))); v {
	case "", "open":
		return false, nil
	case "closed":
		return true, nil
	default:
		return false, fmt.Errorf("config: CRAWL_GATE_FAIL_MODE %q must be \"open\" or \"closed\"", v)
	}
}

// Crawl fact-checkability gate defaults: the gate is on by default so the corpus
// stays bounded to citable evidence, runs on the selected provider's default fast
// model, and allows a modest number of in-flight judgments. The rate cap is
// unpaced by default; an operator sets it to bound provider spend on a large crawl.
const defaultCrawlCheckworthyConcurrency = 8

// CrawlCheckworthy configures the producer-side fact-checkability gate. Enabled
// (default true) runs an LLM judgment on each chunk before publishing so only
// citable evidence reaches the broker; false publishes every chunk, the pre-gate
// behavior. Model selects the classifier; Concurrency caps in-flight judgments;
// RPM (0 = unpaced) caps the per-producer Anthropic call rate. APIKey is a secret
// read from CHECKWORTHY_API_KEY and never logged.
type CrawlCheckworthy struct {
	LLMSelection
	Enabled     bool
	APIKey      string
	Model       string
	Concurrency int
	RPM         int
}

// Active reports whether the LLM gate should be wired. The loader already
// guarantees an enabled gate carries a key, so this is true exactly when the gate
// is on; callers pass a nil gate to the producer when it is false.
func (c CrawlCheckworthy) Active() bool {
	return c.Enabled && c.hasKey(c.APIKey)
}

// LoadCrawlCheckworthy reads the crawl fact-checkability gate configuration.
// CRAWL_CHECKWORTHY defaults to true, so the gate is on unless explicitly
// disabled; when on it requires CHECKWORTHY_API_KEY and fails fast otherwise
// rather than silently publishing everything. Bad values fail at startup. The
// secret is read but never logged.
func LoadCrawlCheckworthy() (CrawlCheckworthy, error) {
	enabled, err := boolEnvDefault("CRAWL_CHECKWORTHY", true)
	if err != nil {
		return CrawlCheckworthy{}, err
	}
	llmSel, err := loadLLMSelection()
	if err != nil {
		return CrawlCheckworthy{}, err
	}
	apiKey := getenv("CHECKWORTHY_API_KEY", "")
	// The crawl gate needs the selected provider's key when on: GEMINI_API_KEY under
	// Gemini, DEEPSEEK_API_KEY under DeepSeek (the default), otherwise
	// CHECKWORTHY_API_KEY (the Anthropic key).
	if enabled && llmSel.providerKey(apiKey) == "" {
		return CrawlCheckworthy{}, fmt.Errorf("config: CRAWL_CHECKWORTHY is on but no API key is set for provider %q", llmSel.Provider)
	}

	concurrency, err := intEnv("CRAWL_CHECKWORTHY_CONCURRENCY", defaultCrawlCheckworthyConcurrency, 1, math.MaxInt32)
	if err != nil {
		return CrawlCheckworthy{}, err
	}
	rpm, err := intEnv("CRAWL_CHECKWORTHY_RPM", 0, 0, math.MaxInt32)
	if err != nil {
		return CrawlCheckworthy{}, err
	}

	return CrawlCheckworthy{
		LLMSelection: llmSel,
		Enabled:      enabled,
		APIKey:       apiKey,
		Model:        getenv("CRAWL_CHECKWORTHY_MODEL", llmSel.defaultModel()),
		Concurrency:  concurrency,
		RPM:          rpm,
	}, nil
}

// defaultFactCheckLanguage filters the fact-check archive to French claims, the
// only jurisdiction this ingest targets.
const defaultFactCheckLanguage = "fr"

// defaultFactCheckTopics is the broadened French-language topic rotation the Google
// Fact Check Tools path walks when FACTCHECK_QUERIES is unset: a systematic sweep of
// the French political domains (institutions, parties and figures, and the recurring
// policy areas), far wider than the fixed ~19-topic legacy set. Combined with the
// full-catalog publisher-scoped streams over the outlet allowlist, it takes the
// Google path to the broadest French yield the API exposes.
var defaultFactCheckTopics = []string{
	// Institutions and process
	"élection présidentielle", "élections législatives", "élections européennes",
	"Assemblée nationale", "Sénat", "gouvernement", "Conseil constitutionnel",
	"référendum", "motion de censure", "Union européenne", "Commission européenne",
	// Figures and parties
	"Emmanuel Macron", "Marine Le Pen", "Jean-Luc Mélenchon", "Gabriel Attal",
	"Rassemblement national", "La France insoumise", "Renaissance", "Les Républicains",
	"Parti socialiste", "Europe Écologie Les Verts", "Jordan Bardella", "Édouard Philippe",
	// Policy areas
	"retraites", "réforme des retraites", "chômage", "emploi", "pouvoir d'achat",
	"inflation", "immigration", "asile", "sécurité", "délinquance", "impôts", "fiscalité",
	"dette publique", "déficit public", "santé", "hôpital", "école", "éducation",
	"énergie", "nucléaire", "climat", "transition écologique", "logement", "agriculture",
	"salaire minimum", "SMIC", "dépenses publiques", "sécurité sociale", "défense",
	"Ukraine", "OTAN", "terrorisme", "laïcité", "police", "justice",
}

// defaultFactCheckPublisherSites is the vetted French fact-check outlet allowlist the
// publisher-scoped streams page in full (reviewPublisherSiteFilter). It is the same
// EFCSN/IFCN-derived allowlist the DataCommons feed is filtered to.
var defaultFactCheckPublisherSites = []string{
	"factuel.afp.com",
	"lemonde.fr",
	"francetvinfo.fr",
	"20minutes.fr",
	"liberation.fr",
	"observers.france24.com",
}

// defaultFactCheckCheckpointPath persists the per-stream drain checkpoint between
// producer runs so a killed run resumes at the next undrained stream. Defaults under
// a state dir the operator can mount as a volume to survive container restarts.
const defaultFactCheckCheckpointPath = "/state/factcheck-checkpoint.json"

// FactCheckArchive configures the fact-check-archive producer that reads
// already-checked claims from the Google Fact Check Tools API into the curated
// claim DB. APIKey is the Google API key (sourced from the environment only, never
// logged); Topics are the claims:search query terms to walk (languageCode-filtered);
// PublisherSites are the outlet sites paged in full via reviewPublisherSiteFilter;
// Language filters by claim language; MaxPages caps result pages per stream (0 =
// every page); MaxAgeDays bounds results to recently published claims (0 = no
// bound); CheckpointPath persists per-stream resume state (empty disables it).
type FactCheckArchive struct {
	APIKey         string
	Topics         []string
	PublisherSites []string
	Language       string
	MaxPages       int
	MaxAgeDays     int
	CheckpointPath string
}

// LoadFactCheckArchive reads the fact-check-archive producer configuration. Only
// FACTCHECK_API_KEY is required. FACTCHECK_QUERIES overrides the broadened default
// topic rotation (comma-separated); FACTCHECK_PUBLISHER_SITES overrides the outlet
// allowlist and may be set empty to disable publisher-scoped streams; the rest
// default. Bad values fail fast at startup. The secret is read but never logged.
func LoadFactCheckArchive() (FactCheckArchive, error) {
	apiKey, err := requireEnv("FACTCHECK_API_KEY")
	if err != nil {
		return FactCheckArchive{}, err
	}
	// An empty-but-present value (a shipped compose injects FACTCHECK_QUERIES="") is
	// treated the same as unset: fall back to the broadened default rotation rather
	// than booting with no topics. Publisher streams stay on by default; the explicit
	// sentinel "none" opts out of them.
	topics := listEnvOr("FACTCHECK_QUERIES", defaultFactCheckTopics)
	sites := listEnvOr("FACTCHECK_PUBLISHER_SITES", defaultFactCheckPublisherSites, "none")
	maxPages, err := intEnv("FACTCHECK_MAX_PAGES", 0, 0, math.MaxInt32)
	if err != nil {
		return FactCheckArchive{}, err
	}
	maxAgeDays, err := intEnv("FACTCHECK_MAX_AGE_DAYS", 0, 0, math.MaxInt32)
	if err != nil {
		return FactCheckArchive{}, err
	}
	checkpointPath := defaultFactCheckCheckpointPath
	if raw, ok := os.LookupEnv("FACTCHECK_CHECKPOINT_PATH"); ok {
		checkpointPath = raw
	}
	return FactCheckArchive{
		APIKey:         apiKey,
		Topics:         topics,
		PublisherSites: sites,
		Language:       getenv("FACTCHECK_LANGUAGE", defaultFactCheckLanguage),
		MaxPages:       maxPages,
		MaxAgeDays:     maxAgeDays,
		CheckpointPath: checkpointPath,
	}, nil
}

// splitCommaList splits a comma-separated env value into trimmed, non-empty items.
func splitCommaList(raw string) []string {
	items := make([]string, 0)
	for _, s := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

// listEnvOr resolves a comma-separated list env var, treating unset AND
// empty-but-present (and a value that trims to no items) identically as "use the
// default". This is deliberate: the ingest compose files inject every knob as
// ${VAR:-} (empty when the host var is unset), so an empty value MUST NOT be read as
// an explicit "" that discards a vetted default (which for an outlet allowlist would
// silently widen ingestion worldwide, and for the topic set would boot with nothing).
// An explicit opt-out/opt-in-all is a distinct sentinel (case-insensitive), which
// returns the empty slice; only that sentinel does.
func listEnvOr(key string, def []string, emptySentinels ...string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	for _, s := range emptySentinels {
		if strings.EqualFold(strings.TrimSpace(raw), s) {
			return []string{}
		}
	}
	items := splitCommaList(raw)
	if len(items) == 0 {
		return def
	}
	return items
}

// defaultDataCommonsFeedURL is the daily ClaimReview data feed DataCommons
// publishes. It is a public, keyless object, so the DataCommons producer needs no
// Secrets Manager entry.
const defaultDataCommonsFeedURL = "https://storage.googleapis.com/datacommons-feeds/claimreview/latest/data.json"

// defaultDataCommonsOutlets is the vetted French fact-check outlet allowlist the
// DataCommons feed is filtered to. The feed carries no per-record language tag, so
// an author-URL allowlist is how the French subset is selected; each entry is a
// host substring matched case-insensitively against the record's author URL.
var defaultDataCommonsOutlets = []string{
	"factuel.afp.com",
	"lemonde.fr",
	"francetvinfo.fr",
	"20minutes.fr",
	"liberation.fr",
	"observers.france24.com",
}

// DataCommonsArchive configures the DataCommons ClaimReview feed producer that
// reads the aggregated ClaimReview markup feed into the curated claim DB. FeedURL
// is the DataFeed JSON endpoint (the daily feed by default, or a historical dump
// served in the same format for a one-shot backfill); OutletAllowlist is the set
// of author-URL host substrings a record must match to be ingested (empty ingests
// every outlet); MaxItems caps records examined (0 = the whole feed). It carries
// no secret: the feed is public and the broker URL loads from LoadFactCheckQueue.
type DataCommonsArchive struct {
	FeedURL         string
	OutletAllowlist []string
	MaxItems        int
	// Format selects the decoder: "datafeed" (the daily schema.org DataFeed JSON,
	// the default) or "ndjson" (one ClaimReview object per line, the shape of the
	// one-shot historical dump). Gzip is auto-detected from the URL/headers either way.
	Format string
}

// LoadDataCommonsArchive reads the DataCommons feed producer configuration from
// the environment. DATACOMMONS_FEED_URL defaults to the daily feed;
// DATACOMMONS_OUTLET_ALLOWLIST defaults to the vetted French outlet set — an
// empty-but-present value (which the ingest compose injects) keeps that default, so
// ingestion is NEVER silently widened worldwide; the explicit sentinel "*" (or
// "all") is the only way to ingest every outlet. DATACOMMONS_MAX_ITEMS caps examined
// records. Bad values fail fast at startup.
func LoadDataCommonsArchive() (DataCommonsArchive, error) {
	allow := listEnvOr("DATACOMMONS_OUTLET_ALLOWLIST", defaultDataCommonsOutlets, "*", "all")
	maxItems, err := intEnv("DATACOMMONS_MAX_ITEMS", 0, 0, math.MaxInt32)
	if err != nil {
		return DataCommonsArchive{}, err
	}
	format := getenv("DATACOMMONS_FEED_FORMAT", "datafeed")
	if format != "datafeed" && format != "ndjson" {
		return DataCommonsArchive{}, fmt.Errorf("config: DATACOMMONS_FEED_FORMAT %q must be datafeed or ndjson", format)
	}
	return DataCommonsArchive{
		FeedURL:         getenv("DATACOMMONS_FEED_URL", defaultDataCommonsFeedURL),
		OutletAllowlist: allow,
		MaxItems:        maxItems,
		Format:          format,
	}, nil
}

// ClaimReviewOutlet is one allowlisted fact-check outlet the JSON-LD reader visits:
// a display Name, the Host its reviews live on (the stored outlet tag and robots
// authority), and the Sitemap URL its article pages are discovered from.
type ClaimReviewOutlet struct {
	Name    string
	Host    string
	Sitemap string
}

// defaultClaimReviewOutlets is the EFCSN/IFCN-derived, config-curated French outlet
// allowlist the JSON-LD reader discovers ClaimReview markup from. Only official
// sitemaps are listed; discovery never spiders links. Sitemap URLs are the outlets'
// standard locations and are overridable per deploy if an outlet relocates one.
var defaultClaimReviewOutlets = []ClaimReviewOutlet{
	{Name: "AFP Factuel", Host: "factuel.afp.com", Sitemap: "https://factuel.afp.com/sitemap.xml"},
	{Name: "Les Décodeurs", Host: "www.lemonde.fr", Sitemap: "https://www.lemonde.fr/sitemap_index.xml"},
	{Name: "franceinfo Vrai ou Fake", Host: "www.francetvinfo.fr", Sitemap: "https://www.francetvinfo.fr/sitemap_index.xml"},
	{Name: "20 Minutes Fake Off", Host: "www.20minutes.fr", Sitemap: "https://www.20minutes.fr/sitemap.xml"},
}

const (
	defaultClaimReviewUserAgent = "truth-in-stream-factcheck-bot"
	defaultClaimReviewMinDelay  = 2 * time.Second
	defaultClaimReviewMaxURLs   = 200
)

// ClaimReviewSites configures the ClaimReview JSON-LD outlet reader. Outlets is the
// curated allowlist; UserAgent identifies the bot to robots.txt; MinDelay is the
// per-outlet pacing floor (raised by a robots Crawl-delay); MaxURLsPerOutlet caps
// pages fetched per outlet per run. It carries no secret: every source is public
// and the broker URL loads from LoadFactCheckQueue.
type ClaimReviewSites struct {
	Outlets          []ClaimReviewOutlet
	UserAgent        string
	MinDelay         time.Duration
	MaxURLsPerOutlet int
}

// LoadClaimReviewSites reads the ClaimReview outlet-reader configuration.
// CLAIMREVIEW_USER_AGENT, CLAIMREVIEW_MIN_DELAY_MS, and CLAIMREVIEW_MAX_URLS default;
// the outlet allowlist is the curated default (a deploy edits it in code, keeping it
// reviewable). Bad values fail fast at startup.
func LoadClaimReviewSites() (ClaimReviewSites, error) {
	minDelayMs, err := intEnv("CLAIMREVIEW_MIN_DELAY_MS", int(defaultClaimReviewMinDelay/time.Millisecond), 0, math.MaxInt32)
	if err != nil {
		return ClaimReviewSites{}, err
	}
	maxURLs, err := intEnv("CLAIMREVIEW_MAX_URLS", defaultClaimReviewMaxURLs, 1, math.MaxInt32)
	if err != nil {
		return ClaimReviewSites{}, err
	}
	return ClaimReviewSites{
		Outlets:          defaultClaimReviewOutlets,
		UserAgent:        getenv("CLAIMREVIEW_USER_AGENT", defaultClaimReviewUserAgent),
		MinDelay:         time.Duration(minDelayMs) * time.Millisecond,
		MaxURLsPerOutlet: maxURLs,
	}, nil
}

// ClaimsKGSeed configures the one-time ClaimsKG seed importer. It is a no-op unless
// Enabled is true and SeedFile points at a ClaimsKG CSV/TSV export; Vintage marks the
// snapshot's age in each record's provenance; TSV selects a tab delimiter. It carries
// no secret: the export is a local file and the broker URL loads from LoadFactCheckQueue.
type ClaimsKGSeed struct {
	Enabled  bool
	SeedFile string
	Vintage  string
	TSV      bool
}

// LoadClaimsKGSeed reads the ClaimsKG seed configuration. CLAIMSKG_SEED_ENABLED
// defaults false so the large stale snapshot is never ingested by accident;
// CLAIMSKG_SEED_FILE is the export path; CLAIMSKG_SEED_VINTAGE defaults to 2023;
// CLAIMSKG_SEED_TSV selects a tab-delimited export.
func LoadClaimsKGSeed() (ClaimsKGSeed, error) {
	enabled, err := boolEnvDefault("CLAIMSKG_SEED_ENABLED", false)
	if err != nil {
		return ClaimsKGSeed{}, err
	}
	tsv, err := boolEnvDefault("CLAIMSKG_SEED_TSV", false)
	if err != nil {
		return ClaimsKGSeed{}, err
	}
	return ClaimsKGSeed{
		Enabled:  enabled,
		SeedFile: getenv("CLAIMSKG_SEED_FILE", ""),
		Vintage:  getenv("CLAIMSKG_SEED_VINTAGE", "2023"),
		TSV:      tsv,
	}, nil
}

// scrutins-archive defaults. Legislature 17 is the current National Assembly
// term; the marker file persists the conditional-GET validators between runs so
// an unchanged archive is skipped, defaulting under a state dir the operator can
// mount as a volume to survive container restarts.
const (
	defaultScrutinsLegislature = "17"
	defaultScrutinsMarkerPath  = "/state/scrutins-marker.json"
)

// scrutinsLegislatureRe matches an AN legislature number, interpolated into the
// archive URL. Only a bare positive integer is valid: anything else would build
// a dead download URL or, worse, a path-traversal segment.
var scrutinsLegislatureRe = regexp.MustCompile(`^[1-9][0-9]*$`)

// ScrutinsArchive configures the scrutins-archive producer that conditionally
// downloads the AN open-data Scrutins.json.zip and publishes one job per scrutin.
// Legislature is the AN legislature number interpolated into the archive URL;
// MarkerPath is where the conditional-GET validators (ETag/Last-Modified) persist
// between runs so an unchanged archive does no redundant work (empty disables the
// skip). It carries no secret: the archive is public open data and the broker URL
// loads from LoadScrutinsQueue.
type ScrutinsArchive struct {
	Legislature string
	MarkerPath  string
}

// LoadScrutinsArchive reads the scrutins-archive producer configuration from the
// environment. SCRUTINS_LEGISLATURE defaults to 17 and must be a bare positive
// integer (it is interpolated into the download URL); SCRUTINS_MARKER_PATH
// defaults to a state file and may be set empty to disable the unchanged-archive
// skip. Bad values fail fast at startup rather than building a dead URL.
func LoadScrutinsArchive() (ScrutinsArchive, error) {
	legislature := getenv("SCRUTINS_LEGISLATURE", defaultScrutinsLegislature)
	if !scrutinsLegislatureRe.MatchString(legislature) {
		return ScrutinsArchive{}, fmt.Errorf("config: SCRUTINS_LEGISLATURE %q must be a positive integer", legislature)
	}
	markerPath := defaultScrutinsMarkerPath
	if raw, ok := os.LookupEnv("SCRUTINS_MARKER_PATH"); ok {
		markerPath = raw
	}
	return ScrutinsArchive{
		Legislature: legislature,
		MarkerPath:  markerPath,
	}, nil
}

// intEnv reads an integer environment variable, applying fallback when unset
// and enforcing an inclusive [low, high] range.
func intEnv(key string, fallback, low, high int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	if v < low || v > high {
		return 0, fmt.Errorf("config: %s must be in [%d, %d], got %d", key, low, high, v)
	}
	return v, nil
}

// EvidenceBinaryQuantizationMultiplier reads EVIDENCE_BQ_MULTIPLIER, the coarse
// candidate multiplier for the two-stage binary-quantization evidence search
// (VER-176). 0 (the default) keeps the single-stage halfvec search; a positive
// value enables the two-stage path, gathering multiplier x k coarse candidates
// from the bit index before the halfvec rerank. It is off by default per the
// VER-173 verdict - recall on the current corpus does not yet justify it - and
// an operator flips it on when the corpus approaches the halfvec HNSW RAM
// ceiling. The [0, 1000] bound keeps a fat-fingered multiplier from scanning the
// whole corpus per query.
func EvidenceBinaryQuantizationMultiplier() (int, error) {
	return intEnv("EVIDENCE_BQ_MULTIPLIER", 0, 0, 1000)
}

// EvidenceNearDupSimilarity reads EVIDENCE_NEAR_DUP_SIMILARITY, the cosine
// similarity bar the near-duplicate gate (VER-203, measure 2) applies at
// embed-write time on the single-write ingest path. 0 (the default) disables the
// gate: every embedded chunk is served. A positive value in (0, 1] turns it on -
// a fresh chunk whose nearest same-source neighbor is at least this similar is
// withheld from search (stored for provenance with no vector) as a redundant
// re-rendering. The intended setting sits well above the evidence borrow
// threshold so only true near-identities are gated, and it stays off by default
// until the golden eval proves no recall loss. The [0, 1] bound matches cosine
// similarity; floatEnv rejects anything outside it.
func EvidenceNearDupSimilarity() (float64, error) {
	return floatEnv("EVIDENCE_NEAR_DUP_SIMILARITY", 0)
}

// floatEnv reads a unit-interval float environment variable, applying fallback
// when unset and enforcing an inclusive [0, 1] range.
func floatEnv(key string, fallback float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	if v < 0 || v > 1 {
		return 0, fmt.Errorf("config: %s must be in [0, 1], got %g", key, v)
	}
	return v, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// positiveDurationEnv reads key as a Go duration, falling back when unset and
// rejecting non-positive values.
func positiveDurationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: %s must be positive, got %s", key, d)
	}
	return d, nil
}

// durationEnvAllowZero reads key as a Go duration, falling back when unset and
// rejecting only negative values, so a feature that uses 0 as "disabled" (the
// repeated-claim cache) can be turned off explicitly.
func durationEnvAllowZero(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("config: %s must not be negative, got %s", key, d)
	}
	return d, nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: %s is required", key)
	}
	return v, nil
}

// CheckWorthinessLocal configures the locally-served check-worthiness
// classifier (VER-225): a fine-tuned French encoder scoring statements on CPU
// so the generative gate is consulted only inside the uncertainty band. The
// model and tokenizer artifacts are distributed outside git and located by
// path; a missing artifact, a load failure, or a binary built without the
// localinference build tag all degrade to the existing heuristic-plus-model
// cascade at boot, never blocking a session.
type CheckWorthinessLocal struct {
	Enabled       bool
	ModelPath     string
	TokenizerPath string
	LibraryPath   string
	BandLow       float64
	BandHigh      float64
	Timeout       time.Duration
}

// Active reports whether the local scorer should be wired: enabled with both
// artifacts configured. Artifact existence is proven by the boot-time health
// check, not here, so a bad path degrades with a warning instead of failing
// the boot.
func (c CheckWorthinessLocal) Active() bool {
	return c.Enabled && c.ModelPath != "" && c.TokenizerPath != ""
}

// Default band bounds ship from the calibration run on the golden gate set:
// below low rejects locally, at or above high accepts locally, in between
// routes to the generative gate.
const (
	defaultCheckWorthinessLocalBandLow  = 0.35
	defaultCheckWorthinessLocalBandHigh = 0.75
	defaultCheckWorthinessLocalTimeout  = 300 * time.Millisecond
)

// LoadCheckWorthinessLocal reads the CHECKWORTHINESS_LOCAL_* block.
func LoadCheckWorthinessLocal() (CheckWorthinessLocal, error) {
	c := CheckWorthinessLocal{
		ModelPath:     os.Getenv("CHECKWORTHINESS_LOCAL_MODEL_PATH"),
		TokenizerPath: os.Getenv("CHECKWORTHINESS_LOCAL_TOKENIZER_PATH"),
		LibraryPath:   os.Getenv("CHECKWORTHINESS_LOCAL_ONNX_LIBRARY"),
	}
	var err error
	if c.Enabled, err = boolEnvDefault("CHECKWORTHINESS_LOCAL_ENABLED", true); err != nil {
		return CheckWorthinessLocal{}, err
	}
	if c.BandLow, err = floatEnv("CHECKWORTHINESS_LOCAL_BAND_LOW", defaultCheckWorthinessLocalBandLow); err != nil {
		return CheckWorthinessLocal{}, err
	}
	if c.BandHigh, err = floatEnv("CHECKWORTHINESS_LOCAL_BAND_HIGH", defaultCheckWorthinessLocalBandHigh); err != nil {
		return CheckWorthinessLocal{}, err
	}
	if !(c.BandLow >= 0 && c.BandLow <= 1) || !(c.BandHigh >= 0 && c.BandHigh <= 1) {
		return CheckWorthinessLocal{}, fmt.Errorf("config: CHECKWORTHINESS_LOCAL band bounds must be probabilities in [0, 1], got low %v high %v", c.BandLow, c.BandHigh)
	}
	if c.BandLow > c.BandHigh {
		return CheckWorthinessLocal{}, fmt.Errorf("config: CHECKWORTHINESS_LOCAL_BAND_LOW %v must not exceed CHECKWORTHINESS_LOCAL_BAND_HIGH %v", c.BandLow, c.BandHigh)
	}
	if c.Timeout, err = positiveDurationEnv("CHECKWORTHINESS_LOCAL_TIMEOUT", defaultCheckWorthinessLocalTimeout); err != nil {
		return CheckWorthinessLocal{}, err
	}
	return c, nil
}

// VerifyNLI configures the local NLI stance stage in front of the generative
// verifier (VER-228): a French cross-encoder scores each retrieved passage's
// stance toward the claim on CPU, and clear support or contradiction decides
// the verdict locally with citations. Thresholds and the calibration
// temperature ship from the training pipeline's calibration run; the model
// artifact is the published community ONNX export, distributed outside git. A
// missing artifact, a load failure, or a binary built without the
// localinference build tag degrades to the LLM-first verify path at boot.
type VerifyNLI struct {
	Enabled             bool
	ModelPath           string
	TokenizerPath       string
	LibraryPath         string
	Temperature         float64
	EntailThreshold     float64
	ContradictThreshold float64
	MinAgree            int
	MaxPassages         int
	Timeout             time.Duration
}

// Active reports whether the stance stage should be wired: enabled with both
// artifacts configured. Artifact usability is proven by the boot-time health
// check, not here.
func (c VerifyNLI) Active() bool {
	return c.Enabled && c.ModelPath != "" && c.TokenizerPath != ""
}

// Defaults ship from the calibration run recorded by the training pipeline
// (stack/ml/checkworthy nli-calibrate): temperature fitted on the labeled
// French pair set, thresholds chosen at 100 percent decided-accuracy with the
// contradiction bar deliberately higher than the entailment bar - wrongly
// refuting a claim costs more than escalating it.
const (
	defaultVerifyNLITemperature         = 1.8634
	defaultVerifyNLIEntailThreshold     = 0.70
	defaultVerifyNLIContradictThreshold = 0.90
	defaultVerifyNLIMinAgree            = 1
	defaultVerifyNLIMaxPassages         = 6
	defaultVerifyNLITimeout             = 2 * time.Second
)

// LoadVerifyNLI reads the FACTCHECK_NLI_* block. The ONNX Runtime library
// path falls back to the check-worthiness scorer's, since one process loads
// exactly one runtime.
func LoadVerifyNLI() (VerifyNLI, error) {
	c := VerifyNLI{
		ModelPath:     os.Getenv("FACTCHECK_NLI_MODEL_PATH"),
		TokenizerPath: os.Getenv("FACTCHECK_NLI_TOKENIZER_PATH"),
		LibraryPath:   os.Getenv("FACTCHECK_NLI_ONNX_LIBRARY"),
	}
	if c.LibraryPath == "" {
		c.LibraryPath = os.Getenv("CHECKWORTHINESS_LOCAL_ONNX_LIBRARY")
	}
	var err error
	if c.Enabled, err = boolEnvDefault("FACTCHECK_NLI_ENABLED", true); err != nil {
		return VerifyNLI{}, err
	}
	// A softmax temperature legitimately exceeds one, so it cannot go through
	// the probability-bounded floatEnv helper.
	c.Temperature = defaultVerifyNLITemperature
	if raw := os.Getenv("FACTCHECK_NLI_TEMPERATURE"); raw != "" {
		if c.Temperature, err = strconv.ParseFloat(raw, 64); err != nil {
			return VerifyNLI{}, fmt.Errorf("config: FACTCHECK_NLI_TEMPERATURE %q: %w", raw, err)
		}
	}
	if !(c.Temperature > 0) || math.IsInf(c.Temperature, 0) {
		return VerifyNLI{}, fmt.Errorf("config: FACTCHECK_NLI_TEMPERATURE must be a positive finite number, got %v", c.Temperature)
	}
	if c.EntailThreshold, err = floatEnv("FACTCHECK_NLI_ENTAIL_THRESHOLD", defaultVerifyNLIEntailThreshold); err != nil {
		return VerifyNLI{}, err
	}
	if c.ContradictThreshold, err = floatEnv("FACTCHECK_NLI_CONTRADICT_THRESHOLD", defaultVerifyNLIContradictThreshold); err != nil {
		return VerifyNLI{}, err
	}
	if !(c.EntailThreshold > 0 && c.EntailThreshold <= 1) || !(c.ContradictThreshold > 0 && c.ContradictThreshold <= 1) {
		return VerifyNLI{}, fmt.Errorf("config: FACTCHECK_NLI thresholds must be probabilities in (0, 1], got entail %v contradict %v", c.EntailThreshold, c.ContradictThreshold)
	}
	if c.MinAgree, err = intEnv("FACTCHECK_NLI_MIN_AGREE", defaultVerifyNLIMinAgree, 1, 100); err != nil {
		return VerifyNLI{}, err
	}
	if c.MaxPassages, err = intEnv("FACTCHECK_NLI_MAX_PASSAGES", defaultVerifyNLIMaxPassages, 1, 100); err != nil {
		return VerifyNLI{}, err
	}
	if c.Timeout, err = positiveDurationEnv("FACTCHECK_NLI_TIMEOUT", defaultVerifyNLITimeout); err != nil {
		return VerifyNLI{}, err
	}
	return c, nil
}
