// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
)

// Config holds the runtime configuration for the server.
type Config struct {
	Port        string
	LadybugPath string
}

// Load reads configuration from the environment, applying defaults and
// failing fast when a required variable is missing.
func Load() (Config, error) {
	cfg := Config{
		Port:        getenv("PORT", "8080"),
		LadybugPath: os.Getenv("LADYBUG_PATH"),
	}
	if cfg.LadybugPath == "" {
		return Config{}, fmt.Errorf("config: LADYBUG_PATH is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
