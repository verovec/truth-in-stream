package config

import "testing"

func TestLoadDocuments(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Documents
		wantErr bool
	}{
		{
			name: "defaults applied",
			want: Documents{MaxSizeBytes: 30 << 20, MaxSentences: 1500},
		},
		{
			name: "overrides applied",
			env: map[string]string{
				"DOCUMENT_MAX_SIZE_BYTES": "1048576",
				"DOCUMENT_MAX_SENTENCES":  "200",
			},
			want: Documents{MaxSizeBytes: 1 << 20, MaxSentences: 200},
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
			for k, v := range tc.env {
				t.Setenv(k, v)
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
