#!/usr/bin/env bash
#
# Tests for scripts/aws-target-guard.sh. Stubs the `aws` CLI so the live-identity
# read (sts get-caller-identity) is served without an AWS account, and points the
# guard at a throwaway deploy/targets.json so the expected-account compare is
# exercised entirely offline. `jq` is used for real. Run: ./scripts/aws-target-guard.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/aws-target-guard.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH and a throwaway targets.json. The aws stub
# serves sts get-caller-identity (account + arn) and logs every call so a test can
# assert the guard never mutates. Behaviour knobs:
#   LIVE_ACCOUNT   the account id sts reports (default 999999999999)
#   STS_FAIL       sts get-caller-identity exits non-zero (no/expired credentials)
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  TARGETS="$SANDBOX/targets.json"
  cat >"$TARGETS" <<'JSON'
{
  "dev":  { "account_id": "111111111111", "region": "eu-west-3" },
  "prod": { "account_id": "999999999999", "region": "eu-west-3" }
}
JSON
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "sts get-caller-identity")
    if [[ -n "${STS_FAIL:-}" ]]; then
      echo "Unable to locate credentials." >&2; exit 255; fi
    args="$*"
    case "$args" in
      *Account*Arn*|*"[Account,Arn]"*)
        printf '%s\tarn:aws:iam::%s:user/operator\n' "${LIVE_ACCOUNT:-999999999999}" "${LIVE_ACCOUNT:-999999999999}" ;;
      *Account*) echo "${LIVE_ACCOUNT:-999999999999}" ;;
      *Arn*)     echo "arn:aws:iam::${LIVE_ACCOUNT:-999999999999}:user/operator" ;;
      *)         printf '%s\tarn:aws:iam::%s:user/operator\n' "${LIVE_ACCOUNT:-999999999999}" "${LIVE_ACCOUNT:-999999999999}" ;;
    esac ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    LIVE_ACCOUNT="${LIVE_ACCOUNT:-999999999999}" \
    STS_FAIL="${STS_FAIL:-}" \
    TARGETS_FILE="$TARGETS" \
    CLUSTER="${CLUSTER:-truth-in-stream-prod-cluster}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    ENVIRONMENT="${ENVIRONMENT:-prod}" \
    DRY_RUN="${DRY_RUN:-}"
}

echo "TEST: a matching account passes --check and emits the preflight summary"
(
  make_sandbox
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when the live account matches" || fail "exit 0 when the live account matches (got $rc)"
  assert_contains "$out" "999999999999" "summary shows the account id"
  assert_contains "$out" "arn:aws:iam::999999999999:user/operator" "summary shows the caller ARN"
  assert_contains "$out" "eu-west-3" "summary shows the region"
  assert_contains "$out" "truth-in-stream-prod-cluster" "summary shows the cluster"
  assert_contains "$out" "prod" "summary shows the environment"
)

echo "TEST: a mismatched account refuses, prints expected vs actual, makes no mutation"
(
  LIVE_ACCOUNT=222222222222 make_sandbox
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on account mismatch" || fail "non-zero exit on account mismatch (got $rc)"
  assert_contains "$out" "222222222222" "prints the actual (live) account"
  assert_contains "$out" "999999999999" "prints the expected account"
  assert_contains "$out" "expected" "labels the expected id"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "a refused guard never scales a fleet"
  assert_not_contains "$log" "run-task" "a refused guard never runs a producer"
)

echo "TEST: the guard distinguishes dev from prod by ENVIRONMENT"
(
  LIVE_ACCOUNT=111111111111 ENVIRONMENT=dev make_sandbox
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "dev account matches the dev entry" || fail "dev account matches the dev entry (got $rc)"
  assert_contains "$out" "dev" "summary shows the dev environment"
)

echo "TEST: a dev account hitting the prod entry refuses"
(
  LIVE_ACCOUNT=111111111111 ENVIRONMENT=prod make_sandbox
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero when a dev account targets prod" || fail "non-zero when a dev account targets prod (got $rc)"
  assert_contains "$out" "111111111111" "prints the wrong live account"
)

echo "TEST: failed/missing credentials abort before any compare"
(
  STS_FAIL=1 make_sandbox
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when sts fails" || fail "non-zero exit when sts fails (got $rc)"
  assert_contains "$out" "not authenticated" "explains credentials are missing/expired"
)

echo "TEST: an unknown environment in targets.json aborts clearly"
(
  ENVIRONMENT=staging make_sandbox
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit for an environment not in targets.json" || fail "non-zero exit for unknown environment (got $rc)"
  assert_contains "$out" "staging" "names the unknown environment"
)

echo "TEST: a placeholder expected id refuses (guard is not bypassable by all-zeros)"
(
  # targets.json with a placeholder dev id; a real live account must not match it.
  LIVE_ACCOUNT=999999999999 ENVIRONMENT=dev make_sandbox
  # overwrite the dev entry with the all-zeros placeholder
  cat >"$TARGETS_FILE" <<'JSON'
{ "dev": { "account_id": "000000000000", "region": "eu-west-3" } }
JSON
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero when the live account does not match the placeholder" || fail "non-zero on placeholder mismatch (got $rc)"
  assert_contains "$out" "000000000000" "prints the placeholder expected id"
)

echo "TEST: a missing region in the targets entry aborts clearly"
(
  ENVIRONMENT=prod make_sandbox
  cat >"$TARGETS_FILE" <<'JSON'
{ "prod": { "account_id": "999999999999" } }
JSON
  out="$(bash "$GUARD" --check 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the region is absent" || fail "non-zero exit when the region is absent (got $rc)"
  assert_contains "$out" "region" "explains the region is missing"
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "aws-target-guard.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
