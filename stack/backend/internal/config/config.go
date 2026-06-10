// Package config loads service configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// defaultEmbeddingModel is the voyage model used for both ingest and query
// embeddings. The same model must be used on both sides or similarity scores
// are meaningless.
const defaultEmbeddingModel = "voyage-4"

// defaultTranscriptionModel is the ElevenLabs batch speech-to-text model.
const defaultTranscriptionModel = "scribe_v2"

// defaultDemoMediaDir is where the server reads and serves the bundled demo
// clip from, relative to the working directory.
const defaultDemoMediaDir = "demo"

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

// LoadEmbedding reads the embedding provider configuration from the
// environment. EMBEDDING_API_KEY is required; the model defaults to voyage-4
// and the dimension is pinned to the claim store's EmbeddingDim. An
// EMBEDDING_DIM that disagrees with the pinned dimension is a fatal
// misconfiguration rather than a silent re-ingest hazard.
func LoadEmbedding() (Embedding, error) {
	apiKey, err := requireEnv("EMBEDDING_API_KEY")
	if err != nil {
		return Embedding{}, err
	}
	e := Embedding{
		APIKey: apiKey,
		Model:  getenv("EMBEDDING_MODEL", defaultEmbeddingModel),
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

// Transcription holds the configuration for the ElevenLabs speech-to-text
// provider.
type Transcription struct {
	APIKey string
	Model  string
}

// LoadTranscription reads the transcription provider configuration from the
// environment. TRANSCRIPTION_API_KEY is required; the model defaults to
// scribe_v2.
func LoadTranscription() (Transcription, error) {
	apiKey, err := requireEnv("TRANSCRIPTION_API_KEY")
	if err != nil {
		return Transcription{}, err
	}
	return Transcription{
		APIKey: apiKey,
		Model:  getenv("TRANSCRIPTION_MODEL", defaultTranscriptionModel),
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

// Match defaults: top-5 keeps responses focused, 0.5 cosine similarity drops
// unrelated text without hiding genuine paraphrases, 4 concurrent embed calls
// stay well inside the Voyage rate limits, and 10s bounds a single segment
// end to end.
const (
	defaultMatchTopK             = 5
	defaultMatchScoreThreshold   = 0.5
	defaultMatchEmbedConcurrency = 4
	defaultMatchTimeout          = 10 * time.Second
)

// Match holds the segment-to-claim matching configuration.
type Match struct {
	TopK             int
	ScoreThreshold   float64
	EmbedConcurrency int
	Timeout          time.Duration
}

// LoadMatch reads the matching configuration from the environment, applying
// defaults and failing fast on values that would make matching meaningless
// (non-positive k or concurrency, a threshold outside cosine similarity's
// [-1, 1] range, a non-positive timeout).
func LoadMatch() (Match, error) {
	m := Match{
		TopK:             defaultMatchTopK,
		ScoreThreshold:   defaultMatchScoreThreshold,
		EmbedConcurrency: defaultMatchEmbedConcurrency,
		Timeout:          defaultMatchTimeout,
	}
	if raw := os.Getenv("MATCH_TOP_K"); raw != "" {
		k, err := strconv.Atoi(raw)
		if err != nil {
			return Match{}, fmt.Errorf("config: MATCH_TOP_K %q: %w", raw, err)
		}
		if k < 1 || k > math.MaxInt32 {
			return Match{}, fmt.Errorf("config: MATCH_TOP_K must be in [1, %d], got %d", math.MaxInt32, k)
		}
		m.TopK = k
	}
	if raw := os.Getenv("MATCH_SCORE_THRESHOLD"); raw != "" {
		threshold, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Match{}, fmt.Errorf("config: MATCH_SCORE_THRESHOLD %q: %w", raw, err)
		}
		// The inverted comparison also rejects NaN, which ParseFloat accepts
		// and which would otherwise disable the filter entirely.
		if !(threshold >= -1 && threshold <= 1) {
			return Match{}, fmt.Errorf("config: MATCH_SCORE_THRESHOLD %v outside cosine similarity range [-1, 1]", threshold)
		}
		m.ScoreThreshold = threshold
	}
	if raw := os.Getenv("MATCH_EMBED_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Match{}, fmt.Errorf("config: MATCH_EMBED_CONCURRENCY %q: %w", raw, err)
		}
		if n < 1 {
			return Match{}, fmt.Errorf("config: MATCH_EMBED_CONCURRENCY must be at least 1, got %d", n)
		}
		m.EmbedConcurrency = n
	}
	timeout, err := positiveDurationEnv("MATCH_TIMEOUT", m.Timeout)
	if err != nil {
		return Match{}, err
	}
	m.Timeout = timeout
	return m, nil
}

// Precheck defaults: a 4-word minimum drops bare fragments while keeping short
// real claims, and a 0.4 cosine coverage floor sits below the 0.5 match
// threshold so coverage only skips clearly-uncovered claims - a covered claim
// can still yield no confident match, but an uncovered one is never forced into
// a verdict. The gate is on by default; precision over recall is the point.
const (
	defaultPrecheckMinWords          = 4
	defaultPrecheckCoverageThreshold = 0.4
)

// Precheck holds the check-worthiness gate configuration. Enabled toggles the
// whole gate; MinWords bounds the claim-worthiness fragment filter;
// CoverageThreshold is the minimum corpus similarity a claim must reach to be
// checked.
type Precheck struct {
	Enabled           bool
	MinWords          int
	CoverageThreshold float64
}

// LoadPrecheck reads the precheck-gate configuration from the environment,
// applying defaults and failing fast on a coverage threshold outside cosine
// similarity's [-1, 1] range or a non-positive minimum word count.
func LoadPrecheck() (Precheck, error) {
	p := Precheck{
		Enabled:           true,
		MinWords:          defaultPrecheckMinWords,
		CoverageThreshold: defaultPrecheckCoverageThreshold,
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
		// The inverted comparison also rejects NaN, which ParseFloat accepts and
		// which would make every segment fall through as not covered.
		if !(threshold >= -1 && threshold <= 1) {
			return Precheck{}, fmt.Errorf("config: PRECHECK_COVERAGE_THRESHOLD %v outside cosine similarity range [-1, 1]", threshold)
		}
		p.CoverageThreshold = threshold
	}
	return p, nil
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

// Bulk-embedding defaults. A 128-input batch sits well under Voyage's 1000
// input / 320k token per-request ceilings even for the longest chunks; four
// concurrent requests stay inside the tier-1 rate limit; six retries ride out
// transient throttling. 512MB maintenance_work_mem builds the simplewiki HNSW
// in memory and is safe on a small instance - raise it for enwiki - and seven
// parallel workers matches pgvector's index-build guidance.
const (
	defaultWikiEmbedBatchSize          = 128
	defaultWikiEmbedConcurrency        = 4
	defaultWikiEmbedMaxRetries         = 6
	defaultWikiEmbedMaintenanceWorkMem = "512MB"
	defaultWikiEmbedMaxParallelWorkers = 7
	// maxVoyageInputsPerRequest is Voyage's documented per-request input cap.
	maxVoyageInputsPerRequest = 1000
)

// workMemRe matches a Postgres memory size like "512MB" or "2GB". It guards
// the value before it reaches set_config, rejecting typos and anything that is
// not a bare size literal.
var workMemRe = regexp.MustCompile(`^[1-9][0-9]*(kB|MB|GB|TB)$`)

// WikiEmbed holds the bulk-embedding pipeline configuration. BatchSize and
// Concurrency bound the embedding API load; MaintenanceWorkMem and
// MaxParallelWorkers tune the post-load HNSW index build.
type WikiEmbed struct {
	BatchSize          int
	Concurrency        int
	MaxRetries         int
	MaintenanceWorkMem string
	MaxParallelWorkers int
}

// LoadWikiEmbed reads the bulk-embedding configuration from the environment,
// applying defaults and failing fast on values that would overrun the
// embedding API limits or produce invalid index-build settings.
func LoadWikiEmbed() (WikiEmbed, error) {
	w := WikiEmbed{
		BatchSize:          defaultWikiEmbedBatchSize,
		Concurrency:        defaultWikiEmbedConcurrency,
		MaxRetries:         defaultWikiEmbedMaxRetries,
		MaintenanceWorkMem: defaultWikiEmbedMaintenanceWorkMem,
		MaxParallelWorkers: defaultWikiEmbedMaxParallelWorkers,
	}
	var err error
	if w.BatchSize, err = intEnv("WIKI_EMBED_BATCH_SIZE", w.BatchSize, 1, maxVoyageInputsPerRequest); err != nil {
		return WikiEmbed{}, err
	}
	if w.Concurrency, err = intEnv("WIKI_EMBED_CONCURRENCY", w.Concurrency, 1, math.MaxInt32); err != nil {
		return WikiEmbed{}, err
	}
	if w.MaxRetries, err = intEnv("WIKI_EMBED_MAX_RETRIES", w.MaxRetries, 1, math.MaxInt32); err != nil {
		return WikiEmbed{}, err
	}
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
type Storage struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
	PutTTL       time.Duration
	GetTTL       time.Duration
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
		Endpoint:  os.Getenv("STORAGE_ENDPOINT"),
		Region:    getenv("STORAGE_REGION", defaultStorageRegion),
		Bucket:    bucket,
		AccessKey: os.Getenv("STORAGE_ACCESS_KEY"),
		SecretKey: os.Getenv("STORAGE_SECRET_KEY"),
		PutTTL:    defaultStoragePutTTL,
		GetTTL:    defaultStorageGetTTL,
	}
	if (s.AccessKey == "") != (s.SecretKey == "") {
		return Storage{}, errors.New("config: STORAGE_ACCESS_KEY and STORAGE_SECRET_KEY must be set together")
	}
	if s.UsePathStyle, err = boolEnv("STORAGE_USE_PATH_STYLE", false); err != nil {
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

// boolEnv reads a boolean environment variable, applying fallback when unset.
func boolEnv(key string, fallback bool) (bool, error) {
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

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: %s is required", key)
	}
	return v, nil
}
