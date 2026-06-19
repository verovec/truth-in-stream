#!/usr/bin/env bash
#
# Tests for scripts/db-push.sh. Stubs the `aws` and `docker` CLIs so the dump,
# the RDS credential fetch + parse, and the pg_restore-over-tunnel invocation are
# exercised without an AWS account, a real database, or Docker. Asserts the load
# uses TEXT-format pg_dump/pg_restore (never binary CopyFrom), connects through
# the local tunnel, and never leaks the RDS password into an argv or a log.
# Run: ./scripts/db-push.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUSH="$SCRIPT_DIR/db-push.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# Sandbox: stubbed `aws` (serves the DSN secret) and `docker` (records every
# invocation; `compose exec ... pg_dump` writes a fake dump, `run ... pg_restore`
# just logs). The docker stub also dumps its environment so the test can prove
# the password is passed via the env (PGPASSWORD), never an argv, and is never
# printed to stdout/stderr.
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  DOCKER_CALL_LOG="$SANDBOX/docker.log"; : >"$DOCKER_CALL_LOG"
  DOCKER_ENV_LOG="$SANDBOX/docker-env.log"; : >"$DOCKER_ENV_LOG"

  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "secretsmanager get-secret-value")
    if [[ -n "${SECRET_MISSING:-}" ]]; then
      echo "An error occurred (ResourceNotFoundException) calling GetSecretValue" >&2
      exit 254
    fi
    printf '%s' "$RDS_DSN" ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"

  cat >"$BIN/docker" <<'DOCKER'
#!/usr/bin/env bash
echo "$*" >> "$DOCKER_CALL_LOG"
# Record PG* environment so the test can assert the password travels by env only.
env | grep -E '^PG' >> "$DOCKER_ENV_LOG" || true
case "$1 $2" in
  "compose ps")
    # require_postgres_running calls `docker compose ps -q postgres`; a non-empty
    # id means the service is up.
    printf 'postgres-container-id' ;;
  "compose exec")
    # Stand in for `pg_dump` inside the local container: emit a fake custom dump
    # on stdout so the caller's redirect produces a non-empty file.
    printf 'PGDMP-fake-custom-dump' ;;
  "run "*) : ;;   # pg_restore stand-in: success, no output
  "run")          : ;;
  *) echo "unexpected docker call: $*" >&2; exit 98 ;;
esac
DOCKER
  chmod +x "$BIN/docker"

  # Keep fresh dumps inside the sandbox so a test run never writes into the
  # repo's backups/ directory.
  export PATH="$BIN:$PATH" AWS_CALL_LOG DOCKER_CALL_LOG DOCKER_ENV_LOG \
    RDS_DSN="${RDS_DSN:-}" SECRET_MISSING="${SECRET_MISSING:-}" \
    AWS_PROFILE="${AWS_PROFILE:-}" BACKUP_DIR="$SANDBOX/backups"
}

DSN_OK="postgres://rdsuser:rdsSEKRET@db-1.abc.eu-west-3.rds.amazonaws.com:5432/truthinstream?sslmode=require"

