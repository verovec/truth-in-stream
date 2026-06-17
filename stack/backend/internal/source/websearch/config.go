package websearch

import (
	"fmt"
	"os"
	"time"
)

// Env var names for the web-search pack. The API key is a secret read from the
// environment only and never logged.
const (
	envAPIKey  = "WEBSEARCH_API_KEY"
	envTimeout = "WEBSEARCH_TIMEOUT"
)

// LoadConfig reads the web-search pack configuration from the environment. The
// API key is required; an unset key is an error so wiring fails fast rather than
// the first claim. The timeout is optional and falls back to the package
// default. The key value itself is never included in an error or log.
func LoadConfig() (Config, error) {
	apiKey := os.Getenv(envAPIKey)
	if apiKey == "" {
		return Config{}, fmt.Errorf("config: %s is required", envAPIKey)
	}
	timeout, err := optionalDuration(envTimeout)
	if err != nil {
		return Config{}, err
	}
	return Config{APIKey: apiKey, Timeout: timeout}, nil
}

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
