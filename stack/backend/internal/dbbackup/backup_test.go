package dbbackup

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestDumpKey(t *testing.T) {
	cet := time.FixedZone("CET", 2*60*60)
	tests := []struct {
		name   string
		prefix string
		dbName string
		ts     time.Time
		want   string
	}{
		{
			name:   "utc timestamp",
			prefix: "db-backups",
			dbName: "truthinstream",
			ts:     time.Date(2026, 6, 12, 4, 5, 6, 0, time.UTC),
			want:   "db-backups/truthinstream-20260612-040506.dump",
		},
		{
			name:   "converts to utc before formatting",
			prefix: "db-backups",
			dbName: "truthinstream",
			// 01:30 CET is 23:30 UTC the previous day; the key must use UTC so
			// keys from different runners still sort chronologically.
			ts:   time.Date(2026, 6, 12, 1, 30, 0, 0, cet),
			want: "db-backups/truthinstream-20260611-233000.dump",
		},
		{
			name:   "custom prefix and name",
			prefix: "nightly",
			dbName: "appdb",
			ts:     time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
			want:   "nightly/appdb-20260102-150405.dump",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DumpKey(tc.prefix, tc.dbName, tc.ts); got != tc.want {
				t.Errorf("DumpKey(%q, %q, %v) = %q, want %q", tc.prefix, tc.dbName, tc.ts, got, tc.want)
			}
		})
	}
}

