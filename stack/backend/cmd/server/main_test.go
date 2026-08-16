package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

func TestLiveAllowedOrigins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cors string
		want []string
	}{
		{name: "empty enforces same-origin only", cors: "", want: nil},
		{name: "derives the dev frontend host", cors: "http://localhost:3000", want: []string{"localhost:3000"}},
		{name: "keeps host and port from an https origin", cors: "https://app.example.com:8443", want: []string{"app.example.com:8443"}},
		{name: "a value without a scheme yields no host", cors: "localhost:3000", want: nil},
		{name: "a single-slash typo yields no host", cors: "http:/localhost:3000", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := liveAllowedOrigins(tc.cors); !slices.Equal(got, tc.want) {
				t.Errorf("liveAllowedOrigins(%q) = %v, want %v", tc.cors, got, tc.want)
			}
		})
	}
}

// stubSegmentMatcher is the fallback passed to buildVerifyMatcher; its identity is
// what the inactive-path assertion checks for.
type stubSegmentMatcher struct{}

func (stubSegmentMatcher) Match(context.Context, string) (service.MatchResult, error) {
	return service.MatchResult{}, nil
}

// validMatchCfg is a Match configuration NewMatcher accepts, so the active branch
// builds without tripping matcher validation.
func validMatchCfg() config.Match {
	return config.Match{
		TopK:                  5,
		ScoreThreshold:        0.5,
		EvidenceTopK:          5,
		EvidenceThreshold:     0.6,
		MaxResults:            5,
		EmbedConcurrency:      4,
		Timeout:               time.Second,
		ConfidenceClusterSize: 5,
		ConfidenceLeadWeight:  1,
		ConfidenceBodyWeight:  0.6,
	}
}

func TestBuildVerifyMatcherInactiveReturnsFallback(t *testing.T) {
	t.Parallel()
	fallback := stubSegmentMatcher{}
	got, err := buildVerifyMatcher(config.VerifyPath{}, validMatchCfg(), config.Rerank{}, nil, nil, fallback, nil)
	if err != nil {
		t.Fatalf("buildVerifyMatcher: %v", err)
	}
	if got != service.SegmentMatcher(fallback) {
		t.Error("inactive verify path must return the fallback matcher unchanged")
	}
}

func TestBuildVerifyMatcherActiveBuildsDedicatedMatcher(t *testing.T) {
	t.Parallel()
	fallback := stubSegmentMatcher{}
	cfg := config.VerifyPath{Enabled: true, APIKey: "sk-test", RetrievalThreshold: 0.45}
	got, err := buildVerifyMatcher(cfg, validMatchCfg(), config.Rerank{}, nil, nil, fallback, nil)
	if err != nil {
		t.Fatalf("buildVerifyMatcher: %v", err)
	}
	if got == nil {
		t.Fatal("active verify path must build a matcher")
	}
	if got == service.SegmentMatcher(fallback) {
		t.Error("active verify path must build a dedicated matcher, not reuse the fallback")
	}
}
