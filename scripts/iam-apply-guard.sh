#!/usr/bin/env bash
#
# Pre-apply IAM guard. Reads the apply-time IAM actions a terraform plan needs
# (the `apply_required_actions` output) and checks each against the apply role
# with `iam simulate-principal-policy`. If the role is missing any, it fails
# before `terraform apply` runs - so a half-applied change never happens because
# the apply role cannot grant itself new permissions - and prints the missing
# actions plus the one manual apply an operator must run with elevated rights.
#
# Usage: ./scripts/iam-apply-guard.sh <plan-json> <apply-role-arn> [env-dir]
#   <plan-json>       output of `terraform show -json <planfile>`
#   <apply-role-arn>  the role CI assumes to apply (AWS_ROLE_ARN)
#   [env-dir]         terraform dir named in the remediation (default: generic)

set -euo pipefail

usage() {
  echo "Usage: $0 <plan-json> <apply-role-arn> [env-dir]" >&2
  exit 2
}

[[ $# -ge 2 ]] || usage
PLAN="$1"
ROLE="$2"
ENV_DIR="${3:-the terraform environment directory}"

[[ -f "$PLAN" ]] || {
  echo "iam-apply-guard: plan json not found: $PLAN" >&2
  exit 2
}

# The required actions are the plan's apply_required_actions output (sorted,
# deduped). Absent or empty means nothing to check.
mapfile -t REQUIRED < <(python3 - "$PLAN" <<'PY'
import sys, json
with open(sys.argv[1]) as f:
    doc = json.load(f)
outs = doc.get("planned_values", {}).get("outputs", {})
val = outs.get("apply_required_actions", {}).get("value") or []
for a in sorted(set(val)):
    print(a)
PY
)

if [[ ${#REQUIRED[@]} -eq 0 ]]; then
  echo "IAM apply guard: no apply permissions declared; nothing to check."
  exit 0
fi

# simulate-principal-policy caps --action-names at 100, so check in batches and
# accumulate every action the role is not allowed (implicit or explicit deny).
denied=()
batch=100
err_file="$(mktemp)"
trap 'rm -f "$err_file"' EXIT
for ((i = 0; i < ${#REQUIRED[@]}; i += batch)); do
  chunk=("${REQUIRED[@]:i:batch}")
  if ! result="$(aws iam simulate-principal-policy \
    --policy-source-arn "$ROLE" \
    --action-names "${chunk[@]}" \
    --output json 2>"$err_file")"; then
    echo "iam-apply-guard: simulate-principal-policy failed:" >&2
    cat "$err_file" >&2
    echo "" >&2
    echo "The apply role likely lacks iam:SimulatePrincipalPolicy. Grant it (and" >&2
    echo "iam:GetRole) to the apply role with elevated credentials, then re-run." >&2
    exit 3
  fi
  # Parse into a variable (not a process substitution) so a parse failure is
  # caught and the guard fails closed - a security gate must never pass silently
  # because it could not read the response.
  if ! chunk_denied="$(printf '%s' "$result" | python3 -c '
import sys, json
for r in json.load(sys.stdin).get("EvaluationResults", []):
    if r.get("EvalDecision") != "allowed":
        print(r["EvalActionName"])
')"; then
    echo "iam-apply-guard: could not parse simulate-principal-policy output." >&2
    echo "Failing closed; do not apply until the guard can verify the role." >&2
    exit 3
  fi
  while IFS= read -r a; do
    [[ -n "$a" ]] && denied+=("$a")
  done <<<"$chunk_denied"
done

if [[ ${#denied[@]} -gt 0 ]]; then
  echo "IAM apply guard: ${ROLE}" >&2
  echo "is missing ${#denied[@]} of ${#REQUIRED[@]} required action(s):" >&2
  for a in "${denied[@]}"; do echo "  - $a" >&2; done
  echo "" >&2
  echo "The apply role cannot grant itself these permissions. Run this apply once" >&2
  echo "with elevated credentials, then re-run CI:" >&2
  echo "  cd ${ENV_DIR}" >&2
  echo "  terraform apply" >&2
  exit 1
fi

echo "IAM apply guard: all ${#REQUIRED[@]} required action(s) granted to ${ROLE}."
