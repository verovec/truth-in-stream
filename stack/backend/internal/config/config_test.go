package config

import "testing"

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/db"},
			want: Config{Port: "8080", DatabaseURL: "postgres://localhost/db"},
		},
		{
			name:    "missing database url fails",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "port override",
			env:  map[string]string{"PORT": "9090", "DATABASE_URL": "postgres://localhost/db"},
			want: Config{Port: "9090", DatabaseURL: "postgres://localhost/db"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
