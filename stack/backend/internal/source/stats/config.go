package stats

import (
	"fmt"
	"os"
	"time"
)

// Env var names for the optional stats-pack tuning. The endpoints are keyless,
// so there is no secret here; these only tune latency and caching.
const (
	envTimeout  = "STATS_TIMEOUT"
	envCacheTTL = "STATS_CACHE_TTL"
)

// LoadConfig reads the stats pack's optional tuning from the environment,
// failing fast on a malformed duration. Unset values keep the package defaults,
// so the zero environment yields a usable pack. The endpoints need no API key,
// so nothing secret is read or logged.
func LoadConfig() (Config, error) {
	timeout, err := optionalDuration(envTimeout)
	if err != nil {
		return Config{}, err
	}
	ttl, err := optionalDuration(envCacheTTL)
	if err != nil {
		return Config{}, err
	}
	return Config{Timeout: timeout, CacheTTL: ttl}, nil
}

// optionalDuration parses key as a Go duration, returning zero (the "use
// default" sentinel) when unset and rejecting a malformed or non-positive value.
func optionalDuration(key string) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return 0, nil
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
