//go:build !localinference

package nli

import "context"

// Scorer is the stub for pure-Go builds. New never returns one; the type
// exists so callers compile identically with and without the build tag.
type Scorer struct{}

// New reports the scorer unavailable in a build without the localinference
// tag. The caller's fail-open wiring keeps the LLM-first verify path.
func New(Config) (*Scorer, error) {
	return nil, ErrUnavailable
}

// ScoreStances is unreachable through New in this build; it satisfies the
// service port for callers that hold the concrete type.
func (s *Scorer) ScoreStances(context.Context, string, []string) ([]Stance, error) {
	return nil, ErrUnavailable
}

// Close releases nothing in the stub build.
func (s *Scorer) Close() error {
	return nil
}
