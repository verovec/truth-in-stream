package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"
)

// Pre-analysis defaults: 1.0 submits extracted audio at realtime, the only
// rate AssemblyAI Universal-Streaming documents as supported; one concurrent
// run keeps a single transcriber session and one verify-load unit in flight;
// the 4 h run timeout must exceed the longest analysable video, since at
// realtime pacing a run takes about as long as the video itself.
const (
	defaultPreanalysisPacingFactor  = 1.0
	defaultPreanalysisMaxConcurrent = 1
	defaultPreanalysisRunTimeout    = 4 * time.Hour
)

// Preanalysis holds the video pre-analysis pipeline settings.
type Preanalysis struct {
	// PacingFactor is the multiple of realtime at which extracted audio is
	// submitted to the streaming transcriber.
	PacingFactor float64
	// MaxConcurrent bounds simultaneous pre-analysis runs process-wide; queued
	// starts hold the analysing status until a slot frees.
	MaxConcurrent int
	// RunTimeout bounds one whole run; a run that overruns it is failed and
	// stays re-runnable.
	RunTimeout time.Duration
}

// LoadPreanalysis reads the pre-analysis configuration from the environment.
// PREANALYSIS_PACING_FACTOR overrides the realtime default and must be in
// (0, 1]: AssemblyAI Universal-Streaming enforces an audio transmission-rate
// check ("Audio Transmission Rate Exceeded", surfaced in the 3007 error
// family) that closes a session receiving audio faster than realtime, so a
// factor above 1.0 is a documented failure mode, rejected here rather than
// shipped. Values below 1.0 slow submission and exist for debugging. Relax
// the upper bound only if AssemblyAI documents faster-than-realtime
// tolerance.
//
// PREANALYSIS_MAX_CONCURRENT overrides the global cap on simultaneous runs
// (default 1, must be a positive integer). PREANALYSIS_RUN_TIMEOUT overrides
// the per-run budget (default 4h, must be a positive Go duration that exceeds
// the longest video to analyse at the configured pacing).
func LoadPreanalysis() (Preanalysis, error) {
	cfg := Preanalysis{
		PacingFactor:  defaultPreanalysisPacingFactor,
		MaxConcurrent: defaultPreanalysisMaxConcurrent,
		RunTimeout:    defaultPreanalysisRunTimeout,
	}
	if raw := os.Getenv("PREANALYSIS_PACING_FACTOR"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return Preanalysis{}, fmt.Errorf("config: PREANALYSIS_PACING_FACTOR %q: %w", raw, err)
		}
		// The inverted comparison also rejects NaN, which ParseFloat accepts.
		if !(v > 0 && v <= 1) {
			return Preanalysis{}, fmt.Errorf("config: PREANALYSIS_PACING_FACTOR must be in (0, 1], got %v", v)
		}
		cfg.PacingFactor = v
	}
	var err error
	if cfg.MaxConcurrent, err = intEnv("PREANALYSIS_MAX_CONCURRENT", cfg.MaxConcurrent, 1, math.MaxInt32); err != nil {
		return Preanalysis{}, err
	}
	if raw := os.Getenv("PREANALYSIS_RUN_TIMEOUT"); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			return Preanalysis{}, fmt.Errorf("config: PREANALYSIS_RUN_TIMEOUT %q: %w", raw, err)
		}
		if v <= 0 {
			return Preanalysis{}, fmt.Errorf("config: PREANALYSIS_RUN_TIMEOUT must be positive, got %s", v)
		}
		cfg.RunTimeout = v
	}
	return cfg, nil
}
