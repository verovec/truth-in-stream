package stats

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv(envTimeout, "")
	t.Setenv(envCacheTTL, "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Timeout != 0 || cfg.CacheTTL != 0 {
		t.Fatalf("unset env should yield zero (use-default) config, got %+v", cfg)
	}
}

func TestLoadConfigParses(t *testing.T) {
	t.Setenv(envTimeout, "3s")
	t.Setenv(envCacheTTL, "1m")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Timeout != 3*time.Second {
		t.Errorf("timeout: got %s", cfg.Timeout)
	}
	if cfg.CacheTTL != time.Minute {
		t.Errorf("cache ttl: got %s", cfg.CacheTTL)
	}
}

func TestLoadConfigRejectsBad(t *testing.T) {
	t.Setenv(envTimeout, "nope")
	if _, err := LoadConfig(); err == nil {
		t.Fatalf("want error on malformed duration")
	}
}

func TestLoadConfigRejectsNonPositive(t *testing.T) {
	t.Setenv(envTimeout, "0s")
	if _, err := LoadConfig(); err == nil {
		t.Fatalf("want error on non-positive duration")
	}
}
