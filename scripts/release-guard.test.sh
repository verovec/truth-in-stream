#!/usr/bin/env bash
#
# Tests for scripts/release-guard.sh. Builds throwaway git repos so the
# tag-ancestor check runs against a real commit graph (git is deterministic and
# offline here), proving a tag on main passes and a tag off main fails.
# Run: ./scripts/release-guard.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/release-guard.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains() { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }

git_quiet() { git -C "$1" -c init.defaultBranch=main -c user.email=t@t -c user.name=t -c commit.gpgsign=false "${@:2}"; }

# Build a repo with a main branch and a divergent side branch, exposing main as
# a remote-tracking ref origin/main (what the workflow checks against). Returns
# the consumer clone path on stdout via the global REPO.
make_repo() {
  local upstream clone
  upstream="$(mktemp -d "$TMPROOT/up.XXXXXX")"
  git_quiet "$upstream" init -q
  echo a > "$upstream/f"; git_quiet "$upstream" add f; git_quiet "$upstream" commit -q -m c1
  MAIN_C1="$(git_quiet "$upstream" rev-parse HEAD)"
  echo b >> "$upstream/f"; git_quiet "$upstream" commit -q -am c2
  MAIN_HEAD="$(git_quiet "$upstream" rev-parse HEAD)"
  # A side branch off c1 with a commit that never lands on main.
  git_quiet "$upstream" checkout -q -b side "$MAIN_C1"
  echo z > "$upstream/g"; git_quiet "$upstream" add g; git_quiet "$upstream" commit -q -m side1
  SIDE_HEAD="$(git_quiet "$upstream" rev-parse HEAD)"
  git_quiet "$upstream" checkout -q main
  # Consumer clone with origin/main as a remote-tracking ref, plus the side
  # commit fetched so the guard can resolve it (a real CI checkout would have
  # the tagged commit checked out).
  clone="$(mktemp -d "$TMPROOT/cl.XXXXXX")"
  git_quiet "$clone" init -q
  git_quiet "$clone" remote add origin "$upstream"
  git_quiet "$clone" fetch -q origin
  REPO="$clone"
}

echo "TEST: a commit that is the main HEAD passes"
(
  make_repo
  out="$(cd "$REPO" && bash "$GUARD" "$MAIN_HEAD" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 for the main HEAD commit" || fail "exit 0 for main HEAD (got $rc)"
  assert_contains "$out" "release authorized" "reports the release is authorized"
)

echo "TEST: an older commit that is an ancestor of main passes"
(
  make_repo
  out="$(cd "$REPO" && bash "$GUARD" "$MAIN_C1" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 for an ancestor of main" || fail "exit 0 for ancestor (got $rc)"
)

echo "TEST: a commit on a side branch (not on main) fails fast"
(
  make_repo
  out="$(cd "$REPO" && bash "$GUARD" "$SIDE_HEAD" 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit for an off-main commit" || fail "non-zero exit off-main (got $rc)"
  assert_contains "$out" "is NOT on" "explains the commit is not on main"
  assert_contains "$out" "Refusing to deploy" "refuses to deploy"
)

echo "TEST: an unresolvable main-ref is a clear error, not a false pass"
(
  make_repo
  out="$(cd "$REPO" && bash "$GUARD" "$MAIN_HEAD" origin/nonexistent 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when main-ref is missing" || fail "non-zero when main-ref missing (got $rc)"
  assert_contains "$out" "cannot resolve" "explains the main-ref cannot be resolved"
)

echo "TEST: an unresolvable tag commit is a clear error"
(
  make_repo
  out="$(cd "$REPO" && bash "$GUARD" 0000000000000000000000000000000000000000 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit for an unknown tag commit" || fail "non-zero for unknown commit (got $rc)"
  assert_contains "$out" "cannot resolve tag commit" "explains the tag commit cannot be resolved"
)

echo "TEST: missing arguments are rejected"
(
  bash "$GUARD" >/dev/null 2>&1 && fail "rejects missing args" || ok "rejects missing args"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
