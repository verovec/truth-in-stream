// Package config loads service configuration from the environment.
package config

import (
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

// Config holds the runtime configuration for the server.
type Config struct {
	Port        string
	DatabaseURL string
}

// Load reads configuration from the environment, applying defaults and
// failing fast when a required variable is missing.
func Load() (Config, error) {
	dbURL, err := requireEnv("DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	return Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: dbURL,
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
	if raw := os.Getenv("MATCH_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Match{}, fmt.Errorf("config: MATCH_TIMEOUT %q: %w", raw, err)
		}
		if d <= 0 {
			return Match{}, fmt.Errorf("config: MATCH_TIMEOUT must be positive, got %s", d)
		}
		m.Timeout = d
	}
	return m, nil
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

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("config: %s is required", key)
	}
	return v, nil
}
