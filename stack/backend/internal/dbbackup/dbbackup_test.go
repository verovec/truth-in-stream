package dbbackup

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDumpArgs(t *testing.T) {
	got := DumpArgs("postgres://u:p@host:5432/db?sslmode=disable", "/tmp/db.dump")
	want := []string{
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		"--file=/tmp/db.dump",
		"--dbname=postgres://u:p@host:5432/db?sslmode=disable",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DumpArgs mismatch (-want +got):\n%s", diff)
	}
}

func TestRestoreArgs(t *testing.T) {
	got := RestoreArgs("postgres://u:p@host:5432/db?sslmode=disable", "/tmp/db.dump")
	want := []string{
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-privileges",
		"--dbname=postgres://u:p@host:5432/db?sslmode=disable",
		"/tmp/db.dump",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("RestoreArgs mismatch (-want +got):\n%s", diff)
	}
}

func TestWithDatabase(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		db      string
		want    string
		wantErr bool
	}{
		{
			name: "swaps the database, keeps credentials and query",
			dsn:  "postgres://postgres:postgres@localhost:5432/test?sslmode=disable",
			db:   "dbbackup_src",
			want: "postgres://postgres:postgres@localhost:5432/dbbackup_src?sslmode=disable",
		},
		{
			name: "no query parameters",
			dsn:  "postgres://postgres@localhost:5432/test",
			db:   "dbbackup_dst",
			want: "postgres://postgres@localhost:5432/dbbackup_dst",
		},
		{
			name:    "rejects a dsn without a host",
			dsn:     "not-a-dsn",
			db:      "whatever",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WithDatabase(tc.dsn, tc.db)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("WithDatabase(%q) = %q, want error", tc.dsn, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("WithDatabase(%q): unexpected error: %v", tc.dsn, err)
			}
			if got != tc.want {
				t.Errorf("WithDatabase(%q, %q) = %q, want %q", tc.dsn, tc.db, got, tc.want)
			}
		})
	}
}
