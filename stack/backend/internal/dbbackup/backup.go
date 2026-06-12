package dbbackup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultPrefix is the S3 key prefix scheduled and manual dumps share. It
// matches DB_BACKUP_PREFIX in scripts/db-common.sh so a dump written by the
// scheduled job and one written by `make backup` land under the same prefix and
// `make restore` consumes either unchanged.
const DefaultPrefix = "db-backups"

// DefaultDatabaseName mirrors DB_NAME in scripts/db-common.sh. It names the dump
// only when the DSN carries no database segment and no override is given; the
// restore path selects the newest object by timestamp, not by this name.
const DefaultDatabaseName = "truthinstream"

// dumpTimeLayout matches `date -u +%Y%m%d-%H%M%S` in scripts/db-backup.sh, so
// timestamped keys sort chronologically and the lexically greatest key is the
// newest dump.
const dumpTimeLayout = "20060102-150405"

// Uploader stores a dump at key. size is the exact byte length so the
// implementation can stream a single object without buffering it in memory.
type Uploader interface {
	Upload(ctx context.Context, key string, body io.Reader, size int64) error
}

// DumpFunc writes a custom-format archive of the database addressed by dsn to
// outPath. The production implementation is PgDump; tests substitute a fake
// that writes known bytes.
type DumpFunc func(ctx context.Context, dsn, outPath string) error

// PgDump is the production DumpFunc: it shells out to pg_dump with the canonical
// flags (DumpArgs), writing a custom-format archive to outPath. A non-zero exit
// returns the combined output so a failure is diagnosable from the task logs;
// the DSN is not echoed.
func PgDump(ctx context.Context, dsn, outPath string) error {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		return fmt.Errorf("locate pg_dump: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, DumpArgs(dsn, outPath)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump exited: %w\n%s", err, out)
	}
	return nil
}

// Options configures a single backup run.
type Options struct {
	// DSN addresses the database to dump. Required.
	DSN string
	// Prefix is the S3 key prefix; empty selects DefaultPrefix.
	Prefix string
	// DBName names the dump in its key; empty derives the name from the DSN and
	// falls back to DefaultDatabaseName when the DSN carries no database.
	DBName string
	// Now is the dump timestamp; the caller passes the current time so the key
	// is deterministic in tests.
	Now time.Time
}

// DumpKey returns the S3 object key for a dump of dbName taken at ts under
// prefix: "<prefix>/<dbName>-<YYYYMMDD-HHMMSS>.dump", with the timestamp in UTC.
func DumpKey(prefix, dbName string, ts time.Time) string {
	return fmt.Sprintf("%s/%s-%s.dump", prefix, dbName, ts.UTC().Format(dumpTimeLayout))
}

// DatabaseName extracts the database name from a Postgres DSN's path, so a dump
// is named after the database it actually captured. It errors on an
// unparseable DSN or one with no database segment.
func DatabaseName(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", fmt.Errorf("dsn has no database name: %q", dsn)
	}
	return name, nil
}

// Backup dumps the database at opt.DSN to a temporary file via dump, then
// uploads that file under the timestamped key via up, returning the key
// written. The temp file is always removed, and an empty dump is rejected
// before upload so a failed pg_dump can never shadow the last good snapshot
// with a zero-byte object.
func Backup(ctx context.Context, dump DumpFunc, up Uploader, opt Options) (string, error) {
	if opt.DSN == "" {
		return "", errors.New("dbbackup: dsn is required")
	}

	prefix := opt.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}
	dbName := opt.DBName
	if dbName == "" {
		if derived, err := DatabaseName(opt.DSN); err == nil {
			dbName = derived
		} else {
			dbName = DefaultDatabaseName
		}
	}
	key := DumpKey(prefix, dbName, opt.Now)

	tmp, err := os.CreateTemp("", "dbbackup-*.dump")
	if err != nil {
		return "", fmt.Errorf("dbbackup: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	// pg_dump writes the archive itself via --file, so close our handle first;
	// the bytes are read back through a fresh handle below.
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("dbbackup: close temp file: %w", err)
	}

	if err := dump(ctx, opt.DSN, tmpPath); err != nil {
		return "", fmt.Errorf("dbbackup: dump: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return "", fmt.Errorf("dbbackup: open dump: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("dbbackup: stat dump: %w", err)
	}
	if info.Size() == 0 {
		return "", errors.New("dbbackup: dump is empty; refusing to upload")
	}

	if err := up.Upload(ctx, key, f, info.Size()); err != nil {
		return "", fmt.Errorf("dbbackup: upload %q: %w", key, err)
	}
	return key, nil
}
