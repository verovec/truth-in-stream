// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
)

// Config holds the runtime configuration for the server.
type Config struct {
	Port        string
	DatabaseURL string
}

// Load reads configuration from the environment, applying defaults and
// failing fast when a required variable is missing.
func Load() (Config, error) {
	cfg := Config{
		Port:        getenv("PORT", "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: DATABASE_URL is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
