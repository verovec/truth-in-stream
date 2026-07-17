package config

import "testing"

func TestLoadPreanalysis(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Preanalysis
		wantErr bool
	}{
		{
			name: "default is realtime",
			want: Preanalysis{PacingFactor: 1.0},
		},
		{
			name: "slower factor accepted",
			env:  map[string]string{"PREANALYSIS_PACING_FACTOR": "0.5"},
			want: Preanalysis{PacingFactor: 0.5},
		},
		{
			name: "explicit realtime accepted",
			env:  map[string]string{"PREANALYSIS_PACING_FACTOR": "1"},
			want: Preanalysis{PacingFactor: 1.0},
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
