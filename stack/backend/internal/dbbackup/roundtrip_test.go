package dbbackup_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"

	"github.com/verovec/truth-in-stream/backend/internal/dbbackup"
)

type embeddingRow struct {
	ID        int
	Embedding string
	Label     string
}

// TestRoundTripPreservesHalfvec is the guarantee behind `make backup` /
// `make restore`: a custom-format pg_dump of a halfvec table, restored with
// pg_restore, reproduces the vectors byte-for-byte. This is the whole reason the
// card exists - the corpus embeddings must survive a reset without re-embedding,
// and a silent corruption here would waste the Voyage tokens the backup is meant
// to save. It skips when TEST_DATABASE_URL is unset or the client tools are
// absent, mirroring the store integration tests; CI provides a pgvector pg16.
func TestRoundTripPreservesHalfvec(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dump/restore round-trip test")
	}
	pgDump := lookTool(t, "pg_dump")
	pgRestore := lookTool(t, "pg_restore")

	ctx := t.Context()
	suffix := time.Now().UnixNano()
	srcDB := fmt.Sprintf("dbbackup_src_%d", suffix)
	dstDB := fmt.Sprintf("dbbackup_dst_%d", suffix)

	admin := connect(ctx, t, dsn)
	createDatabase(ctx, t, admin, srcDB)
	t.Cleanup(func() { dropDatabase(t, admin, srcDB) })
	createDatabase(ctx, t, admin, dstDB)
	t.Cleanup(func() { dropDatabase(t, admin, dstDB) })

	srcDSN := withDB(t, dsn, srcDB)
	dstDSN := withDB(t, dsn, dstDB)

	// Seed the source with values that are exactly representable in half
	// precision, so any difference after the round-trip is real corruption, not
	// float16 rounding. The negative and fractional components exercise the sign
	// and mantissa paths of halfvec's text I/O.
	src := connect(ctx, t, srcDSN)
	mustExec(ctx, t, src, "CREATE EXTENSION IF NOT EXISTS vector")
	mustExec(ctx, t, src, "CREATE TABLE embeddings (id int PRIMARY KEY, embedding halfvec(3), label text)")
	mustExec(ctx, t, src, "INSERT INTO embeddings (id, embedding, label) VALUES (1, '[1,2.5,-0.5]', 'alpha'), (2, '[0.25,16,1024]', 'beta'), (3, '[-2,0,0.125]', 'gamma')")
	want := readEmbeddings(ctx, t, src)
	if len(want) != 3 {
		t.Fatalf("seed: expected 3 rows, got %d", len(want))
	}

	archive := filepath.Join(t.TempDir(), "src.dump")
	mustRun(ctx, t, pgDump, dbbackup.DumpArgs(srcDSN, archive))
	// pg_restore can exit non-zero for benign reasons (e.g. a newer client
	// emitting a session GUC an older server ignores). The real guarantee is the
	// strict row diff below: if the restore actually lost or corrupted data, the
	// embeddings will not match. So log the restore output but let the data be
	// the gate; a missing table surfaces as a query failure in readEmbeddings.
	logRun(ctx, t, pgRestore, dbbackup.RestoreArgs(dstDSN, archive))

	dst := connect(ctx, t, dstDSN)
	got := readEmbeddings(ctx, t, dst)

	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("embeddings differ after dump/restore (-source +restored):\n%s", diff)
	}
}

func lookTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not on PATH; skipping round-trip test", name)
	}
	return path
}

func connect(ctx context.Context, t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func mustExec(ctx context.Context, t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// createDatabase makes a throwaway database. CREATE DATABASE cannot run in a
// transaction, which pgx's auto-commit Exec satisfies.
func createDatabase(ctx context.Context, t *testing.T, admin *pgx.Conn, name string) {
	t.Helper()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
}

func dropDatabase(t *testing.T, admin *pgx.Conn, name string) {
	t.Helper()
	// WITH (FORCE) terminates leftover connections so cleanup never hangs.
	if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		t.Errorf("drop database %s: %v", name, err)
	}
}

func withDB(t *testing.T, dsn, name string) string {
	t.Helper()
	out, err := dbbackup.WithDatabase(dsn, name)
	if err != nil {
		t.Fatalf("derive dsn for %s: %v", name, err)
	}
	return out
}

func readEmbeddings(ctx context.Context, t *testing.T, conn *pgx.Conn) []embeddingRow {
	t.Helper()
	rows, err := conn.Query(ctx, "SELECT id, embedding::text, label FROM embeddings ORDER BY id")
	if err != nil {
		t.Fatalf("query embeddings: %v", err)
	}
	defer rows.Close()

	var out []embeddingRow
	for rows.Next() {
		var r embeddingRow
		if err := rows.Scan(&r.ID, &r.Embedding, &r.Label); err != nil {
			t.Fatalf("scan embedding row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate embeddings: %v", err)
	}
	return out
}

// mustRun fails the test if the tool exits non-zero; used for pg_dump, where a
// failure means there is no archive to restore.
func mustRun(ctx context.Context, t *testing.T, tool string, args []string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, tool, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(tool), args, err, out)
	}
}

// logRun runs the tool and records its output without failing on a non-zero
// exit; the caller asserts success by inspecting the resulting data instead.
func logRun(ctx context.Context, t *testing.T, tool string, args []string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, tool, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("%s %v: %v\n%s", filepath.Base(tool), args, err, out)
	}
}
