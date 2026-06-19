#!/usr/bin/env bash
#
# Tests for scripts/insee-idempotency-check.sh. Stubs `psql` so the baseline
# count, the re-run, and the after count are exercised without a real database or
# an open tunnel. The psql stub returns successive counts from a queue file so a
# test can model "no growth" (idempotent, PASS) and "grew" (duplicate, FAIL).
# SKIP_INGEST avoids invoking the real run-ingest-task.sh; one test stubs the
# ingest call on PATH to prove the re-run is wired. Run: ./scripts/insee-idempotency-check.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$SCRIPT_DIR/insee-idempotency-check.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed psql on PATH. Each psql invocation logs its full args
# (so the test can assert on the SQL and that no password is on the argv) and pops
# the next count from COUNTS_FILE. Set COUNTS to a space-separated list of counts
# psql should return in order, e.g. "42 42" for no growth.
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  PSQL_CALL_LOG="$SANDBOX/psql.log"; : >"$PSQL_CALL_LOG"
  COUNTS_FILE="$SANDBOX/counts"
  # shellcheck disable=SC2206  # intentional word-split of the space-separated COUNTS list
  local counts_arr=(${COUNTS:-42 42}); printf '%s\n' "${counts_arr[@]}" >"$COUNTS_FILE"
  cat >"$BIN/psql" <<'PSQL'
#!/usr/bin/env bash
echo "$*" >> "$PSQL_CALL_LOG"
# Pop the first remaining count from the queue and echo it.
val="$(head -n1 "$COUNTS_FILE")"
tail -n +2 "$COUNTS_FILE" > "$COUNTS_FILE.tmp" && mv "$COUNTS_FILE.tmp" "$COUNTS_FILE"
echo "$val"
PSQL
  chmod +x "$BIN/psql"
  export PATH="$BIN:$PATH" PSQL_CALL_LOG COUNTS_FILE \
    SKIP_INGEST="${SKIP_INGEST:-1}" \
    PGPASSWORD="${PGPASSWORD:-s3cret}" \
    PGURL="${PGURL:-postgres://app@localhost:5432/truthinstream}"
}

echo "TEST: equal before/after counts pass (idempotent re-run)"
(
  COUNTS="100 100" make_sandbox
  out="$(bash "$CHECK" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when the count is unchanged" || fail "exit 0 when unchanged (got $rc)"
  assert_contains "$out" "PASS" "reports PASS on no growth"
  assert_contains "$out" "100 -> 100" "shows the unchanged counts"
  log="$(cat "$PSQL_CALL_LOG")"
  assert_contains "$log" "wiki_chunks" "queries the corpus table"
  assert_contains "$log" "corpus LIKE 'insee%'" "counts only the INSEE corpora"
  assert_not_contains "$log" "s3cret" "the password never reaches the psql argv"
)

echo "TEST: a grown count fails (duplicate passages)"
(
  COUNTS="100 137" make_sandbox
  out="$(bash "$CHECK" 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the count grows" || fail "non-zero exit when it grows (got $rc)"
  assert_contains "$out" "FAIL" "reports FAIL on growth"
  assert_contains "$out" "100 -> 137" "shows the changed counts"
)

echo "TEST: the count query path runs psql twice (baseline + after)"
(
  COUNTS="5 5" make_sandbox
  bash "$CHECK" >/dev/null 2>&1
  n="$(grep -c wiki_chunks "$PSQL_CALL_LOG")"
  [[ "$n" -eq 2 ]] && ok "counts twice (baseline and after)" || fail "expected 2 count queries, got $n"
)

echo "TEST: without SKIP_INGEST it re-runs the ingest between counts"
(
  COUNTS="9 9" make_sandbox
  export SKIP_INGEST=""
  # Stub the sibling run-ingest-task.sh by intercepting it: shadow it in BIN is
  # not possible (the script calls it by absolute path), so stub the `aws` it
  # would use and let it dry-run via DRY_RUN, which run-ingest-task honours.
  export DRY_RUN=1
  cat >"$SANDBOX/bin/aws" <<'AWS'
#!/usr/bin/env bash
echo "aws $*" >> "$PSQL_CALL_LOG"
exit 0
AWS
  chmod +x "$SANDBOX/bin/aws"
  # jq is needed by run-ingest-task; ensure it is reachable (real jq on PATH).
  out="$(SUBNETS=subnet-a SECURITY_GROUP=sg-1 CLUSTER=cl bash "$CHECK" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when ingest dry-runs and counts are equal" || fail "exit 0 on the ingest path (got $rc)"
  assert_contains "$out" "re-running statsingest" "announces the re-ingest"
  assert_contains "$out" "DRY-RUN aws ecs run-task" "the re-ingest reaches run-task (dry-run)"
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "insee-idempotency-check.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