echo "TEST: dumps the local DB, fetches RDS creds, and restores over the tunnel (prod default)"
(
  RDS_DSN="$DSN_OK" make_sandbox
  out="$(bash "$PUSH" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on the happy path" || fail "exit 0 on the happy path (got $rc)"
  dlog="$(cat "$DOCKER_CALL_LOG")"
  alog="$(cat "$AWS_CALL_LOG")"

  # The dump path uses pg_dump custom (text COPY internally), never a binary copy.
  assert_contains "$dlog" "compose exec" "dumps via the running local container"
  assert_contains "$dlog" "pg_dump --format=custom" "dumps in custom format (text COPY, halfvec-safe)"

  # The credential fetch targets the prod DSN secret.
  assert_contains "$alog" "truth-in-stream/prod/rds/dsn" "fetches the prod RDS DSN secret"
  assert_contains "$alog" "--profile verovec-prod" "defaults to the prod SSO profile"

  # The restore is pg_restore (NOT a binary CopyFrom), over the host tunnel.
  assert_contains "$dlog" "pg_restore --clean --if-exists" "restores with pg_restore (text COPY replay)"
  assert_contains "$dlog" "--network host" "runs the restore on the host network to reach the tunnel"
  assert_contains "$dlog" "--host localhost --port 5432" "connects through the local tunnel, not the RDS host"
  assert_contains "$dlog" "--username rdsuser --dbname truthinstream" "uses the parsed RDS user and dbname"

  # Binary-copy guard: the corrupting path must never appear.
  assert_not_contains "$dlog" "CopyFrom" "never uses binary CopyFrom"
  assert_not_contains "$dlog" "FORMAT binary" "never uses binary COPY format"
  assert_not_contains "$dlog" "--format=binary" "never dumps in a binary streaming format"
)

echo "TEST: the RDS password is passed by env name only (PGPASSWORD), never an argv or a log"
(
  RDS_DSN="$DSN_OK" make_sandbox
  out="$(bash "$PUSH" prod 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0" || fail "exit 0 (got $rc)"
  dlog="$(cat "$DOCKER_CALL_LOG")"
  envlog="$(cat "$DOCKER_ENV_LOG")"
  # `-e PGPASSWORD` (name only) means the value is never in any argv; it reaches
  # the container only as an inherited environment variable.
  assert_not_contains "$dlog" "rdsSEKRET" "password value never appears in a docker argv"
  assert_not_contains "$out" "rdsSEKRET" "password never printed to stdout/stderr"
  assert_contains "$dlog" "-e PGPASSWORD " "passes PGPASSWORD by name only (no value in argv)"
  assert_contains "$envlog" "PGPASSWORD=rdsSEKRET" "password reaches the container as an inherited env var"
  assert_contains "$dlog" "-e PGSSLMODE=require" "TLS is required for the RDS connection"
)

echo "TEST: --port routes the restore to the matching tunnel port"
(
  RDS_DSN="$DSN_OK" make_sandbox
  bash "$PUSH" prod --port 5440 >/dev/null 2>&1
  dlog="$(cat "$DOCKER_CALL_LOG")"
  assert_contains "$dlog" "--host localhost --port 5440" "restore connects to the overridden local port"
)

echo "TEST: --file reuses an existing dump and takes no fresh dump"
(
  RDS_DSN="$DSN_OK" make_sandbox
  existing="$SANDBOX/preexisting.dump"; printf 'PGDMP-existing' >"$existing"
  bash "$PUSH" prod --file "$existing" >/dev/null 2>&1
  dlog="$(cat "$DOCKER_CALL_LOG")"
  assert_not_contains "$dlog" "pg_dump" "skips the dump when a file is given"
  assert_contains "$dlog" "pg_restore" "still restores the supplied dump"
)

echo "TEST: env selects the RDS DSN secret and profile (dev)"
(
  RDS_DSN="postgres://u:p@dev-db:5432/truthinstream?sslmode=require" make_sandbox
  bash "$PUSH" dev >/dev/null 2>&1
  alog="$(cat "$AWS_CALL_LOG")"
  assert_contains "$alog" "truth-in-stream/dev/rds/dsn" "fetches the dev DSN secret"
  assert_contains "$alog" "--profile verovec-dev" "keys off the dev SSO profile"
)

echo "TEST: fails clearly when the RDS DSN secret is missing"
(
  SECRET_MISSING=1 make_sandbox
  existing="$SANDBOX/some.dump"; printf 'PGDMP-x' >"$existing"
  out="$(bash "$PUSH" prod --file "$existing" 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the secret is missing" || fail "non-zero exit when the secret is missing (got $rc)"
  assert_contains "$out" "truth-in-stream/prod/rds/dsn" "names the secret it expected"
  dlog="$(cat "$DOCKER_CALL_LOG")"
  assert_not_contains "$dlog" "pg_restore" "does not restore without RDS credentials"
)

