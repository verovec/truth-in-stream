package config

import (
	"testing"
	"time"
)

func TestLoadPreanalysis(t *testing.T) {
	defaults := Preanalysis{PacingFactor: 1.0, MaxConcurrent: 1, RunTimeout: 4 * time.Hour}
	withPacing := func(v float64) Preanalysis {
		cfg := defaults
		cfg.PacingFactor = v
		return cfg
	}
	tests := []struct {
		name    string
		env     map[string]string
		want    Preanalysis
		wantErr bool
	}{
		{
			name: "defaults: realtime, one slot, 4h budget",
			want: defaults,
		},
		{
			name: "slower factor accepted",
			env:  map[string]string{"PREANALYSIS_PACING_FACTOR": "0.5"},
			want: withPacing(0.5),
		},
		{
			name: "explicit realtime accepted",
			env:  map[string]string{"PREANALYSIS_PACING_FACTOR": "1"},
			want: withPacing(1.0),
		},
		{
			name:    "faster than realtime rejected",
			env:     map[string]string{"PREANALYSIS_PACING_FACTOR": "1.5"},
			wantErr: true,
		},
		{
			name:    "zero rejected",
			env:     map[string]string{"PREANALYSIS_PACING_FACTOR": "0"},
			wantErr: true,
		},
		{
			name:    "negative rejected",
			env:     map[string]string{"PREANALYSIS_PACING_FACTOR": "-1"},
			wantErr: true,
		},
		{
			name:    "NaN rejected",
			env:     map[string]string{"PREANALYSIS_PACING_FACTOR": "NaN"},
			wantErr: true,
		},
		{
			name:    "non-numeric rejected",
			env:     map[string]string{"PREANALYSIS_PACING_FACTOR": "fast"},
			wantErr: true,
		},
		{
			name: "max concurrent override",
			env:  map[string]string{"PREANALYSIS_MAX_CONCURRENT": "3"},
			want: Preanalysis{PacingFactor: 1.0, MaxConcurrent: 3, RunTimeout: 4 * time.Hour},
		},
		{
			name:    "zero max concurrent rejected",
			env:     map[string]string{"PREANALYSIS_MAX_CONCURRENT": "0"},
			wantErr: true,
		},
		{
			name:    "negative max concurrent rejected",
			env:     map[string]string{"PREANALYSIS_MAX_CONCURRENT": "-2"},
			wantErr: true,
		},
		{
			name:    "non-numeric max concurrent rejected",
			env:     map[string]string{"PREANALYSIS_MAX_CONCURRENT": "many"},
			wantErr: true,
		},
		{
			name: "run timeout override",
			env:  map[string]string{"PREANALYSIS_RUN_TIMEOUT": "90m"},
			want: Preanalysis{PacingFactor: 1.0, MaxConcurrent: 1, RunTimeout: 90 * time.Minute},
		},
		{
			name:    "zero run timeout rejected",
			env:     map[string]string{"PREANALYSIS_RUN_TIMEOUT": "0s"},
			wantErr: true,
		},
		{
			name:    "negative run timeout rejected",
			env:     map[string]string{"PREANALYSIS_RUN_TIMEOUT": "-1h"},
			wantErr: true,
		},
		{
			name:    "malformed run timeout rejected",
			env:     map[string]string{"PREANALYSIS_RUN_TIMEOUT": "soon"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		// Subtests stay sequential: t.Setenv forbids t.Parallel.
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := LoadPreanalysis()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadPreanalysis: %v", err)
			}
			if got != tc.want {
				t.Errorf("LoadPreanalysis = %+v, want %+v", got, tc.want)
			}
		})
	}
}
