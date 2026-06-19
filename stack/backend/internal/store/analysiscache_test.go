package store

import (
	"bytes"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCache(t *testing.T) (*RedisCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisCache(client), mr
}

func TestRedisCachePutGetRoundTrip(t *testing.T) {
	t.Parallel()
	cache, _ := newTestCache(t)
	ctx := t.Context()

	want := []byte(`{"verdicts":["credible"]}`)
	if err := cache.Put(ctx, "video-1", want, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, found, err := cache.Get(ctx, "video-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected cache hit, got miss")
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestRedisCacheGetMiss(t *testing.T) {
	t.Parallel()
	cache, _ := newTestCache(t)

	got, found, err := cache.Get(t.Context(), "absent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("expected miss, got hit")
	}
	if got != nil {
		t.Fatalf("payload = %q, want nil", got)
	}
}

func TestRedisCacheTTLExpiry(t *testing.T) {
	t.Parallel()
	cache, mr := newTestCache(t)
	ctx := t.Context()

	if err := cache.Put(ctx, "video-1", []byte("payload"), time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Just before expiry the entry is still present.
	mr.FastForward(59 * time.Minute)
	if _, found, err := cache.Get(ctx, "video-1"); err != nil || !found {
		t.Fatalf("before expiry: found=%v err=%v, want found=true err=nil", found, err)
	}

	// Past the TTL the entry is gone and reads as a clean miss.
	mr.FastForward(2 * time.Minute)
	got, found, err := cache.Get(ctx, "video-1")
	if err != nil {
		t.Fatalf("after expiry Get: %v", err)
	}
	if found {
		t.Fatalf("expected miss after TTL, got hit with %q", got)
	}
}

func TestRedisCachePutRejectsNonPositiveTTL(t *testing.T) {
	t.Parallel()
	cache, _ := newTestCache(t)

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := cache.Put(t.Context(), "video-1", []byte("p"), ttl); err == nil {
			t.Fatalf("ttl %s: expected error, got nil", ttl)
		}
	}
}

func TestRedisCacheGetWrapsBackendError(t *testing.T) {
	t.Parallel()
	cache, mr := newTestCache(t)
	mr.Close() // server gone: every command now fails at the transport.

	if _, _, err := cache.Get(t.Context(), "video-1"); err == nil {
		t.Fatal("expected error when backend is down, got nil")
	}
}

func TestNoopCache(t *testing.T) {
	t.Parallel()
	var cache NoopCache
	ctx := t.Context()

	if err := cache.Put(ctx, "video-1", []byte("payload"), time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, found, err := cache.Get(ctx, "video-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("noop cache must always miss")
	}
	if got != nil {
		t.Fatalf("payload = %q, want nil", got)
	}
}

// NoopCache and RedisCache must both satisfy AnalysisCache.
var (
	_ AnalysisCache = NoopCache{}
	_ AnalysisCache = (*RedisCache)(nil)
)