echo "TEST: a password containing '@' still parses host/user/dbname correctly"
(
  RDS_DSN="postgres://rdsuser:p@ss@db-1.abc.eu-west-3.rds.amazonaws.com:5432/truthinstream?sslmode=require" make_sandbox
  out="$(bash "$PUSH" prod 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 with an '@' in the password" || fail "exit 0 with an '@' in the password (got $rc)"
  dlog="$(cat "$DOCKER_CALL_LOG")"
  envlog="$(cat "$DOCKER_ENV_LOG")"
  assert_contains "$dlog" "--username rdsuser --dbname truthinstream" "splits on the LAST @, so user/dbname are correct"
  assert_contains "$envlog" "PGPASSWORD=p@ss" "keeps the full password including the '@'"
  assert_not_contains "$dlog" "p@ss" "password (with @) never reaches an argv"
)

echo "TEST: rejects a DSN with no password in its userinfo (would otherwise send db_user as the password)"
(
  RDS_DSN="postgres://rdsuser@db-1.abc.eu-west-3.rds.amazonaws.com:5432/truthinstream?sslmode=require" make_sandbox
  out="$(bash "$PUSH" prod 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a password-less DSN" || fail "non-zero exit on a password-less DSN (got $rc)"
  assert_contains "$out" "no password" "explains the DSN has no password"
  dlog="$(cat "$DOCKER_CALL_LOG")"
  assert_not_contains "$dlog" "pg_restore" "does not restore with a mis-parsed password"
)

echo "TEST: leaves the dump and points at --file when pg_restore fails"
(
  RDS_DSN="$DSN_OK" make_sandbox
  # Make the restore (docker run) fail while the dump (docker compose exec) still
  # succeeds, by failing any `docker run` invocation.
  cat >"$BIN/docker" <<'DOCKER'
#!/usr/bin/env bash
echo "$*" >> "$DOCKER_CALL_LOG"
case "$1 $2" in
  "compose ps") printf 'postgres-container-id' ;;
  "compose exec") printf 'PGDMP-fake-custom-dump' ;;
  "run "*|"run") echo "pg_restore: connection refused" >&2; exit 1 ;;
  *) echo "unexpected docker call: $*" >&2; exit 98 ;;
esac
DOCKER
  chmod +x "$BIN/docker"
  out="$(bash "$PUSH" prod 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the restore fails" || fail "non-zero exit when the restore fails (got $rc)"
  assert_contains "$out" "The dump is kept at" "tells the operator the dump was retained"
  assert_contains "$out" "--file" "points at the --file retry path"
  # The retained dump file named in the message must actually still exist.
  kept="$(printf '%s\n' "$out" | sed -n 's/.*kept at: //p' | head -1)"
  [[ -n "$kept" && -f "$kept" ]] && ok "the named dump file exists on disk" || fail "the named dump file exists on disk (path: ${kept:-<none>})"
)

echo "TEST: rejects a non-numeric --port before any work"
(
  RDS_DSN="$DSN_OK" make_sandbox
  out="$(bash "$PUSH" prod -p abc 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a bad port" || fail "non-zero exit on a bad port (got $rc)"
  assert_contains "$out" "Invalid --port" "explains the port is invalid"
  alog="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$alog" "get-secret-value" "fails before fetching the secret"
)

echo "TEST: an unknown environment is rejected"
(
  RDS_DSN="$DSN_OK" make_sandbox
  out="$(bash "$PUSH" staging 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on an unknown env" || fail "non-zero exit on an unknown env (got $rc)"
  assert_contains "$out" "staging" "names the offending environment"
)

echo "TEST: -h prints usage and exits 0"
(
  make_sandbox
  out="$(bash "$PUSH" -h 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on --help" || fail "exit 0 on --help (got $rc)"
  assert_contains "$out" "Usage" "prints usage"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
