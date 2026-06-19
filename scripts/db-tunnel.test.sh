#!/usr/bin/env bash
#
# Tests for scripts/db-tunnel.sh. Stubs the `aws` CLI so the bastion lookup, the
# RDS DSN fetch, the host/port parse, and the port-forward invocation are
# exercised without an AWS account or a real SSM session.
# Run: ./scripts/db-tunnel.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PF="$SCRIPT_DIR/db-tunnel.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH. The stub serves the three calls the
# script makes (describe-instances, get-secret-value, ssm start-session) from
# exported env, logging every invocation so the test can assert on the args.
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "ec2 describe-instances")
    if [[ -n "${DESCRIBE_FAIL:-}" ]]; then
      echo "An error occurred (ExpiredToken) calling DescribeInstances" >&2
      exit 254
    fi
    printf '%s' "${INSTANCE_ID:-i-0abc123def456}" ;;
  "secretsmanager get-secret-value")
    if [[ -n "${SECRET_MISSING:-}" ]]; then
      echo "An error occurred (ResourceNotFoundException) calling GetSecretValue" >&2
      exit 254
    fi
    printf '%s' "$RDS_DSN" ;;
  "ssm start-session")
    exit 0 ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    INSTANCE_ID="${INSTANCE_ID:-}" \
    RDS_DSN="${RDS_DSN:-}" \
    SECRET_MISSING="${SECRET_MISSING:-}" \
    DESCRIBE_FAIL="${DESCRIBE_FAIL:-}" \
    AWS_PROFILE="${AWS_PROFILE:-}"
}

echo "TEST: opens the tunnel with the parsed RDS host/port and default local port (prod default)"
(
  RDS_DSN="postgres://app:s3cret@db-1.abc.eu-west-3.rds.amazonaws.com:5432/truthinstream?sslmode=require" make_sandbox
  out="$(bash "$PF" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on the happy path" || fail "exit 0 on the happy path (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "tag:Name,Values=truth-in-stream-prod-bastion" "resolves the prod bastion by Name tag (prod is the default env)"
  assert_contains "$log" "instance-state-name,Values=running" "filters to running instances"
  assert_contains "$log" "truth-in-stream/prod/rds/dsn" "fetches the RDS DSN secret"
  assert_contains "$log" "--profile verovec-prod" "defaults to the prod SSO profile"
  assert_contains "$log" "start-session" "starts an SSM session"
  assert_contains "$log" "AWS-StartPortForwardingSessionToRemoteHost" "uses the remote-host port-forward document"
  assert_contains "$log" "i-0abc123def456" "targets the resolved bastion instance"
  assert_contains "$log" "host=db-1.abc.eu-west-3.rds.amazonaws.com,portNumber=5432,localPortNumber=5432" "forwards the RDS host:port to the default local port"
  assert_contains "$out" "localhost:5432" "prints the local endpoint"
)

echo "TEST: --port overrides the local listen port only"
(
  RDS_DSN="postgres://app:pw@db.internal:5432/truthinstream?sslmode=require" make_sandbox
  out="$(bash "$PF" prod --port 5440 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 with -p" || fail "exit 0 with -p (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "host=db.internal,portNumber=5432,localPortNumber=5440" "remote port stays 5432, local port is the override"
)

echo "TEST: a non-standard RDS port in the secret is honoured (parse, not hardcode)"
(
  RDS_DSN="postgres://app:pw@db.internal:5499/truthinstream" make_sandbox
  bash "$PF" prod >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "portNumber=5499" "uses the port from the secret DSN"
)

echo "TEST: a DSN with no explicit port defaults the remote port to 5432"
(
  RDS_DSN="postgres://app:pw@db.internal/truthinstream" make_sandbox
  bash "$PF" prod >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "host=db.internal,portNumber=5432,localPortNumber=5432" "defaults the remote port when the DSN omits it"
)

echo "TEST: env selects the SSO profile, bastion name, and secret (dev)"
(
  RDS_DSN="postgres://app:pw@dev-db:5432/truthinstream" make_sandbox
  bash "$PF" dev >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--profile verovec-dev" "keys off the dev SSO profile"
  assert_contains "$log" "tag:Name,Values=truth-in-stream-dev-bastion" "resolves the dev bastion"
  assert_contains "$log" "truth-in-stream/dev/rds/dsn" "fetches the dev RDS DSN secret"
)

echo "TEST: AWS_PROFILE overrides the env-derived profile"
(
  RDS_DSN="postgres://app:pw@db.internal:5432/truthinstream" AWS_PROFILE=custom-sso make_sandbox
  bash "$PF" prod >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--profile custom-sso" "honours an explicit AWS_PROFILE"
)

echo "TEST: fails clearly when no running bastion is found"
(
  RDS_DSN="postgres://app:pw@db.internal:5432/truthinstream" INSTANCE_ID="None" make_sandbox
  out="$(bash "$PF" prod 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when no bastion" || fail "non-zero exit when no bastion (got $rc)"
  assert_contains "$out" "truth-in-stream-prod-bastion" "names the bastion it looked for"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-session" "does not start a session without a target"
)

echo "TEST: fails clearly when EC2 cannot be queried (e.g. expired SSO)"
(
  RDS_DSN="postgres://app:pw@db.internal:5432/truthinstream" DESCRIBE_FAIL=1 make_sandbox
  out="$(bash "$PF" prod 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the EC2 query fails" || fail "non-zero exit when the EC2 query fails (got $rc)"
  assert_contains "$out" "aws sso login --profile verovec-prod" "points at the SSO login to fix it"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-session" "does not start a session without a target"
)

echo "TEST: rejects a non-numeric --port before touching AWS"
(
  RDS_DSN="postgres://app:pw@db.internal:5432/truthinstream" make_sandbox
  out="$(bash "$PF" prod -p abc 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a bad port" || fail "non-zero exit on a bad port (got $rc)"
  assert_contains "$out" "Invalid --port" "explains the port is invalid"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "describe-instances" "fails before any AWS call"
)

echo "TEST: fails clearly when the RDS DSN secret is missing"
(
  INSTANCE_ID="i-0abc123def456" SECRET_MISSING=1 make_sandbox
  out="$(bash "$PF" prod 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the secret is missing" || fail "non-zero exit when the secret is missing (got $rc)"
  assert_contains "$out" "truth-in-stream/prod/rds/dsn" "names the secret it expected"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-session" "does not start a session without an RDS host"
)

echo "TEST: an unknown environment is rejected"
(
  RDS_DSN="postgres://app:pw@db.internal:5432/truthinstream" make_sandbox
  out="$(bash "$PF" staging 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on an unknown env" || fail "non-zero exit on an unknown env (got $rc)"
  assert_contains "$out" "staging" "names the offending environment"
)

echo "TEST: -h prints usage and exits 0"
(
  make_sandbox
  out="$(bash "$PF" -h 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on --help" || fail "exit 0 on --help (got $rc)"
  assert_contains "$out" "Usage" "prints usage"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