func TestDatabaseName(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		want    string
		wantErr bool
	}{
		{
			name: "db with query parameters",
			dsn:  "postgres://u:p@host:5432/truthinstream?sslmode=require",
			want: "truthinstream",
		},
		{
			name: "db without query",
			dsn:  "postgres://u@host:5432/appdb",
			want: "appdb",
		},
		{
			name:    "no database segment",
			dsn:     "postgres://u:p@host:5432/",
			wantErr: true,
		},
		{
			name:    "unparseable dsn",
			dsn:     "://nonsense",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DatabaseName(tc.dsn)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DatabaseName(%q) = %q, want error", tc.dsn, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DatabaseName(%q): unexpected error: %v", tc.dsn, err)
			}
			if got != tc.want {
				t.Errorf("DatabaseName(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// fakeUploader records the one upload it is asked to perform, draining the body
// so the test can assert the exact bytes and size handed to S3.
type fakeUploader struct {
	calls int
	key   string
	body  []byte
	size  int64
	err   error
}

func (f *fakeUploader) Upload(_ context.Context, key string, body io.Reader, size int64) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.key, f.body, f.size = key, b, size
	return nil
}

// writeDump returns a DumpFunc that writes content to outPath and records the
// path it was given, so the test can assert the temp file is cleaned up after.
func writeDump(content string, capturedPath *string) DumpFunc {
	return func(_ context.Context, _ string, outPath string) error {
		*capturedPath = outPath
		return os.WriteFile(outPath, []byte(content), 0o600)
	}
}

func TestBackupDumpsThenUploads(t *testing.T) {
	up := &fakeUploader{}
	var dumpPath string
	opt := Options{
		DSN:    "postgres://u:p@host:5432/truthinstream?sslmode=require",
		Prefix: "db-backups",
		Now:    time.Date(2026, 6, 12, 4, 0, 0, 0, time.UTC),
	}

	key, err := Backup(t.Context(), writeDump("PGDMP-bytes", &dumpPath), up, opt)
	if err != nil {
		t.Fatalf("Backup: unexpected error: %v", err)
	}

	wantKey := "db-backups/truthinstream-20260612-040000.dump"
	if key != wantKey {
		t.Errorf("key = %q, want %q", key, wantKey)
	}
	if up.calls != 1 {
		t.Fatalf("uploader called %d times, want 1", up.calls)
	}
	if up.key != wantKey {
		t.Errorf("uploaded key = %q, want %q", up.key, wantKey)
	}
	if string(up.body) != "PGDMP-bytes" {
		t.Errorf("uploaded body = %q, want %q", up.body, "PGDMP-bytes")
	}
	if up.size != int64(len("PGDMP-bytes")) {
		t.Errorf("uploaded size = %d, want %d", up.size, len("PGDMP-bytes"))
	}
	if _, err := os.Stat(dumpPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp dump %q not removed (stat err = %v)", dumpPath, err)
	}
}

func TestBackupDefaultsPrefixAndDerivesName(t *testing.T) {
	up := &fakeUploader{}
	var dumpPath string
	opt := Options{
		DSN: "postgres://u:p@host:5432/corpusdb?sslmode=require",
		Now: time.Date(2026, 6, 12, 4, 0, 0, 0, time.UTC),
	}

	key, err := Backup(t.Context(), writeDump("x", &dumpPath), up, opt)
	if err != nil {
		t.Fatalf("Backup: unexpected error: %v", err)
	}
	want := "db-backups/corpusdb-20260612-040000.dump"
	if key != want {
		t.Errorf("key = %q, want %q (default prefix + name derived from DSN)", key, want)
	}
}

func TestBackupFallsBackToDefaultName(t *testing.T) {
	up := &fakeUploader{}
	var dumpPath string
	opt := Options{
		// No database segment in the DSN and no DBName override: the name falls
		// back to the shared default rather than failing the run.
		DSN: "postgres://u:p@host:5432/",
		Now: time.Date(2026, 6, 12, 4, 0, 0, 0, time.UTC),
	}

	key, err := Backup(t.Context(), writeDump("x", &dumpPath), up, opt)
	if err != nil {
		t.Fatalf("Backup: unexpected error: %v", err)
	}
	want := "db-backups/truthinstream-20260612-040000.dump"
	if key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

func TestBackupOverridesName(t *testing.T) {
	up := &fakeUploader{}
	var dumpPath string
	opt := Options{
		DSN:    "postgres://u:p@host:5432/truthinstream",
		DBName: "manual",
		Now:    time.Date(2026, 6, 12, 4, 0, 0, 0, time.UTC),
	}

	key, err := Backup(t.Context(), writeDump("x", &dumpPath), up, opt)
	if err != nil {
		t.Fatalf("Backup: unexpected error: %v", err)
	}
	want := "db-backups/manual-20260612-040000.dump"
	if key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

func TestBackupRequiresDSN(t *testing.T) {
	up := &fakeUploader{}
	_, err := Backup(t.Context(), writeDump("x", new(string)), up, Options{Now: time.Now()})
	if err == nil {
		t.Fatal("Backup with empty DSN: want error, got nil")
	}
	if up.calls != 0 {
		t.Errorf("uploader called %d times on missing DSN, want 0", up.calls)
	}
}

func TestBackupDumpErrorSkipsUpload(t *testing.T) {
	up := &fakeUploader{}
	var dumpPath string
	failingDump := func(_ context.Context, _, outPath string) error {
		dumpPath = outPath
		return errors.New("pg_dump exploded")
	}
	_, err := Backup(t.Context(), failingDump, up, Options{
		DSN: "postgres://u@host:5432/db",
		Now: time.Now(),
	})
	if err == nil {
		t.Fatal("Backup with failing dump: want error, got nil")
	}
	if up.calls != 0 {
		t.Errorf("uploader called %d times after dump failure, want 0", up.calls)
	}
	if dumpPath != "" {
		if _, statErr := os.Stat(dumpPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("temp dump %q not removed after dump failure", dumpPath)
		}
	}
}

func TestBackupRejectsEmptyDump(t *testing.T) {
	up := &fakeUploader{}
	var dumpPath string
	_, err := Backup(t.Context(), writeDump("", &dumpPath), up, Options{
		DSN: "postgres://u@host:5432/db",
		Now: time.Now(),
	})
	if err == nil {
		t.Fatal("Backup with empty dump: want error, got nil")
	}
	if up.calls != 0 {
		t.Errorf("uploader called %d times for an empty dump, want 0", up.calls)
	}
}

func TestBackupUploadErrorIsWrapped(t *testing.T) {
	up := &fakeUploader{err: errors.New("s3 down")}
	var dumpPath string
	_, err := Backup(t.Context(), writeDump("bytes", &dumpPath), up, Options{
		DSN: "postgres://u@host:5432/db",
		Now: time.Now(),
	})
	if err == nil {
		t.Fatal("Backup with failing upload: want error, got nil")
	}
	if _, statErr := os.Stat(dumpPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("temp dump %q not removed after upload failure", dumpPath)
	}
}
