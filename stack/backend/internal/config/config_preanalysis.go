package config

import (
	"fmt"
	"os"
	"strconv"
)

// Pre-analysis pacing default: 1.0 submits extracted audio at realtime, the
// only rate AssemblyAI Universal-Streaming documents as supported.
const defaultPreanalysisPacingFactor = 1.0

// Preanalysis holds the video pre-analysis pipeline settings.
type Preanalysis struct {
	// PacingFactor is the multiple of realtime at which extracted audio is
	// submitted to the streaming transcriber.
	PacingFactor float64
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
func LoadPreanalysis() (Preanalysis, error) {
	cfg := Preanalysis{PacingFactor: defaultPreanalysisPacingFactor}
	raw := os.Getenv("PREANALYSIS_PACING_FACTOR")
	if raw == "" {
		return cfg, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return Preanalysis{}, fmt.Errorf("config: PREANALYSIS_PACING_FACTOR %q: %w", raw, err)
	}
	// The inverted comparison also rejects NaN, which ParseFloat accepts.
	if !(v > 0 && v <= 1) {
		return Preanalysis{}, fmt.Errorf("config: PREANALYSIS_PACING_FACTOR must be in (0, 1], got %v", v)
	}
	cfg.PacingFactor = v
	return cfg, nil
}
