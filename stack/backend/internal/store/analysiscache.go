// Package store holds the byte-oriented analysis cache that lets a completed
// imported video's transcript and verdicts be replayed without re-running the
// live pipeline. It is the foundation the snapshot capture and cache-hit replay
// cards build on; this package owns only the cache abstraction and its Redis and
// no-op implementations, never any HTTP type.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// AnalysisCache is the byte-oriented contract the analysis cache exposes to its
// callers. It is deliberately payload-agnostic: the snapshot type and its
// encoding live with the capture logic in a later card, so this layer neither
// knows nor cares what the bytes mean. Get reports a miss with found=false and a
// nil error; an error is reserved for an actual backend failure.
type AnalysisCache interface {
	Get(ctx context.Context, videoID string) (payload []byte, found bool, err error)
	Put(ctx context.Context, videoID string, payload []byte, ttl time.Duration) error
}

// NoopCache is the disabled-cache implementation, selected when no Redis is
// configured or it is unreachable. Every Get is a miss and every Put is
// discarded, so the service behaves exactly as it does with no cache at all. Its
// zero value is ready to use.
type NoopCache struct{}

// Get always reports a miss.
func (NoopCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

// Put discards the payload.
func (NoopCache) Put(context.Context, string, []byte, time.Duration) error {
	return nil
}

// keyPrefix namespaces every analysis-cache entry under a versioned prefix so a
// future payload-format change can bump the version without colliding with stale
// keys, and so the cache shares a Redis/Valkey instance with other uses without
// key collisions.
const keyPrefix = "analysis:v1:"

// RedisCache is the Redis/Valkey-backed AnalysisCache. go-redis/v9 speaks a wire
// protocol both Redis OSS and Valkey serve, so the same client drives the local
// dev Redis container and the production managed Valkey with no engine-specific
// code.
type RedisCache struct {
	client redis.UniversalClient
}

// NewRedisCache wraps an already-connected go-redis client as an AnalysisCache.
// The caller owns the client's lifecycle (it builds it from REDIS_URL, pings it,
// and closes it), keeping connection and configuration concerns in the wiring
// layer where the secret URL is read.
func NewRedisCache(client redis.UniversalClient) *RedisCache {
	return &RedisCache{client: client}
}

// Get fetches the cached payload for videoID. A missing key is a clean miss
// (nil, false, nil); any other backend error is wrapped and returned.
func (c *RedisCache) Get(ctx context.Context, videoID string) ([]byte, bool, error) {
	payload, err := c.client.Get(ctx, keyPrefix+videoID).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("analysis cache get %q: %w", videoID, err)
	}
	return payload, true, nil
}

// Put stores payload for videoID under the given TTL. A non-positive TTL would
// persist the entry forever, defeating the bounded-replay-window design, so it is
// rejected rather than silently written without expiry.
func (c *RedisCache) Put(ctx context.Context, videoID string, payload []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("analysis cache put %q: ttl must be positive, got %s", videoID, ttl)
	}
	if err := c.client.Set(ctx, keyPrefix+videoID, payload, ttl).Err(); err != nil {
		return fmt.Errorf("analysis cache put %q: %w", videoID, err)
	}
	return nil
}
