#!/usr/bin/env bash
#
# Gate on the workflows themselves: a branch push must never apply Terraform.
# Only the tag-triggered release may ask _terraform.yml to apply, and the
# `terraform apply` command lives in _terraform.yml alone. The check is first
# proven against throwaway fixtures (so a broken check cannot pass silently),
# then run against the real .github/workflows.
# Run: ./scripts/ci-apply-gate.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKFLOWS="$SCRIPT_DIR/../.github/workflows"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }

# Print every violation found in a workflows directory, one per line; empty
# output means the directory honours the gate.
violations() {
  local dir="$1" file name
  for file in "$dir"/*.yml; do
    name="$(basename "$file")"
    # An `apply:` input set to anything but a literal false. The typed input
    # declaration in _terraform.yml (`apply: { type: boolean, ... }`) is not a
    # value and is skipped.
    if [[ "$name" != "release.yml" ]]; then
      grep -nE '^[[:space:]]*apply:' "$file" \
        | grep -vE 'apply:[[:space:]]*false[[:space:]]*(#.*)?$' \
        | grep -vE 'apply:[[:space:]]*\{[[:space:]]*type:' \
        | sed "s|^|$name: apply requested outside release.yml: |"
    fi
    if [[ "$name" != "_terraform.yml" ]]; then
      grep -nE '^[^#]*terraform[[:space:]]+apply' "$file" \
        | sed "s|^|$name: terraform apply outside _terraform.yml: |"
    fi
  done
}

fixture() {
  local dir
  dir="$(mktemp -d "$TMPROOT/wf.XXXXXX")"
  cat >"$dir/_terraform.yml" <<'YAML'
on:
  workflow_call:
    inputs:
      apply: { type: boolean, default: false }
jobs:
  terraform:
    steps:
      - run: terraform apply -input=false tfplan
YAML
  cat >"$dir/release.yml" <<'YAML'
jobs:
  terraform:
    with:
      apply: true
YAML
  cat >"$dir/terraform.yml" <<'YAML'
jobs:
  dev:
    with:
      apply: false
YAML
  echo "$dir"
}

echo "TEST: a plan-only tree with the release apply is clean"
(
  dir="$(fixture)"
  out="$(violations "$dir")"
  [[ -z "$out" ]] && ok "no violation reported" || fail "unexpected violation: $out"
)

echo "TEST: an apply conditioned on the main ref is caught"
(
  dir="$(fixture)"
  printf 'jobs:\n  dev:\n    with:\n      apply: ${{ github.ref == '"'"'refs/heads/main'"'"' }}\n' >"$dir/terraform.yml"
  out="$(violations "$dir")"
  [[ "$out" == *"terraform.yml: apply requested outside release.yml"* ]] \
    && ok "ref-conditioned apply reported" || fail "ref-conditioned apply not reported: $out"
)

echo "TEST: a literal apply: true outside the release is caught"
(
  dir="$(fixture)"
  printf 'jobs:\n  dev:\n    with:\n      apply: true\n' >"$dir/terraform.yml"
  out="$(violations "$dir")"
  [[ "$out" == *"terraform.yml: apply requested outside release.yml"* ]] \
    && ok "literal apply reported" || fail "literal apply not reported: $out"
)

echo "TEST: a terraform apply step outside _terraform.yml is caught"
(
  dir="$(fixture)"
  printf 'jobs:\n  x:\n    steps:\n      - run: terraform apply -auto-approve\n' >"$dir/pr.yml"
  out="$(violations "$dir")"
  [[ "$out" == *"pr.yml: terraform apply outside _terraform.yml"* ]] \
    && ok "stray terraform apply reported" || fail "stray terraform apply not reported: $out"
)

echo "TEST: the repository workflows honour the gate"
(
  out="$(violations "$WORKFLOWS")"
  [[ -z "$out" ]] && ok "no workflow applies outside the release" || fail "violations: $out"
  grep -qE '^[[:space:]]*apply:[[:space:]]*true' "$WORKFLOWS/release.yml" \
    && ok "release.yml still applies prod" || fail "release.yml no longer passes apply: true"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
