// Package dbbackup defines the canonical pg_dump / pg_restore invocation used
// to snapshot and restore the Postgres + pgvector database. The operator-facing
// entrypoints are the repository's `scripts/db-backup.sh` and
// `scripts/db-restore.sh`, which run the same flags inside the Postgres
// container; the round-trip test in this package is the guarantee that a
// custom-format dump preserves halfvec embeddings exactly, so a restore never
// silently corrupts the vectors the corpus paid to compute.
package dbbackup

import (
	"fmt"
	"net/url"
)

// DumpArgs returns the pg_dump arguments that write a compact, restorable
// custom-format archive at outPath for the database addressed by dsn. Custom
// format keeps the dump small and self-contained (it carries CREATE EXTENSION
// vector), and serializes halfvec through the type's text I/O, which round-trips
// exactly; no binary COPY path is involved.
func DumpArgs(dsn, outPath string) []string {
	return []string{
		"--format=custom",
		"--no-owner",
		"--no-privileges",
		"--file=" + outPath,
		"--dbname=" + dsn,
	}
}

// RestoreArgs returns the pg_restore arguments that restore archivePath into the
// database addressed by dsn, dropping any pre-existing objects first so the
// result is independent of whether migrations or seed ran beforehand.
func RestoreArgs(dsn, archivePath string) []string {
	return []string{
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-privileges",
		"--dbname=" + dsn,
		archivePath,
	}
}

// WithDatabase returns dsn rewritten to address the database name, preserving
// host, credentials, and query parameters. It is used to derive throwaway
// databases on the same server for the round-trip test.
func WithDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("dsn has no host: %q", dsn)
	}
	u.Path = "/" + name
	return u.String(), nil
}
