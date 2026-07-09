package config

import (
	"testing"
	"time"
)

func TestLoadDocuments(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Documents
		wantErr bool
	}{
		{
			name: "defaults applied",
			want: Documents{MaxSizeBytes: 30 << 20, MaxSentences: 1500, AnalysisTimeout: 30 * time.Minute},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"DOCUMENT_MAX_SIZE_BYTES":   "1048576",
				"DOCUMENT_MAX_SENTENCES":    "200",
				"DOCUMENT_ANALYSIS_TIMEOUT": "5m",
			},
			want: Documents{MaxSizeBytes: 1 << 20, MaxSentences: 200, AnalysisTimeout: 5 * time.Minute},
		},
		{
			name:    "non-positive analysis timeout fails",
			env:     map[string]string{"DOCUMENT_ANALYSIS_TIMEOUT": "0s"},
			wantErr: true,
		},
		{
			name:    "unparseable analysis timeout fails",
			env:     map[string]string{"DOCUMENT_ANALYSIS_TIMEOUT": "soon"},
			wantErr: true,
		},
		{
			name:    "non-numeric size fails",
			env:     map[string]string{"DOCUMENT_MAX_SIZE_BYTES": "large"},
			wantErr: true,
		},
		{
			name:    "non-positive size fails",
			env:     map[string]string{"DOCUMENT_MAX_SIZE_BYTES": "0"},
			wantErr: true,
		},
		{
			name:    "non-numeric sentence cap fails",
			env:     map[string]string{"DOCUMENT_MAX_SENTENCES": "many"},
			wantErr: true,
		},
		{
			name:    "non-positive sentence cap fails",
			env:     map[string]string{"DOCUMENT_MAX_SENTENCES": "-1"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		// Subtests stay sequential: t.Setenv forbids t.Parallel.
		t.Run(tc.name, func(t *testing.T) {
			// Pin both knobs every run (empty reads as unset), so an ambient
			// DOCUMENT_* variable on the host cannot leak into the defaults case.
			for _, k := range []string{"DOCUMENT_MAX_SIZE_BYTES", "DOCUMENT_MAX_SENTENCES", "DOCUMENT_ANALYSIS_TIMEOUT"} {
				t.Setenv(k, tc.env[k])
			}
			got, err := LoadDocuments()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadDocuments: %v", err)
			}
			if got != tc.want {
				t.Errorf("LoadDocuments = %+v, want %+v", got, tc.want)
			}
		})
	}
}
