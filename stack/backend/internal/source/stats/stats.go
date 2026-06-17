package stats

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/verovec/truth-in-stream/backend/internal/source"
)

// Hint keys the stats pack reads off a source.Query to select which series to
// fetch. A statistic claim is routed (card J) with the series already resolved;
// this pack does not guess series from free text. HintEurostatFilterPrefix is a
// prefix: a key "eurostat_filter_geo" sets the geo dimension to its value.
const (
	HintINSEEIDBANK          = "insee_idbank"
	HintEurostatDataset      = "eurostat_dataset"
	HintEurostatFilterPrefix = "eurostat_filter_"
)

// defaultTimeout bounds a single upstream call. Live retrieval sits on the
// fact-check latency budget, so a slow statistics API must not stall the path.
const defaultTimeout = 8 * time.Second

// defaultLastN bounds how many recent INSEE observations to request: enough
// adjacent periods for the verifier to see a cherry-pick, not the full history.
const defaultLastN = 16

// defaultCacheTTL is how long a fetched series is reused within a session. The
// same series is cited repeatedly across a debate; a short TTL collapses those
// into one upstream call without serving a stale figure for long.
const defaultCacheTTL = 10 * time.Minute

// Config tunes the stats pack. Zero values fall back to the defaults, so the
// zero Config is usable.
type Config struct {
	Timeout  time.Duration
	LastN    int
	CacheTTL time.Duration
	// BaseURLs overrides the upstream endpoints, for tests. Production leaves
	// them empty so the real keyless endpoints are used.
	INSEEBaseURL    string
	EurostatBaseURL string
}

// Pack retrieves official statistics series (INSEE BDM, Eurostat) as evidence.
// It satisfies source.Retriever. Each returned passage renders the whole series
// so the verifier can see surrounding periods, not just the cited point.
type Pack struct {
	insee    *inseeClient
	eurostat *eurostatClient
	cache    *seriesCache
}

