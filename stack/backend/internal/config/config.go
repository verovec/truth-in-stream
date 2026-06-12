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

// Match defaults: top-5 keeps responses focused, 0.5 cosine similarity drops
// unrelated text without hiding genuine paraphrases, 4 concurrent embed calls
// stay well inside the Voyage rate limits, and 10s bounds a single segment
// end to end. Evidence retrieval pulls 5 Wikipedia chunks at a higher 0.6
// threshold (the corpus is far larger, so a stricter bar avoids loosely related
// noise), and the merged result is capped at 5 across both corpora.
const (
	defaultMatchTopK              = 5
	defaultMatchScoreThreshold    = 0.5
	defaultMatchEvidenceTopK      = 5
	defaultMatchEvidenceThreshold = 0.6
	defaultMatchMaxResults        = 5
	defaultMatchEmbedConcurrency  = 4
	defaultMatchTimeout           = 10 * time.Second
)

// Match holds the segment matching configuration across the curated claims and
// Wikipedia evidence corpora.
type Match struct {
	TopK              int
	ScoreThreshold    float64
	EvidenceTopK      int
	EvidenceThreshold float64
	MaxResults        int
	EmbedConcurrency  int
	Timeout           time.Duration
}

// LoadMatch reads the matching configuration from the environment, applying
// defaults and failing fast on values that would make matching meaningless
// (out-of-range k or concurrency, a threshold outside cosine similarity's
// [-1, 1] range, a non-positive timeout). MATCH_EVIDENCE_TOP_K 0 disables
// Wikipedia evidence retrieval.
func LoadMatch() (Match, error) {
	m := Match{
		TopK:              defaultMatchTopK,
		ScoreThreshold:    defaultMatchScoreThreshold,
		EvidenceTopK:      defaultMatchEvidenceTopK,
		EvidenceThreshold: defaultMatchEvidenceThreshold,
		MaxResults:        defaultMatchMaxResults,
		EmbedConcurrency:  defaultMatchEmbedConcurrency,
		Timeout:           defaultMatchTimeout,
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
	return m, nil
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

// LoadDebugSearch reads the developer wiki-search configuration from the
// environment. DEBUG_WIKI_SEARCH gates the whole feature (default off);
// DEBUG_WIKI_SEARCH_TOP_K and DEBUG_WIKI_SEARCH_TIMEOUT tune it when enabled.
func LoadDebugSearch() (DebugSearch, error) {
	enabled, err := boolEnv("DEBUG_WIKI_SEARCH", false)
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
		if !domain.ValidCosineThreshold(threshold) {
			return Precheck{}, fmt.Errorf("config: PRECHECK_COVERAGE_THRESHOLD %v outside cosine similarity range [-1, 1]", threshold)
		}
		p.CoverageThreshold = threshold
	}
	return p, nil
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

// Bulk-embedding defaults. A 128-input batch sits well under Voyage's 1000
// input / 320k token per-request ceilings even for the longest chunks; four
// concurrent requests stay inside the tier-1 rate limit; six retries ride out
// transient throttling. A 30s per-request HTTP timeout is generous for a
// healthy batch (Voyage returns a 128-input request in seconds) yet surfaces a
// throttling stall fast, instead of burning two minutes on each hung request
// before the retry. RequestsPerMinute is off by default (0 = unpaced, right for
// a paid tier whose limit is thousands of RPM); set it to pace a run onto a
// constrained tier's request budget rather than bursting past it and stalling.
// 512MB maintenance_work_mem builds the simplewiki HNSW in memory and is safe on
// a small instance - raise it for enwiki - and seven parallel workers matches
// pgvector's index-build guidance.
const (
	defaultWikiEmbedBatchSize          = 128
	defaultWikiEmbedConcurrency        = 4
	defaultWikiEmbedMaxRetries         = 6
	defaultWikiEmbedHTTPTimeout        = 30 * time.Second
	defaultWikiEmbedRequestsPerMinute  = 0
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
// Concurrency bound the embedding API load; RequestsPerMinute optionally paces
// outbound embedding requests onto a tier's budget (0 = unpaced); HTTPTimeout
// bounds each Voyage request on the bulk path; MaintenanceWorkMem and
// MaxParallelWorkers tune the post-load HNSW index build.
type WikiEmbed struct {
	BatchSize          int
	Concurrency        int
	MaxRetries         int
	RequestsPerMinute  int
	HTTPTimeout        time.Duration
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
		RequestsPerMinute:  defaultWikiEmbedRequestsPerMinute,
		HTTPTimeout:        defaultWikiEmbedHTTPTimeout,
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
	// 0 disables pacing; a positive value caps outbound requests per minute so a
	// constrained tier is not overrun.
	if w.RequestsPerMinute, err = intEnv("WIKI_EMBED_RPM", w.RequestsPerMinute, 0, math.MaxInt32); err != nil {
		return WikiEmbed{}, err
	}
	if w.HTTPTimeout, err = positiveDurationEnv("WIKI_EMBED_HTTP_TIMEOUT", w.HTTPTimeout); err != nil {
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
)

// Queue holds the RabbitMQ embedding-job queue configuration. URL is required
// and carries the broker credentials, so it is sourced from the environment
// only and never logged. MaxPriority sets the queue's x-max-priority ceiling and
// Prefetch the per-consumer unacknowledged-message limit (0 leaves it unbounded).
type Queue struct {
	URL         string
	Name        string
	MaxPriority uint8
	Prefetch    int
}

// LoadQueue reads the broker configuration from the environment. RABBITMQ_URL is
// required; the queue name defaults to embedding.jobs, the max priority to 10
// (validated to [1, 255]) and the prefetch to 1 (validated non-negative). Bad
// values fail fast at startup rather than surfacing as a broker error later.
func LoadQueue() (Queue, error) {
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
	return Queue{
		URL:         url,
		Name:        getenv("RABBITMQ_QUEUE", defaultQueueName),
		MaxPriority: uint8(maxPriority),
		Prefetch:    prefetch,
	}, nil
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
	if w.BulkFraction, err = floatEnv("WIKI_DELTA_BULK_FRACTION", w.BulkFraction, 0, 1); err != nil {
		return WikiDelta{}, err
	}
	return w, nil
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

// floatEnv reads a float environment variable, applying fallback when unset and
// enforcing an inclusive [low, high] range.
func floatEnv(key string, fallback, low, high float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s %q: %w", key, raw, err)
	}
	if v < low || v > high {
		return 0, fmt.Errorf("config: %s must be in [%g, %g], got %g", key, low, high, v)
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
