package config

import (
	"testing"
	"time"
)

func TestLoadScheduleDefaults(t *testing.T) {
	clearScheduleEnv(t)

	cfg, err := LoadSchedule()
	if err != nil {
		t.Fatalf("LoadSchedule: %v", err)
	}

	if cfg.Wikipedia.Enabled || cfg.Factcheck.Enabled || cfg.Scrutins.Enabled {
		t.Fatalf("expected every source disabled by default, got %+v", cfg)
	}
	if cfg.Wikipedia.Cron != defaultWikipediaCron {
		t.Fatalf("wikipedia cron = %q, want %q", cfg.Wikipedia.Cron, defaultWikipediaCron)
	}
	if cfg.Factcheck.Cron != defaultFactcheckCron {
		t.Fatalf("factcheck cron = %q, want %q", cfg.Factcheck.Cron, defaultFactcheckCron)
	}
	if cfg.Scrutins.Cron != defaultScrutinsCron {
		t.Fatalf("scrutins cron = %q, want %q", cfg.Scrutins.Cron, defaultScrutinsCron)
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

	cfg, err := LoadSchedule()
	if err != nil {
		t.Fatalf("LoadSchedule: %v", err)
	}
	if !cfg.Wikipedia.Enabled {
		t.Fatal("expected wikipedia enabled by override")
	}
	if cfg.Factcheck.Cron != "*/15 * * * *" {
		t.Fatalf("factcheck cron = %q, want override", cfg.Factcheck.Cron)
	}
	if cfg.Jitter != 10*time.Second {
		t.Fatalf("jitter = %s, want 10s", cfg.Jitter)
	}
}

func TestLoadScheduleRejectsBadBool(t *testing.T) {
	clearScheduleEnv(t)
	t.Setenv("SCHEDULE_SCRUTINS_ENABLED", "maybe")

	if _, err := LoadSchedule(); err == nil {
		t.Fatal("expected an error for a non-boolean enable flag, got nil")
	}
}

func TestLoadScheduleRejectsJitterAboveMax(t *testing.T) {
	clearScheduleEnv(t)
	t.Setenv("SCHEDULE_JITTER", "2h")

	if _, err := LoadSchedule(); err == nil {
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
