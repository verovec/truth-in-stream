package domain

import (
	"math"
	"testing"
)

func TestValidCosineThreshold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   float64
		want bool
	}{
		{"lower bound", -1, true},
		{"upper bound", 1, true},
		{"mid", 0.4, true},
		{"below range", -1.0001, false},
		{"above range", 1.0001, false},
		{"NaN", math.NaN(), false},
		{"positive infinity", math.Inf(1), false},
		{"negative infinity", math.Inf(-1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidCosineThreshold(tc.in); got != tc.want {
				t.Errorf("ValidCosineThreshold(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
