package main

import (
	"slices"
	"testing"
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
