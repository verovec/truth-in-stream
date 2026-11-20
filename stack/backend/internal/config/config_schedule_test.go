package config

import (
	"testing"
	"time"
)

// testSpecs mirrors the schedulable sources the connector registry supplies,
// without importing it (config must stay registry-agnostic).
func testSpecs() []ScheduleSpec {
	return []ScheduleSpec{
		{Name: "wikipedia", EnvPrefix: "WIKIPEDIA", DefaultCron: "0 3 * * *"},
		{Name: "factcheck", EnvPrefix: "FACTCHECK", DefaultCron: "0 4 * * *"},
		{Name: "scrutins", EnvPrefix: "SCRUTINS", DefaultCron: "30 4 * * *"},
	}
}

func TestLoadScheduleDefaults(t *testing.T) {
	clearScheduleEnv(t)

	cfg, err := LoadSchedule(testSpecs())
	if err != nil {
		t.Fatalf("LoadSchedule: %v", err)
	}

	for _, name := range []string{"wikipedia", "factcheck", "scrutins"} {
		src, ok := cfg.Source(name)
		if !ok {
			t.Fatalf("source %q missing from schedule", name)
		}
		if src.Enabled {
			t.Fatalf("source %q enabled by default, want disabled", name)
		}
	}
	if src, _ := cfg.Source("wikipedia"); src.Cron != "0 3 * * *" {
		t.Fatalf("wikipedia cron = %q, want default", src.Cron)
	}
	if src, _ := cfg.Source("factcheck"); src.Cron != "0 4 * * *" {
		t.Fatalf("factcheck cron = %q, want default", src.Cron)
	}
	if src, _ := cfg.Source("scrutins"); src.Cron != "30 4 * * *" {
		t.Fatalf("scrutins cron = %q, want default", src.Cron)
	}
	if cfg.Jitter != defaultScheduleJitter {
		t.Fatalf("jitter = %s, want %s", cfg.Jitter, defaultScheduleJitter)
	}
}

func TestLoadScheduleOverrides(t *testing.T) {
	clearScheduleEnv(t)
	t.Setenv("SCHEDULE_WIKIPEDIA_ENABLED", "true")
	t.Setenv("SCHEDULE_FACTCHECK_CRON", "*/15 * * * *")
	t.Setenv("SCHEDULE_JITTER", "10s")

	cfg, err := LoadSchedule(testSpecs())
	if err != nil {
		t.Fatalf("LoadSchedule: %v", err)
	}
	if src, _ := cfg.Source("wikipedia"); !src.Enabled {
		t.Fatal("expected wikipedia enabled by override")
	}
	if src, _ := cfg.Source("factcheck"); src.Cron != "*/15 * * * *" {
		t.Fatalf("factcheck cron = %q, want override", src.Cron)
	}
	if cfg.Jitter != 10*time.Second {
		t.Fatalf("jitter = %s, want 10s", cfg.Jitter)
	}
}

func TestLoadScheduleRejectsBadBool(t *testing.T) {
	clearScheduleEnv(t)
	t.Setenv("SCHEDULE_SCRUTINS_ENABLED", "maybe")

	if _, err := LoadSchedule(testSpecs()); err == nil {
		t.Fatal("expected an error for a non-boolean enable flag, got nil")
	}
}

func TestLoadScheduleRejectsJitterAboveMax(t *testing.T) {
	clearScheduleEnv(t)
	t.Setenv("SCHEDULE_JITTER", "2h")

	if _, err := LoadSchedule(testSpecs()); err == nil {
		t.Fatal("expected an error for a jitter above the cap, got nil")
	}
}

// clearScheduleEnv blanks every scheduler env var so a test starts from defaults
// regardless of the ambient environment.
func clearScheduleEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SCHEDULE_WIKIPEDIA_ENABLED", "SCHEDULE_WIKIPEDIA_CRON",
		"SCHEDULE_FACTCHECK_ENABLED", "SCHEDULE_FACTCHECK_CRON",
		"SCHEDULE_SCRUTINS_ENABLED", "SCHEDULE_SCRUTINS_CRON",
		"SCHEDULE_JITTER",
	} {
		t.Setenv(k, "")
	}
}
