#!/usr/bin/env bash
#
# Tests for scripts/ssm-port-forward.sh. Stubs the `aws` CLI so the bastion
# lookup, broker-secret fetch, AMQP URL parse, and the port-forward invocation
# are exercised without an AWS account or a real SSM session.
# Run: ./scripts/ssm-port-forward.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PF="$SCRIPT_DIR/ssm-port-forward.sh"

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
# exported env, logging every invocation so the test can assert on the
# arguments the script built.
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
    printf '%s' "$BROKER_URL" ;;
  "ssm start-session")
    # Stand in for the long-lived tunnel; exec hands control here, so a clean
    # exit means the script reached the forwarding call with its built args.
    exit 0 ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    INSTANCE_ID="${INSTANCE_ID:-}" \
    BROKER_URL="${BROKER_URL:-}" \
    SECRET_MISSING="${SECRET_MISSING:-}" \
    DESCRIBE_FAIL="${DESCRIBE_FAIL:-}" \
    AWS_PROFILE="${AWS_PROFILE:-}"
}

echo "TEST: opens the tunnel with the parsed broker host/port and default local port"
(
  BROKER_URL="amqps://app:s3cret@b-1234.mq.eu-west-3.amazonaws.com:5671/" make_sandbox
  out="$(bash "$PF" dev 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on the happy path" || fail "exit 0 on the happy path (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "tag:Name,Values=truth-in-stream-dev-bastion" "resolves the bastion by Name tag"
  assert_contains "$log" "instance-state-name,Values=running" "filters to running instances"
  assert_contains "$log" "truth-in-stream/dev/rabbitmq/url" "fetches the broker URL secret"
  assert_contains "$log" "start-session" "starts an SSM session"
  assert_contains "$log" "AWS-StartPortForwardingSessionToRemoteHost" "uses the remote-host port-forward document"
  assert_contains "$log" "i-0abc123def456" "targets the resolved bastion instance"
  assert_contains "$log" "host=b-1234.mq.eu-west-3.amazonaws.com,portNumber=5671,localPortNumber=5671" "forwards the broker host:port to the default local port"
  assert_contains "$out" "localhost:5671" "prints the local endpoint"
)

echo "TEST: --port overrides the local listen port only"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" make_sandbox
  out="$(bash "$PF" dev --port 5673 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 with -p" || fail "exit 0 with -p (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "host=broker.internal,portNumber=5671,localPortNumber=5673" "remote port stays 5671, local port is the override"
)

echo "TEST: a non-standard broker port in the secret is honoured (parse, not hardcode)"
(
  BROKER_URL="amqps://app:pw@broker.internal:5999/" make_sandbox
  bash "$PF" dev >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "portNumber=5999" "uses the port from the secret URL"
)

echo "TEST: a URL with no explicit port defaults the remote port to 5671"
(
  BROKER_URL="amqps://app:pw@broker.internal/" make_sandbox
  bash "$PF" dev >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "host=broker.internal,portNumber=5671,localPortNumber=5671" "defaults the remote port when the URL omits it"
)

echo "TEST: env selects the SSO profile, bastion name, and secret"
(
  BROKER_URL="amqps://app:pw@prod-broker:5671/" make_sandbox
  bash "$PF" prod >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--profile verovec-prod" "keys off the prod SSO profile"
  assert_contains "$log" "tag:Name,Values=truth-in-stream-prod-bastion" "resolves the prod bastion"
  assert_contains "$log" "truth-in-stream/prod/rabbitmq/url" "fetches the prod broker secret"
)

echo "TEST: dev is the default environment"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" make_sandbox
  bash "$PF" >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--profile verovec-dev" "defaults to the dev SSO profile"
  assert_contains "$log" "truth-in-stream-dev-bastion" "defaults to the dev bastion"
)

echo "TEST: AWS_PROFILE overrides the env-derived profile"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" AWS_PROFILE=custom-sso make_sandbox
  bash "$PF" dev >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--profile custom-sso" "honours an explicit AWS_PROFILE"
)

echo "TEST: fails clearly when no running bastion is found"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" INSTANCE_ID="None" make_sandbox
  out="$(bash "$PF" dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when no bastion" || fail "non-zero exit when no bastion (got $rc)"
  assert_contains "$out" "truth-in-stream-dev-bastion" "names the bastion it looked for"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-session" "does not start a session without a target"
)

echo "TEST: fails clearly when EC2 cannot be queried (e.g. expired SSO)"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" DESCRIBE_FAIL=1 make_sandbox
  out="$(bash "$PF" dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the EC2 query fails" || fail "non-zero exit when the EC2 query fails (got $rc)"
  assert_contains "$out" "aws sso login --profile verovec-dev" "points at the SSO login to fix it"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-session" "does not start a session without a target"
)

echo "TEST: rejects a non-numeric --port before touching AWS"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" make_sandbox
  out="$(bash "$PF" dev -p abc 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a bad port" || fail "non-zero exit on a bad port (got $rc)"
  assert_contains "$out" "Invalid --port" "explains the port is invalid"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "describe-instances" "fails before any AWS call"
)

echo "TEST: fails clearly when the broker secret is missing"
(
  INSTANCE_ID="i-0abc123def456" SECRET_MISSING=1 make_sandbox
  out="$(bash "$PF" dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the secret is missing" || fail "non-zero exit when the secret is missing (got $rc)"
  assert_contains "$out" "truth-in-stream/dev/rabbitmq/url" "names the secret it expected"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-session" "does not start a session without a broker host"
)

echo "TEST: an unknown environment is rejected"
(
  BROKER_URL="amqps://app:pw@broker.internal:5671/" make_sandbox
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
