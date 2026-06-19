#!/usr/bin/env bash
#
# Tests for scripts/worker-fleet.sh. Stubs the `aws` CLI so the scale-up,
# scale-to-zero, and status paths are exercised without an AWS account or a real
# ECS cluster. The cluster name is injected via CLUSTER so no terraform output is
# needed. Run: ./scripts/worker-fleet.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLEET="$SCRIPT_DIR/worker-fleet.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH. The stub serves the two calls the script
# makes (ecs update-service, ecs describe-services) and logs every invocation so
# the test can assert on the desired count the script set. Behaviour knobs:
#   SERVICE_MISSING   describe-services returns no service (None ...)
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "ecs update-service")
    echo '{"service":{"desiredCount":0}}' ;;
  "ecs describe-services")
    if [[ -n "${SERVICE_MISSING:-}" ]]; then
      echo "None    None"
    else
      echo "3	1"
    fi ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    SERVICE_MISSING="${SERVICE_MISSING:-}" \
    CLUSTER="${CLUSTER:-truth-in-stream-prod-cluster}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    ENVIRONMENT="${ENVIRONMENT:-prod}" \
    DRY_RUN="${DRY_RUN:-}"
}

echo "TEST: up scales the fleet to the requested count via update-service"
(
  make_sandbox
  out="$(bash "$FLEET" up embedworker 4 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on scale up" || fail "exit 0 on scale up (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "ecs update-service" "calls update-service"
  assert_contains "$log" "--cluster truth-in-stream-prod-cluster" "targets the resolved cluster"
  assert_contains "$log" "--service embedworker" "targets the bare ECS service name"
  assert_not_contains "$log" "--service truth-in-stream-prod-embedworker" "never prefixes the service name (only the family is prefixed)"
  assert_contains "$log" "--desired-count 4" "sets the requested desired count"
)

echo "TEST: up defaults the count to 2 when omitted"
(
  make_sandbox
  bash "$FLEET" up crawlworker >/dev/null 2>&1
  assert_contains "$(cat "$AWS_CALL_LOG")" "--desired-count 2" "defaults desired count to 2"
)

echo "TEST: down scales the fleet to zero"
(
  make_sandbox
  out="$(bash "$FLEET" down embedworker 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on scale down" || fail "exit 0 on scale down (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--service embedworker" "targets the bare ECS service name"
  assert_not_contains "$log" "--service truth-in-stream-prod-embedworker" "never prefixes the service name (only the family is prefixed)"
  assert_contains "$log" "--desired-count 0" "sets desired count to zero"
)

echo "TEST: status reports desired and running counts"
(
  make_sandbox
  out="$(bash "$FLEET" status embedworker 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on status" || fail "exit 0 on status (got $rc)"
  assert_contains "$out" "desired=3 running=1" "reports the parsed counts"
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "update-service" "status never mutates"
)

echo "TEST: up 0 is rejected (use down)"
(
  make_sandbox
  out="$(bash "$FLEET" up embedworker 0 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on up 0" || fail "non-zero exit on up 0 (got $rc)"
  assert_contains "$out" "use 'down'" "explains to use down for zero"
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "update-service" "up 0 never calls update-service"
)

echo "TEST: a non-numeric count is rejected before any aws call"
(
  make_sandbox
  out="$(bash "$FLEET" up embedworker two 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on bad count" || fail "non-zero exit on bad count (got $rc)"
  assert_contains "$out" "non-negative integer" "explains the count must be numeric"
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "update-service" "bad count never calls update-service"
)

echo "TEST: an unknown fleet is rejected before any aws call"
(
  make_sandbox
  out="$(bash "$FLEET" up bogusworker 2 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unknown fleet" || fail "non-zero exit on unknown fleet (got $rc)"
  assert_contains "$out" "unknown worker fleet" "names the bad fleet"
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "update-service" "unknown fleet never calls update-service"
)

echo "TEST: status on a missing service fails clearly"
(
  SERVICE_MISSING=1 make_sandbox
  out="$(bash "$FLEET" status embedworker 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the service is missing" || fail "non-zero exit when the service is missing (got $rc)"
  assert_contains "$out" "not found" "reports the service is not found"
)

echo "TEST: DRY_RUN prints the update-service call and never invokes aws"
(
  DRY_RUN=1 make_sandbox
  out="$(bash "$FLEET" up embedworker 4 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 under DRY_RUN" || fail "exit 0 under DRY_RUN (got $rc)"
  assert_contains "$out" "DRY-RUN aws ecs update-service" "prints the dry-run update-service"
  assert_contains "$out" "--desired-count 4" "dry-run shows the count"
  # update-service is the only mutating call; under DRY_RUN it must not reach aws.
  if [[ -s "$AWS_CALL_LOG" ]]; then
    grep -q "update-service" "$AWS_CALL_LOG" && fail "DRY_RUN must not call update-service" || ok "DRY_RUN made no mutating aws call"
  else
    ok "DRY_RUN made no aws call at all"
  fi
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "worker-fleet.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
