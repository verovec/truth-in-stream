package vectorbench

import (
	"testing"
	"time"
)

func TestRecallAtK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		exact   [][]int64
		approx  [][]int64
		want    float64
		wantErr bool
	}{
		{
			name:   "perfect recall",
			exact:  [][]int64{{1, 2, 3}},
			approx: [][]int64{{3, 1, 2}},
			want:   1,
		},
		{
			name:   "zero recall",
			exact:  [][]int64{{1, 2, 3}},
			approx: [][]int64{{4, 5, 6}},
			want:   0,
		},
		{
			name:   "partial recall",
			exact:  [][]int64{{1, 2, 3}},
			approx: [][]int64{{1, 2, 9}},
			want:   2.0 / 3.0,
		},
		{
			name:   "mean over queries",
			exact:  [][]int64{{1, 2}, {3, 4}},
			approx: [][]int64{{1, 2}, {5, 6}},
			want:   0.5,
		},
		{
			name:   "approx shorter than exact counts misses",
			exact:  [][]int64{{1, 2, 3, 4}},
			approx: [][]int64{{1, 2}},
			want:   0.5,
		},
		{
			name:    "mismatched query counts",
			exact:   [][]int64{{1}},
			approx:  [][]int64{{1}, {2}},
			wantErr: true,
		},
		{
			name:    "no queries",
			exact:   [][]int64{},
			approx:  [][]int64{},
			wantErr: true,
		},
		{
			name:    "empty exact neighbor list",
			exact:   [][]int64{{}},
			approx:  [][]int64{{1}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := RecallAtK(tc.exact, tc.approx)
			if tc.wantErr {
				if err == nil {
					t.Fatal("RecallAtK returned nil error")
				}
				return
			}
			if err != nil {
				t.Fatalf("RecallAtK: %v", err)
			}
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("RecallAtK = %f, want %f", got, tc.want)
			}
		})
	}
}

func TestPercentile(t *testing.T) {
	t.Parallel()
	hundred := make([]time.Duration, 100)
	for i := range 100 {
		hundred[i] = time.Duration(i+1) * time.Millisecond
	}
	tests := []struct {
		name    string
		samples []time.Duration
		p       float64
		want    time.Duration
	}{
		{"p50 of 1..100ms", hundred, 50, 50 * time.Millisecond},
		{"p95 of 1..100ms", hundred, 95, 95 * time.Millisecond},
		{"p100 of 1..100ms", hundred, 100, 100 * time.Millisecond},
		{"single sample", []time.Duration{7 * time.Millisecond}, 95, 7 * time.Millisecond},
		{"unsorted input", []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}, 50, 20 * time.Millisecond},
		{"empty samples", nil, 50, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Percentile(tc.samples, tc.p); got != tc.want {
				t.Errorf("Percentile(%v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}