// New builds a stats pack. The HTTP client carries the per-source timeout so a
// slow upstream cannot exceed the budget even if the caller's context has none.
func New(cfg Config) *Pack {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	lastN := cfg.LastN
	if lastN <= 0 {
		lastN = defaultLastN
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	httpClient := &http.Client{Timeout: timeout}
	return &Pack{
		insee:    &inseeClient{httpClient: httpClient, baseURL: cfg.INSEEBaseURL, lastN: lastN},
		eurostat: &eurostatClient{httpClient: httpClient, baseURL: cfg.EurostatBaseURL},
		cache:    newSeriesCache(ttl),
	}
}

// Index of each provider's evidence within a stats retrieval. Each provider
// returns at most one passage, so a fixed slot per provider keeps an
// EvidenceID's index stable regardless of which providers a query selects or
// which one fails - the determinism the EvidenceID contract promises.
const (
	inseeEvidenceIndex    = 0
	eurostatEvidenceIndex = 1
)

// Kind reports the adapter-level source family. The stats pack serves every
// statistic claim; the per-evidence EvidenceID kind (KindStatsINSEE /
// KindStatsEurostat) distinguishes the provider that answered.
func (p *Pack) Kind() source.Kind { return source.KindStats }

// Retrieve fetches every series the query's hints select and returns one
// evidence passage per series. INSEE and Eurostat are independent; a failure
// from one does not suppress the other. A query selecting neither returns no
// evidence (not an error). When every selected source fails, the joined errors
// are returned so no upstream failure is silently dropped.
func (p *Pack) Retrieve(ctx context.Context, q source.Query) ([]source.Evidence, error) {
	out := make([]source.Evidence, 0, 2)
	var errs []error

	if idbank, ok := q.Hint(HintINSEEIDBANK); ok && idbank != "" {
		ev, err := p.retrieveINSEE(ctx, idbank)
		if err != nil {
			errs = append(errs, err)
		} else {
			out = append(out, ev)
		}
	}
	if dataset, ok := q.Hint(HintEurostatDataset); ok && dataset != "" {
		ev, err := p.retrieveEurostat(ctx, dataset, eurostatFilters(q))
		if err != nil {
			errs = append(errs, err)
		} else {
			out = append(out, ev)
		}
	}

	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func (p *Pack) retrieveINSEE(ctx context.Context, idbank string) (source.Evidence, error) {
	series, err := p.cache.fetch(ctx, "insee\x00"+idbank, func(ctx context.Context) (Series, error) {
		return p.insee.fetch(ctx, idbank)
	})
	if err != nil {
		return source.Evidence{}, err
	}
	return toEvidence(series, source.KindStatsINSEE, inseeSourceName, inseeEvidenceIndex), nil
}

func (p *Pack) retrieveEurostat(ctx context.Context, dataset string, filters map[string]string) (source.Evidence, error) {
	series, err := p.cache.fetch(ctx, "eurostat\x00"+dataset+"\x00"+filterKey(filters), func(ctx context.Context) (Series, error) {
		return p.eurostat.fetch(ctx, dataset, filters)
	})
	if err != nil {
		return source.Evidence{}, err
	}
	return toEvidence(series, source.KindStatsEurostat, eurostatSourceName, eurostatEvidenceIndex), nil
}

// toEvidence renders a series into an evidence passage with a stable id and
// provenance. The id is minted from the series' own source id, so the same claim
// over the same series yields the same id across runs.
func toEvidence(series Series, kind source.Kind, name string, index int) source.Evidence {
	return source.Evidence{
		ID:      source.NewEvidenceID(kind, series.SourceID, index),
		Passage: series.render(),
		Source: source.Source{
			Name: name,
			URL:  series.URL,
			Date: series.LastUpdated,
		},
	}
}

// eurostatFilters extracts the dimension filters from the query hints.
func eurostatFilters(q source.Query) map[string]string {
	filters := make(map[string]string)
	for k, v := range q.Hints {
		if dim, ok := strings.CutPrefix(k, HintEurostatFilterPrefix); ok && dim != "" {
			filters[dim] = v
		}
	}
	return filters
}

// filterKey is a deterministic cache-key fragment for a filter set.
func filterKey(filters map[string]string) string {
	if len(filters) == 0 {
		return ""
	}
	keys := make([]string, 0, len(filters))
	for k := range filters {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(filters[k])
	}
	return b.String()
}

// cacheEntry holds a fetched series and when it expires.
type cacheEntry struct {
	series  Series
	expires time.Time
}

// seriesCache is a small TTL cache so a series cited repeatedly within a session
// is fetched once. A singleflight group collapses concurrent misses for the same
// key into a single upstream call, so a burst of slots citing the same series in
// the same instant does not stampede the upstream API. Safe for concurrent use
// by the two retrieval pools.
type seriesCache struct {
	ttl    time.Duration
	mu     sync.Mutex
	m      map[string]cacheEntry
	flight singleflight.Group
}

func newSeriesCache(ttl time.Duration) *seriesCache {
	return &seriesCache{ttl: ttl, m: make(map[string]cacheEntry)}
}

// fetch returns the cached series for key when it is live, otherwise calls load
// once across all concurrent callers for that key, stores the result, and
// returns it. A load error is not cached.
func (c *seriesCache) fetch(ctx context.Context, key string, load func(context.Context) (Series, error)) (Series, error) {
	if series, ok := c.lookup(key); ok {
		return series, nil
	}

	// The load runs under a context detached from the triggering caller's: a
	// singleflight invocation is shared by every concurrent caller for the key,
	// so binding it to one caller's context would let that caller's cancellation
	// abort the load for all the others. The http.Client timeout still bounds the
	// upstream call.
	loadCtx := context.WithoutCancel(ctx)
	v, err, _ := c.flight.Do(key, func() (any, error) {
		// Re-check under the flight: a prior holder may have just stored it.
		if series, ok := c.lookup(key); ok {
			return series, nil
		}
		series, err := load(loadCtx)
		if err != nil {
			return Series{}, err
		}
		c.store(key, series)
		return series, nil
	})
	if err != nil {
		return Series{}, err
	}
	return v.(Series), nil
}

func (c *seriesCache) lookup(key string) (Series, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.m[key]; ok && time.Now().Before(entry.expires) {
		return entry.series, true
	}
	return Series{}, false
}

func (c *seriesCache) store(key string, series Series) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = cacheEntry{series: series, expires: time.Now().Add(c.ttl)}
}
