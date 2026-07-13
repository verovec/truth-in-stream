#!/usr/bin/env bash
#
# Tests for scripts/test-db.sh. Stubs `docker` on a sandbox PATH so the
# create-if-missing behaviour is exercised without Docker or Postgres. The stub
# is driven by env (PG_RUNNING, DB_EXISTS) and records createdb/up calls to
# CALLLOG so the test can assert what the script did.
# Run: ./scripts/test-db.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTDB="$SCRIPT_DIR/test-db.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }
assert_eq()           { [ "$1" = "$2" ] && ok "$3" || fail "$3 (want $2, got $1)"; }

# A stub `docker` emulating just the compose calls test-db.sh makes. `ps -q`
# reports a container when PG_RUNNING; the `psql ... pg_database` probe reports a
# row when DB_EXISTS; `up` and `createdb` are appended to CALLLOG.
BIN="$TMPROOT/bin"; mkdir -p "$BIN"
cat >"$BIN/docker" <<'STUB'
#!/usr/bin/env bash
[ "$1" = compose ] || exit 0
sub="$2"; shift 2
case "$sub" in
  ps)
    [ -n "${PG_RUNNING:-}" ] && echo "stub-postgres-container-id"
    exit 0 ;;
  up)
    echo "up $*" >>"$CALLLOG"; exit 0 ;;
  exec)
    while [ $# -gt 0 ] && [ "$1" != postgres ]; do shift; done
    shift || true                      # drop the `postgres` service token
    case "${1:-}" in
      pg_isready) exit 0 ;;
      psql)       [ -n "${DB_EXISTS:-}" ] && echo "1"; exit 0 ;;
      createdb)   echo "createdb $*" >>"$CALLLOG"; exit 0 ;;
      *)          exit 0 ;;
    esac ;;
  *) exit 0 ;;
esac
STUB
chmod +x "$BIN/docker"

run() { CALLLOG="$TMPROOT/calls.log"; : >"$CALLLOG"; PATH="$BIN:$PATH" bash "$TESTDB" 2>&1; }

echo "TEST: database absent -> created"
CALLLOG="$TMPROOT/calls.log"; : >"$CALLLOG"
out="$(PG_RUNNING=1 CALLLOG="$CALLLOG" PATH="$BIN:$PATH" bash "$TESTDB" 2>&1)"; rc=$?
calls="$(cat "$CALLLOG")"
assert_eq "$rc" 0 "exit 0 when postgres is up and the database is absent"
assert_contains "$out" "created throwaway database 'truthinstream_test'" "reports the create"
assert_contains "$calls" "createdb -U postgres truthinstream_test" "calls createdb with the default name"

echo "TEST: database present -> not created (idempotent)"
CALLLOG="$TMPROOT/calls.log"; : >"$CALLLOG"
out="$(PG_RUNNING=1 DB_EXISTS=1 CALLLOG="$CALLLOG" PATH="$BIN:$PATH" bash "$TESTDB" 2>&1)"; rc=$?
calls="$(cat "$CALLLOG")"
assert_eq "$rc" 0 "exit 0 when the database already exists"
assert_contains "$out" "already present" "reports the database is present"
assert_not_contains "$calls" "createdb" "does not create when the database exists"

echo "TEST: postgres not running -> brought up"
CALLLOG="$TMPROOT/calls.log"; : >"$CALLLOG"
out="$(DB_EXISTS=1 CALLLOG="$CALLLOG" PATH="$BIN:$PATH" bash "$TESTDB" 2>&1)"; rc=$?
calls="$(cat "$CALLLOG")"
assert_eq "$rc" 0 "exit 0 when it has to start postgres first"
assert_contains "$calls" "up -d postgres" "starts postgres when it is not running"

echo "TEST: TEST_DB_NAME override"
CALLLOG="$TMPROOT/calls.log"; : >"$CALLLOG"
out="$(PG_RUNNING=1 TEST_DB_NAME=custom_itest CALLLOG="$CALLLOG" PATH="$BIN:$PATH" bash "$TESTDB" 2>&1)"; rc=$?
calls="$(cat "$CALLLOG")"
assert_eq "$rc" 0 "exit 0 with a custom database name"
assert_contains "$calls" "createdb -U postgres custom_itest" "honours TEST_DB_NAME"

echo "TEST: invalid TEST_DB_NAME rejected"
CALLLOG="$TMPROOT/calls.log"; : >"$CALLLOG"
out="$(PG_RUNNING=1 TEST_DB_NAME='bad; DROP DATABASE truthinstream' CALLLOG="$CALLLOG" PATH="$BIN:$PATH" bash "$TESTDB" 2>&1)"; rc=$?
calls="$(cat "$CALLLOG")"
assert_eq "$rc" 1 "exit 1 on an invalid database name"
assert_contains "$out" "invalid TEST_DB_NAME" "reports the invalid name"
assert_not_contains "$calls" "createdb" "does not create with an invalid name"

passed="$(grep -c PASS "$TALLY" || true)"
failed="$(grep -c FAIL "$TALLY" || true)"
echo
echo "test-db.test.sh: $passed passed, $failed failed"
[ "$failed" -eq 0 ]
