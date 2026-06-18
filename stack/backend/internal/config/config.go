// Package config loads service configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
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
	if m.ConfidenceLeadWeight, err = floatEnv("MATCH_CONFIDENCE_LEAD_WEIGHT", m.ConfidenceLeadWeight, 0, 1); err != nil {
		return Match{}, err
	}
	if m.ConfidenceBodyWeight, err = floatEnv("MATCH_CONFIDENCE_BODY_WEIGHT", m.ConfidenceBodyWeight, 0, 1); err != nil {
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
)

// Live holds the live-analyzer scoring configuration. Concurrency is the number
// of verdict workers; QueueDepth is the bounded backlog those workers drain
// before a ready unit is shed to not_checked.
type Live struct {
	Concurrency int
	QueueDepth  int
}

// LoadLive reads the live-analyzer configuration from the environment, applying
// defaults and failing fast when concurrency or queue depth is not a positive
// integer (a zero pool or zero buffer would stall or drop every statement).
func LoadLive() (Live, error) {
	l := Live{Concurrency: defaultLiveConcurrency, QueueDepth: defaultLiveQueueDepth}
	var err error
	if l.Concurrency, err = intEnv("LIVE_CONCURRENCY", l.Concurrency, 1, math.MaxInt32); err != nil {
		return Live{}, err
	}
	if l.QueueDepth, err = intEnv("LIVE_QUEUE_DEPTH", l.QueueDepth, 1, math.MaxInt32); err != nil {
		return Live{}, err
	}
	return l, nil
}

// Intra-speaker consistency defaults. The model is the cheapest fast Claude
// model, suitable for a binary contradiction judgment over two short
// statements; TopK 3 caps stance calls per statement; SimilarityFloor 0.6 keeps
// the stance check off topically-unrelated prior statements. These are the
// env-layer defaults; the service and stance packages keep matching library
// defaults for direct construction and must stay in sync with them.
const (
	defaultConsistencyModel = "claude-haiku-4-5-20251001"
	defaultConsistencyTopK  = 3
	defaultConsistencyFloor = 0.6
)

// Consistency holds the live intra-speaker consistency configuration. The
// feature is off unless Enabled is true and APIKey is set: with no key, live
// analysis behaves exactly as before, emitting no consistency flags. APIKey is
// a secret and comes from the environment only - never logged. Model selects
// the stance model; TopK and SimilarityFloor tune detection.
type Consistency struct {
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
	return c.Enabled && c.APIKey != ""
}

