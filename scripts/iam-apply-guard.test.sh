#!/usr/bin/env bash
#
# Tests for scripts/iam-apply-guard.sh. Stubs `aws iam simulate-principal-policy`
# so the required-vs-granted permission diff is exercised without an AWS account.
# Run: ./scripts/iam-apply-guard.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/iam-apply-guard.sh"
ROLE="arn:aws:iam::123456789012:role/truth-in-stream-dev-apply"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH. DENY_ACTIONS (space separated) decide
# which simulated actions come back implicitDeny; everything else is allowed.
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
if [[ -n "${FAIL_SIMULATE:-}" ]]; then
  echo "An error occurred (AccessDenied): not authorized to perform iam:SimulatePrincipalPolicy" >&2
  exit 254
fi
if [[ -n "${BAD_JSON:-}" ]]; then
  # Exit 0 but emit non-JSON, as a throttled/partial CLI response might.
  echo "<html>503 Service Unavailable</html>"
  exit 0
fi
# Collect the action names that follow --action-names (until the next --flag).
collecting=0; actions=()
for a in "$@"; do
  case "$a" in
    --action-names) collecting=1; continue ;;
    --*) collecting=0 ;;
  esac
  [[ $collecting -eq 1 ]] && actions+=("$a")
done
python3 - "$DENY_ACTIONS" "${actions[@]}" <<'PY'
import sys, json
deny = set(sys.argv[1].split())
results = []
for act in sys.argv[2:]:
    results.append({"EvalActionName": act,
                    "EvalDecision": "implicitDeny" if act in deny else "allowed"})
print(json.dumps({"EvaluationResults": results}))
PY
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG DENY_ACTIONS="${DENY_ACTIONS:-}" FAIL_SIMULATE="${FAIL_SIMULATE:-}" BAD_JSON="${BAD_JSON:-}"
}

# Write a terraform `show -json` plan whose apply_required_actions output holds
# the given space-separated actions.
plan_with_actions() {
  local f="$SANDBOX/plan.json"
  python3 - "$f" "$@" <<'PY'
import sys, json
path = sys.argv[1]
acts = sys.argv[2:]
doc = {"planned_values": {"outputs": {
    "apply_required_actions": {"value": acts}}}}
open(path, "w").write(json.dumps(doc))
PY
  echo "$f"
}

echo "TEST: passes when the apply role holds every required action"
(
  DENY_ACTIONS="" make_sandbox
  plan="$(plan_with_actions ec2:CreateVpc ecs:CreateCluster mq:CreateBroker)"
  out="$(bash "$GUARD" "$plan" "$ROLE" stack/terraform/dev 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when all granted" || fail "exit 0 when all granted (got $rc)"
  assert_contains "$out" "3 required action" "reports the count checked"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "simulate-principal-policy" "calls simulate-principal-policy"
  assert_contains "$log" "$ROLE" "simulates against the apply role"
)

echo "TEST: fails fast and lists the missing actions when the role lacks them"
(
  DENY_ACTIONS="mq:CreateBroker lambda:CreateFunction" make_sandbox
  plan="$(plan_with_actions ec2:CreateVpc mq:CreateBroker lambda:CreateFunction)"
  out="$(bash "$GUARD" "$plan" "$ROLE" stack/terraform/dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when permissions are missing" || fail "non-zero exit when missing (got $rc)"
  assert_contains "$out" "mq:CreateBroker" "names the first missing action"
  assert_contains "$out" "lambda:CreateFunction" "names the second missing action"
  assert_not_contains "$out" "ec2:CreateVpc" "does not flag a granted action"
  assert_contains "$out" "terraform apply" "prints the manual-apply remediation"
  assert_contains "$out" "stack/terraform/dev" "names the environment directory in the remediation"
)

echo "TEST: an empty required set is a clean no-op"
(
  DENY_ACTIONS="" make_sandbox
  plan="$(plan_with_actions)"
  out="$(bash "$GUARD" "$plan" "$ROLE" stack/terraform/dev 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when nothing is declared" || fail "exit 0 when nothing declared (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "simulate-principal-policy" "skips the simulate call when there is nothing to check"
)

echo "TEST: missing arguments are rejected"
(
  DENY_ACTIONS="" make_sandbox
  bash "$GUARD" >/dev/null 2>&1 && fail "rejects missing args" || ok "rejects missing args"
)

echo "TEST: more than 100 actions are simulated in batches (API limit)"
(
  DENY_ACTIONS="" make_sandbox
  actions=(); for i in $(seq 1 130); do actions+=("svc:Action$i"); done
  plan="$(plan_with_actions "${actions[@]}")"
  out="$(bash "$GUARD" "$plan" "$ROLE" stack/terraform/dev 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 across batches" || fail "exit 0 across batches (got $rc)"
  calls="$(grep -c simulate-principal-policy "$AWS_CALL_LOG")"
  [[ "$calls" -ge 2 ]] && ok "batches into multiple simulate calls ($calls)" || fail "batches into multiple calls (got $calls)"
)

echo "TEST: a simulate failure (role cannot self-simulate) is reported clearly"
(
  DENY_ACTIONS="" FAIL_SIMULATE=1 make_sandbox
  plan="$(plan_with_actions ec2:CreateVpc)"
  out="$(bash "$GUARD" "$plan" "$ROLE" stack/terraform/dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when simulate fails" || fail "non-zero exit when simulate fails (got $rc)"
  assert_contains "$out" "iam:SimulatePrincipalPolicy" "explains the role needs SimulatePrincipalPolicy"
)

echo "TEST: fails closed when the simulate response is not valid JSON"
(
  DENY_ACTIONS="" BAD_JSON=1 make_sandbox
  plan="$(plan_with_actions ec2:CreateVpc mq:CreateBroker)"
  out="$(bash "$GUARD" "$plan" "$ROLE" stack/terraform/dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unparseable response (fails closed)" || fail "fails closed on bad JSON (got $rc)"
  assert_contains "$out" "Failing closed" "explains it is failing closed"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
