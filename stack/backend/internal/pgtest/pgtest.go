// Package pgtest holds shared integration-test database helpers. The backend has
// many integration packages (store, seed, wiki tooling, the live matcher) and CI
// runs them as parallel `go test` binaries against ONE shared TEST_DATABASE_URL.
// Each one resets the schema before its tests, so two resets can run at the same
// instant against the same database - and concurrent DDL on one database
// (CREATE EXTENSION, DROP/CREATE TABLE) races on the system catalogs, surfacing
// as a duplicate-key error on pg_extension_name_index. AcquireSchemaLock
// serializes those resets with a session-level Postgres advisory lock so they are
// mutually exclusive across every package, while the tests themselves still run
// in parallel.
//
// It is imported only from _test.go files and deliberately takes no dependency on
// the testing package, so callers wrap its error in their own t.Fatalf.
package pgtest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey is an arbitrary fixed key every integration package shares, so
// their schema resets contend on one lock and run one at a time. Its value is
// not meaningful beyond being constant and distinct from any application lock.
const advisoryLockKey int64 = 0x5452555448 // "TRUTH"

// AcquireSchemaLock takes the shared schema-reset advisory lock against dsn and
// returns a release function the caller must defer. Hold it for the duration of
// a schema reset (the DROP and migration re-apply) so parallel integration
// packages sharing one database never run those DDL statements concurrently. The
// lock is held on a dedicated connection; the reset's own statements may run on
// any connection, since the lock blocks every other reset regardless of which
// connection performs the work. Releasing also closes the connection, so a lock
// can never outlive its release call.
func AcquireSchemaLock(ctx context.Context, dsn string) (release func(), err error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgtest: connect: %w", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgtest: acquire connection: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		conn.Release()
		pool.Close()
		return nil, fmt.Errorf("pgtest: acquire advisory lock: %w", err)
	}
	return func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
		conn.Release()
		pool.Close()
	}, nil
}
