#!/usr/bin/env bash
#
# Tests for scripts/secret-scan.sh. Builds throwaway git working trees and asserts the
# scan fails on a real-shaped account id in a tracked config/doc/script file, passes on
# placeholders and fixtures, and ignores out-of-scope files (Go source, the embedding
# cache) and untracked/gitignored files. Offline and deterministic.
# Run: ./scripts/secret-scan.test.sh
#
# The account-id-shaped "leak" tokens are assembled at runtime from shorter literals so
# no literal 12-digit run (and no real account id) ever appears in this file - which the
# scanner itself scans, being a tracked *.sh file.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCAN="$SCRIPT_DIR/secret-scan.sh"

# Synthetic 12-digit account-shaped ids, not in the scanner's allow-list. Assembled by
# concatenation so this source carries only 6-digit fragments, never a 12-digit token.
LEAK1="555555""555555"
LEAK2="444444""444444"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains() { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }

git_quiet() { git -C "$1" -c init.defaultBranch=main -c user.email=t@t -c user.name=t -c commit.gpgsign=false "${@:2}"; }

# Build a tracked working tree with the given "path=content" pairs, committed so
# `git ls-files` sees them. Exposes the tree path via the global REPO.
make_tree() {
  local repo; repo="$(mktemp -d "$TMPROOT/tree.XXXXXX")"
  git_quiet "$repo" init -q
  local pair path
  for pair in "$@"; do
    path="${pair%%=*}"; mkdir -p "$repo/$(dirname "$path")"
    printf '%s\n' "${pair#*=}" > "$repo/$path"
  done
  git_quiet "$repo" add -A
  git_quiet "$repo" commit -q -m fixture
  REPO="$repo"
}

run_scan() { SECRET_SCAN_ROOT="$1" bash "$SCAN" 2>&1; }

echo "TEST: a real-shaped account id in a tracked .tf fails the scan"
(
  make_tree "infra/main.tf=provider \"aws\" { allowed_account_ids = [\"$LEAK1\"] }"
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a leaked account id" || fail "non-zero exit on leak (got $rc)"
  assert_contains "$out" "$LEAK1" "reports the offending id"
)

echo "TEST: a real-shaped account id in tracked docs and a script also fails"
(
  make_tree "docs/readme.md=the app account is $LEAK1" "scripts/x.sh=EXPECTED=$LEAK2"
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on leaked ids in md/sh" || fail "non-zero exit (got $rc)"
)

echo "TEST: a leak in a *.example template is caught (name.ext.example)"
(
  make_tree \
    "stack/terraform/main-account/terraform.tfvars.example=main_account_id = \"$LEAK1\"" \
    "config/.env.example=SOME_ARN=arn:aws:iam::$LEAK2:role/x"
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a leak in a .example template" || fail "non-zero exit on .example leak (got $rc): $out"
  assert_contains "$out" "$LEAK1" "reports the id in the .tfvars.example template"
)

echo "TEST: placeholders and fixture ids pass"
(
  make_tree \
    'deploy/targets.example.json={ "prod": { "account_id": "000000000000" } }' \
    'scripts/guard.test.sh=LIVE_ACCOUNT=111111111111 ; OTHER=222222222222 ; FAKE=999999999999' \
    'docs/aws.md=example arn:aws:iam::123456789012:role/x'
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when only placeholders/fixtures are present" || fail "exit 0 for placeholders (got $rc): $out"
  assert_contains "$out" "clean" "reports a clean scan"
)

echo "TEST: out-of-scope files (Go source, embedding cache) are ignored"
(
  make_tree \
    "stack/backend/internal/x_test.go=var v = []float64{$LEAK1, 1}" \
    "stack/backend/seed/embeddings.cache.jsonl={\"v\":[$LEAK2]}" \
    'deploy/targets.example.json={ "prod": { "account_id": "000000000000" } }'
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 - .go and .jsonl are out of scope" || fail "exit 0 for out-of-scope (got $rc): $out"
)

echo "TEST: a 12-digit run inside a longer number is not a false positive"
(
  make_tree 'docs/hash.md=digest 9876543210987654'
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 - no word-bounded 12-digit token" || fail "exit 0 for long number (got $rc): $out"
)

echo "TEST: an untracked (gitignored) file with a real id is not scanned"
(
  make_tree 'deploy/targets.example.json={ "prod": { "account_id": "000000000000" } }'
  printf '%s\n' 'deploy/targets.json' > "$REPO/.gitignore"
  printf '%s\n' "{ \"prod\": { \"account_id\": \"$LEAK1\" } }" > "$REPO/deploy/targets.json"
  git_quiet "$REPO" add .gitignore; git_quiet "$REPO" commit -q -m ignore
  out="$(run_scan "$REPO")"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 - gitignored real value is not tracked, not scanned" || fail "exit 0 for gitignored (got $rc): $out"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