// LoadConsistency reads the intra-speaker consistency configuration from the
// environment, applying defaults and failing fast on an out-of-range top-k or
// similarity floor. The secret is read but never logged.
func LoadConsistency() (Consistency, error) {
	c := Consistency{
		Model:           defaultConsistencyModel,
		TopK:            defaultConsistencyTopK,
		SimilarityFloor: defaultConsistencyFloor,
	}
	var err error
	if c.Enabled, err = boolEnv("CONSISTENCY_ENABLED"); err != nil {
		return Consistency{}, err
	}
	c.APIKey = getenv("CONSISTENCY_API_KEY", "")
	c.Model = getenv("CONSISTENCY_MODEL", c.Model)
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

// Retrieve-then-verify defaults. The path is off by default (the old
// gate-and-match path stays the default until the golden eval clears the
// baseline). Decomposition and verification both run on the cheapest fast Claude
// model. MaxClaimsPerUnit caps a unit's fan-out at 4 atomic claims. FastTau 0.85
// is the curated near-match similarity at or above which the fast path borrows a
// verdict with no LLM call - a high bar, since a borrowed verdict bypasses
// reasoning. The verify pool is 2 workers with a 4-deep queue, smaller than the
// fast pool because each verify call is an LLM round-trip; FastDeadline 800ms
// bounds decompose-plus-retrieve and VerifyDeadline 4s bounds one verify call,
// matching the spec's tiers. CacheTTL 30s collapses a recurring talking point
// repeated within the window. These are the env-layer defaults; the service
// package validates the same bounds for direct construction.
const (
	defaultVerifyModel            = "claude-haiku-4-5-20251001"
	defaultVerifyMaxClaimsPerUnit = 4
	defaultVerifyFastTau          = 0.85
	defaultVerifyConcurrency      = 2
	defaultVerifyQueueDepth       = 4
	defaultVerifyFastDeadline     = 800 * time.Millisecond
	defaultVerifyDeadline         = 4 * time.Second
	defaultVerifyCacheTTL         = 30 * time.Second
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
	// defaultSpeakerScorePriorStrength is the Beta-Binomial prior pseudo-count k
	// for the per-speaker credibility score. A symmetric Beta(k/2, k/2) prior is
	// neutral (mean 0.5); larger k shrinks harder toward neutral, so the score
	// moves more slowly and a speaker with one credible claim does not read as a
	// confident 100% (k=4 puts one full-confidence credible claim at 60%).
	// SPEAKER_SCORE_PRIOR_STRENGTH overrides it; the service package mirrors the
	// same default for direct construction.
	defaultSpeakerScorePriorStrength = 4.0
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
	// RetrievalThreshold is the cosine floor for the evidence the verify path
	// retrieves and feeds the verifier. It is a recall bar, lower than the legacy
	// borrow-by-similarity threshold, so the on-topic band is retrieved rather than
	// discarded before the verifier ever sees it.
	RetrievalThreshold float64
	// SpeakerPriorStrength is the Beta-Binomial prior pseudo-count for the
	// per-speaker credibility score. Larger values move the score more slowly.
	SpeakerPriorStrength float64
}

// Active reports whether the retrieve-then-verify path should be wired: it is
// enabled and has the API key its decomposer and verifier need. Wiring keys off
// this so an enabled-but-keyless configuration degrades to the legacy path
// rather than failing to start.
func (v VerifyPath) Active() bool {
	return v.Enabled && v.APIKey != ""
}

// LoadVerifyPath reads the retrieve-then-verify configuration from the
// environment, applying defaults and failing fast on out-of-range bounds (a
// non-positive pool or deadline, a fast tau outside cosine similarity's [-1, 1]
// range, a negative queue depth or cache ttl). FACTCHECK_VERIFY_PATH gates the
// whole feature (default off). The secret is read but never logged.
func LoadVerifyPath() (VerifyPath, error) {
	v := VerifyPath{
		Model:                defaultVerifyModel,
		MaxClaimsPerUnit:     defaultVerifyMaxClaimsPerUnit,
		FastTau:              defaultVerifyFastTau,
		Concurrency:          defaultVerifyConcurrency,
		QueueDepth:           defaultVerifyQueueDepth,
		FastDeadline:         defaultVerifyFastDeadline,
		VerifyDeadline:       defaultVerifyDeadline,
		CacheTTL:             defaultVerifyCacheTTL,
		RetrievalThreshold:   defaultVerifyRetrievalThreshold,
		SpeakerPriorStrength: defaultSpeakerScorePriorStrength,
	}
	var err error
	if v.Enabled, err = boolEnv("FACTCHECK_VERIFY_PATH"); err != nil {
		return VerifyPath{}, err
	}
	v.APIKey = getenv("FACTCHECK_VERIFY_API_KEY", "")
	v.Model = getenv("FACTCHECK_VERIFY_MODEL", v.Model)
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
	// The prior strength must stay positive (a non-positive k has no valid Beta
	// prior); the lower bound keeps it meaningfully above a single observation.
	if v.SpeakerPriorStrength, err = floatEnv("SPEAKER_SCORE_PRIOR_STRENGTH", v.SpeakerPriorStrength, 0.5, 1000); err != nil {
		return VerifyPath{}, err
	}
	return v, nil
}

// Check-worthiness defaults. The model is the cheapest fast Claude model,
// suitable for a binary check-worthy/not judgment over one short statement. This
// is the env-layer default; the checkworthy adapter keeps a matching default for
// direct construction and the two must stay in sync.
const defaultCheckWorthinessModel = "claude-haiku-4-5-20251001"

// CheckWorthiness holds the model stage of the check-worthiness gate. It is the
// optional upgrade to the gate's stage one: when off (the default) or keyless,
// the deterministic heuristic alone decides claim-worthiness, exactly as before.
// When active, a model judges whether a heuristic-accepted declarative is a
// check-worthy public claim rather than casual small talk. APIKey is a secret
// and comes from the environment only - never logged. Model selects the
// classifier model.
type CheckWorthiness struct {
	Enabled bool
	APIKey  string
	Model   string
}

// Active reports whether the model classifier should be wired: it is enabled and
// has the API key it needs. Wiring keys off this so an enabled-but-keyless
// configuration degrades to the heuristic-only gate rather than failing to
// start.
func (c CheckWorthiness) Active() bool {
	return c.Enabled && c.APIKey != ""
}

// LoadCheckWorthiness reads the model check-worthiness configuration from the
// environment, applying defaults. The secret is read but never logged.
func LoadCheckWorthiness() (CheckWorthiness, error) {
	c := CheckWorthiness{Model: defaultCheckWorthinessModel}
	var err error
	if c.Enabled, err = boolEnv("CHECKWORTHINESS_ENABLED"); err != nil {
		return CheckWorthiness{}, err
	}
	c.APIKey = getenv("CHECKWORTHINESS_API_KEY", "")
	c.Model = getenv("CHECKWORTHINESS_MODEL", c.Model)
	return c, nil
}

// defaultPoliticalRouterMinResults is the floor below which a routed source's
// result is considered thin and the Router broadens to web search. One
// authoritative passage is enough to keep an authoritative answer; below it the
// open web fills in.
const defaultPoliticalRouterMinResults = 1

// Political holds the French/EU political fact-checking mode flag and the routing
// knob the capstone (VER-103) wires behind it. The whole redesign rides
// FACTCHECK_POLITICAL (default off) so main stays shippable: with the flag off the
// locale is the default English behavior and the verify path (when active) runs its
// credibility-only stage unchanged, and with it on the live LLM stages prompt and
// reason in French, the transcriber biases toward French, and the verify path's
// per-claim stage routes through the political pipeline (classify -> route+retrieve
// -> two-axis verify). RouterMinResults is the thin-result floor below which the
// router broadens to web search.
type Political struct {
	Enabled          bool
	RouterMinResults int
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
	enabled, err := boolEnv("FACTCHECK_POLITICAL")
	if err != nil {
		return Political{}, err
	}
	minResults, err := intEnv("FACTCHECK_POLITICAL_ROUTER_MIN_RESULTS", defaultPoliticalRouterMinResults, 1, math.MaxInt32)
	if err != nil {
		return Political{}, err
	}
	return Political{Enabled: enabled, RouterMinResults: minResults}, nil
}

// CrawlAlerts holds the ingestion-fleet Slack alerting configuration. WebhookURL
// is the Slack incoming-webhook crawl runs announce themselves to; it is a secret
// sourced from the environment only and never logged. Empty disables alerting, so
// local runs without Slack are unaffected.
type CrawlAlerts struct {
	WebhookURL string
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
	return CrawlAlerts{WebhookURL: os.Getenv("SLACK_WEBHOOK_URL")}
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
	// defaultQueueVersion is the single active version when none is configured,
	// so a fresh local dev runs against embedding.jobs.v1.
	defaultQueueVersion = "1"
	// defaultCrawlQueueName is the base queue the category crawler publishes to,
	// kept separate from the embedding-jobs queue so the crawl worker and the
	// dump-pipeline fleet never consume each other's messages.
	defaultCrawlQueueName = "crawl.chunks"
	// defaultFactCheckQueueName is the base queue the fact-check-archive producer
	// publishes curated-claim jobs to, kept separate from the wiki crawl and
	// embedding queues so the fact-check worker never consumes a wiki chunk.
	defaultFactCheckQueueName = "factcheck.claims"
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
}

// VersionedName is the broker queue name for the active version: the base name
// with an explicit .v<version> suffix. Producer and worker both resolve it from
// the same configuration, so they bind to the same queue.
func (q Queue) VersionedName() string {
	return q.Name + ".v" + q.Version
}

// LoadQueue reads the broker configuration from the environment. RABBITMQ_URL is
// required; the queue name defaults to embedding.jobs, the max priority to 10
// (validated to [1, 255]) and the prefetch to 1 (validated non-negative). The
// versions default to a single "1"; RABBITMQ_QUEUE_VERSIONS overrides them as a
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
	return Queue{
		URL:           url,
		Name:          getenv(nameEnv, nameDefault),
		MaxPriority:   uint8(maxPriority),
		Prefetch:      prefetch,
		Version:       versions[len(versions)-1],
		KnownVersions: versions,
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
type EmbedWorker struct {
	Concurrency       int
	BatchSize         int
	BatchWait         time.Duration
	MaxAttempts       int
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
	return w, nil
}

// LoadCrawlWorker reads the crawl-worker configuration (CRAWL_WORKER_*). It
// reuses the EmbedWorker shape and shared defaults; the crawl worker embeds one
// chunk per job, so it never reads BatchSize/BatchWait, which keep their
// defaults. Only the env prefix differs from LoadEmbedWorker.
func LoadCrawlWorker() (EmbedWorker, error) {
	return loadWorkerCommon("CRAWL_WORKER", defaultWorker())
}

// defaultWorker is the worker configuration before any environment override.
func defaultWorker() EmbedWorker {
	return EmbedWorker{
		Concurrency:       defaultEmbedWorkerConcurrency,
		BatchSize:         defaultEmbedWorkerBatchSize,
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
	if w.BulkFraction, err = floatEnv("WIKI_DELTA_BULK_FRACTION", w.BulkFraction, 0, 1); err != nil {
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
)

// Crawl configures the category crawler. Categories are the seed category titles
// (e.g. "Category:Physics"); Project is the wiki project whose Action API is
// queried and whose host builds article URLs (defaults to WIKI_CORPUS); Corpus is
// the provenance tag written to wiki_chunks.corpus (defaults to "<project>-crawl"
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

	return Crawl{
		Categories:  categories,
		Project:     project,
		Corpus:      corpus,
		MaxDepth:    maxDepth,
		MaxPages:    maxPages,
		IncludeBody: includeBody,
		Shards:      shards,
		ShardIndex:  shardIndex,
	}, nil
}

// Crawl fact-checkability gate defaults: the gate is on by default so the corpus
// stays bounded to citable evidence, runs on the cheapest fast Claude model, and
// allows a modest number of in-flight judgments. The rate cap is unpaced by
// default; an operator sets it to bound Anthropic spend on a large crawl.
const (
	defaultCrawlCheckworthyModel       = "claude-haiku-4-5-20251001"
	defaultCrawlCheckworthyConcurrency = 8
)

// CrawlCheckworthy configures the producer-side fact-checkability gate. Enabled
// (default true) runs an LLM judgment on each chunk before publishing so only
// citable evidence reaches the broker; false publishes every chunk, the pre-gate
// behavior. Model selects the classifier; Concurrency caps in-flight judgments;
// RPM (0 = unpaced) caps the per-producer Anthropic call rate. APIKey is a secret
// read from CHECKWORTHY_API_KEY and never logged.
type CrawlCheckworthy struct {
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
	return c.Enabled && c.APIKey != ""
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
	apiKey := getenv("CHECKWORTHY_API_KEY", "")
	if enabled && apiKey == "" {
		return CrawlCheckworthy{}, fmt.Errorf("config: CRAWL_CHECKWORTHY is on but CHECKWORTHY_API_KEY is not set")
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
		Enabled:     enabled,
		APIKey:      apiKey,
		Model:       getenv("CRAWL_CHECKWORTHY_MODEL", defaultCrawlCheckworthyModel),
		Concurrency: concurrency,
		RPM:         rpm,
	}, nil
}

// defaultFactCheckLanguage filters the fact-check archive to French claims, the
// only jurisdiction this ingest targets.
const defaultFactCheckLanguage = "fr"

// FactCheckArchive configures the fact-check-archive producer that reads
// already-checked claims from the Google Fact Check Tools API into the curated
// claim DB. APIKey is the Google API key (sourced from the environment only,
// never logged); Queries are the comma-separated claims:search query terms to
// walk (a politician's name, a policy area); Language filters by claim language;
// MaxPages caps result pages followed per query (0 = follow every page).
type FactCheckArchive struct {
	APIKey   string
	Queries  []string
	Language string
	MaxPages int
}

// LoadFactCheckArchive reads the fact-check-archive producer configuration.
// FACTCHECK_API_KEY and FACTCHECK_QUERIES are required (the producer has nothing
// to ingest without a query); the rest default. Bad values fail fast at startup.
// The secret is read but never logged.
func LoadFactCheckArchive() (FactCheckArchive, error) {
	apiKey, err := requireEnv("FACTCHECK_API_KEY")
	if err != nil {
		return FactCheckArchive{}, err
	}
	raw, err := requireEnv("FACTCHECK_QUERIES")
	if err != nil {
		return FactCheckArchive{}, err
	}
	queries := make([]string, 0)
	for _, q := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(q); trimmed != "" {
			queries = append(queries, trimmed)
		}
	}
	if len(queries) == 0 {
		return FactCheckArchive{}, fmt.Errorf("config: FACTCHECK_QUERIES %q has no query", raw)
	}
	maxPages, err := intEnv("FACTCHECK_MAX_PAGES", 0, 0, math.MaxInt32)
	if err != nil {
		return FactCheckArchive{}, err
	}
	return FactCheckArchive{
		APIKey:   apiKey,
		Queries:  queries,
		Language: getenv("FACTCHECK_LANGUAGE", defaultFactCheckLanguage),
		MaxPages: maxPages,
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
