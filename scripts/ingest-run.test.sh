#!/usr/bin/env bash
#
# Tests for scripts/ingest-run.sh, the /ingest orchestrator. Stubs the `aws` CLI
# so the full guarded lifecycle (account guard -> required-env validation ->
# family/service preflight -> fleet up -> producer run -> drain wait -> fleet
# down, with trap-teardown) is exercised without an AWS account or real infra.
# The orchestrator composes worker-fleet.sh and run-ingest-task.sh, which run for
# real against the stub. `jq` is used for real. Run: ./scripts/ingest-run.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$SCRIPT_DIR/ingest-run.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH, a throwaway targets.json, and the run
# context resolved via env (CLUSTER/SUBNETS/SECURITY_GROUP) so nothing reaches
# terraform or SSM. The aws stub serves every call the lifecycle makes:
#   sts get-caller-identity      -> account + arn (guard)
#   ecs describe-task-definition -> family preflight (ACTIVE unless FAMILY_MISSING)
#   ecs describe-services        -> service preflight + status counts
#   ecs update-service           -> fleet up/down (logged)
#   ecs run-task / describe-tasks-> producer launch + wait + exit code
#   cloudwatch get-metric-data   -> drain depth (DRAIN_DEPTHS sequence, or absent)
# It logs every call so a test can assert order and that teardown ran.
# Behaviour knobs:
#   LIVE_ACCOUNT     account sts reports (default 965638922723, the prod target)
#   FAMILY_MISSING   describe-task-definition errors (family not provisioned)
#   SERVICE_MISSING  describe-services reports the service in failures (MISSING)
#   TASK_EXIT        producer container exit code (default 0)
#   DRAIN_DEPTHS     space-separated backlog values get-metric-data returns in turn
#   METRICS_ABSENT   get-metric-data returns no datapoints (None)
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  DRAIN_STATE="$SANDBOX/drain.idx"; echo 0 >"$DRAIN_STATE"
  TARGETS="$SANDBOX/targets.json"
  cat >"$TARGETS" <<'JSON'
{ "dev":{"account_id":"111111111111","region":"eu-west-3"}, "prod":{"account_id":"965638922723","region":"eu-west-3"} }
JSON
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
args="$*"
case "$1 $2" in
  "sts get-caller-identity")
    printf '%s\tarn:aws:iam::%s:user/operator\n' "${LIVE_ACCOUNT:-965638922723}" "${LIVE_ACCOUNT:-965638922723}" ;;
  "ecs describe-task-definition")
    if [[ -n "${FAMILY_MISSING:-}" ]]; then
      echo "An error occurred (ClientException): Unable to describe task definition." >&2; exit 255; fi
    echo "ACTIVE" ;;
  "ecs describe-services")
    # A missing service lands in `failures`, so services[0].status queries to the
    # literal "None"; ECS still exits 0. The script tests the status string, not
    # the exit code, so the stub returns "None" for the missing case.
    if [[ -n "${SERVICE_MISSING:-}" ]]; then
      echo "None"
    else
      case "$args" in
        *status*) echo "ACTIVE" ;;
        *) echo "2	2" ;;
      esac
    fi ;;
  "ecs update-service")
    # FLEET_UP_FAIL makes only the scale-UP fail (non-zero desired count); the
    # scale-to-zero teardown still succeeds, so the trap path is observable.
    if [[ -n "${FLEET_UP_FAIL:-}" && "$args" != *"--desired-count 0"* ]]; then
      echo "An error occurred (ServiceNotActiveException)" >&2; exit 254; fi
    echo '{"service":{"desiredCount":0}}' ;;
  "ecs run-task")
    echo "arn:aws:ecs:eu-west-3:1:task/cl/abc123" ;;
  "ecs describe-tasks")
    case "$args" in
      *lastStatus*) echo "STOPPED" ;;
      *stopCode*)   echo "TaskFailedToStart	CannotPullContainerError" ;;
      *)            echo "${TASK_EXIT:-0}" ;;
    esac ;;
  "cloudwatch get-metric-data")
    if [[ -n "${METRICS_ABSENT:-}" ]]; then echo "None"; exit 0; fi
    idx="$(cat "$DRAIN_STATE")"
    read -r -a depths <<< "${DRAIN_DEPTHS:-0}"
    n="$idx"; [[ "$n" -ge "${#depths[@]}" ]] && n=$((${#depths[@]} - 1))
    echo "${depths[$n]}"
    echo $((idx + 1)) >"$DRAIN_STATE" ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG DRAIN_STATE \
    LIVE_ACCOUNT="${LIVE_ACCOUNT:-965638922723}" \
    FAMILY_MISSING="${FAMILY_MISSING:-}" \
    SERVICE_MISSING="${SERVICE_MISSING:-}" \
    FLEET_UP_FAIL="${FLEET_UP_FAIL:-}" \
    TASK_EXIT="${TASK_EXIT:-}" \
    DRAIN_DEPTHS="${DRAIN_DEPTHS:-0}" \
    METRICS_ABSENT="${METRICS_ABSENT:-}" \
    TARGETS_FILE="$TARGETS" \
    CLUSTER="${CLUSTER:-truth-in-stream-prod-cluster}" \
    SUBNETS="${SUBNETS:-subnet-aaa,subnet-bbb}" \
    SECURITY_GROUP="${SECURITY_GROUP:-sg-xyz}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    ENVIRONMENT="${ENVIRONMENT:-prod}" \
    INGEST_POLL_INTERVAL=0 \
    INGEST_DRAIN_POLL_INTERVAL=0 \
    INGEST_DRAIN_TIMEOUT="${INGEST_DRAIN_TIMEOUT:-5}" \
    INGEST_DRAIN_STABLE_POLLS="${INGEST_DRAIN_STABLE_POLLS:-1}" \
    DRY_RUN="${DRY_RUN:-}"
}

# call order helper: line number of the first log line matching a pattern, or a
# large sentinel when absent, so tests can assert one call preceded another.
line_of() { grep -nF -- "$2" "$1" | head -1 | cut -d: -f1; }

echo "TEST: stats runs the full lifecycle in order (guard, up, producer, drain, down)"
(
  DRAIN_DEPTHS="0" make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on a clean run" || fail "exit 0 on a clean run (got $rc)"
  log="$AWS_CALL_LOG"
  assert_contains "$(cat "$log")" "sts get-caller-identity" "runs the account guard"
  assert_contains "$(cat "$log")" "--task-definition truth-in-stream-prod-statsingest" "runs the statsingest producer"
  assert_contains "$(cat "$log")" "--service embedworker" "drives the embedworker fleet"
  up="$(line_of "$log" "--desired-count 2")"; run="$(line_of "$log" "ecs run-task")"; down="$(line_of "$log" "--desired-count 0")"
  [[ -n "$up" && -n "$run" && -n "$down" && "$up" -lt "$run" && "$run" -lt "$down" ]] \
    && ok "scales up, then runs, then scales to zero" || fail "lifecycle order up<run<down (up=$up run=$run down=$down)"
)

echo "TEST: wiki maps to the wikisync family with the bulk override on embedworker"
(
  make_sandbox
  bash "$RUN" wiki --yes >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-wikisync" "wiki uses the wikisync family"
  assert_contains "$log" "-mode=bulk" "wiki runs the bulk override"
  assert_contains "$log" "--service embedworker" "wiki drives the embedworker fleet"
)

echo "TEST: wiki-delta maps to the wikisync family with the delta override"
(
  make_sandbox
  bash "$RUN" wiki-delta --yes >/dev/null 2>&1
  assert_contains "$(cat "$AWS_CALL_LOG")" "-mode=delta" "wiki-delta runs the delta override"
)

echo "TEST: wiki-categories maps to crawlworker + wikicrawl and needs CRAWL_CATEGORIES"
(
  make_sandbox
  CRAWL_CATEGORIES="Cat1,Cat2" bash "$RUN" wiki-categories --yes >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-wikicrawl" "wiki-categories uses the wikicrawl family"
  assert_contains "$log" "--service crawlworker" "wiki-categories drives the crawlworker fleet"
)

echo "TEST: scrutins maps to scrutinsworker + scrutinscrawl"
(
  make_sandbox
  bash "$RUN" scrutins --yes >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-scrutinscrawl" "scrutins uses the scrutinscrawl family"
  assert_contains "$log" "--service scrutinsworker" "scrutins drives the scrutinsworker fleet"
)

echo "TEST: count=N sets the fleet replica count"
(
  make_sandbox
  bash "$RUN" stats count=5 --yes >/dev/null 2>&1
  assert_contains "$(cat "$AWS_CALL_LOG")" "--desired-count 5" "count=5 scales the fleet to 5"
)

echo "TEST: without --yes the run stops at the confirmation gate before any mutation"
(
  make_sandbox
  out="$(bash "$RUN" stats 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when confirmation is withheld" || fail "non-zero exit without --yes (got $rc)"
  assert_contains "$out" "preflight" "shows the preflight summary"
  assert_contains "$out" "--yes" "tells the operator to pass --yes"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "no fleet is scaled before confirmation"
  assert_not_contains "$log" "run-task" "no producer runs before confirmation"
)

echo "TEST: a wrong account refuses before any mutation"
(
  LIVE_ACCOUNT=222222222222 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on wrong account" || fail "non-zero exit on wrong account (got $rc)"
  assert_contains "$out" "965638922723" "prints the expected account"
  assert_contains "$out" "222222222222" "prints the actual account"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "wrong account never scales a fleet"
  assert_not_contains "$log" "run-task" "wrong account never runs a producer"
)

echo "TEST: missing required env for a source fails fast naming the variables"
(
  make_sandbox
  unset FACTCHECK_API_KEY FACTCHECK_QUERIES
  out="$(bash "$RUN" factcheck --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on missing env" || fail "non-zero exit on missing env (got $rc)"
  assert_contains "$out" "FACTCHECK_API_KEY" "names the first missing variable"
  assert_contains "$out" "FACTCHECK_QUERIES" "names the second missing variable"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "missing env never scales a fleet"
  assert_not_contains "$log" "run-task" "missing env never runs a producer"
)

echo "TEST: an absent producer family fails fast before any scale-up"
(
  FAMILY_MISSING=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on absent family" || fail "non-zero exit on absent family (got $rc)"
  assert_contains "$out" "statsingest" "names the missing family"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "absent family never scales a fleet"
  assert_not_contains "$log" "run-task" "absent family never runs a producer"
)

echo "TEST: an absent worker service fails fast before any scale-up"
(
  SERVICE_MISSING=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on absent service" || fail "non-zero exit on absent service (got $rc)"
  assert_contains "$out" "embedworker" "names the missing service"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "absent service never scales a fleet"
)

echo "TEST: a failed producer still tears the fleet back to zero (trap)"
(
  TASK_EXIT=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the producer fails" || fail "non-zero exit when the producer fails (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--desired-count 2" "fleet was scaled up"
  assert_contains "$log" "--desired-count 0" "fleet was torn back to zero by the trap"
)

echo "TEST: a failed scale-up does not run the producer and triggers no spurious teardown"
(
  FLEET_UP_FAIL=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when scale-up fails" || fail "non-zero exit when scale-up fails (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "run-task" "a failed scale-up never runs the producer"
  # FLEET_UP is set only after a successful up, so the trap's teardown is a no-op:
  # no scale-to-zero call should be issued for a fleet that never came up.
  assert_not_contains "$log" "--desired-count 0" "a failed scale-up issues no scale-to-zero"
)

echo "TEST: --keep-fleet skips the teardown"
(
  make_sandbox
  bash "$RUN" stats --yes --keep-fleet >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--desired-count 2" "fleet was scaled up"
  assert_not_contains "$log" "--desired-count 0" "--keep-fleet leaves the fleet up"
)

echo "TEST: drain waits until the queue depth is near zero, then tears down"
(
  DRAIN_DEPTHS="50 10 0" INGEST_DRAIN_STABLE_POLLS=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 once the queue drains" || fail "exit 0 once the queue drains (got $rc)"
  assert_contains "$out" "drained" "reports the queue drained"
  assert_contains "$(cat "$AWS_CALL_LOG")" "get-metric-data" "polled the queue-depth metric"
)

echo "TEST: a drain timeout reports 'not confirmed drained' and leaves the fleet up"
(
  DRAIN_DEPTHS="99" INGEST_DRAIN_TIMEOUT=0 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  assert_contains "$out" "not confirmed drained" "reports the drain was not confirmed"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--desired-count 2" "fleet was scaled up"
  assert_not_contains "$log" "--desired-count 0" "a drain timeout leaves the fleet up by default"
)

echo "TEST: metrics absent degrades: producer runs, drain wait skipped, manual teardown advised"
(
  METRICS_ABSENT=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when metrics are absent" || fail "exit 0 when metrics are absent (got $rc)"
  assert_contains "$out" "ingest status" "tells the operator to watch /ingest status"
  assert_contains "$out" "ingest down" "tells the operator to run /ingest down"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "ecs run-task" "the producer still ran"
  assert_not_contains "$log" "--desired-count 0" "metrics-absent leaves teardown to the operator"
)

echo "TEST: status is read-only and reports identity, region, cluster, and fleet counts"
(
  make_sandbox
  out="$(bash "$RUN" status stats 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on status" || fail "exit 0 on status (got $rc)"
  assert_contains "$out" "965638922723" "status shows the account"
  assert_contains "$out" "truth-in-stream-prod-cluster" "status shows the cluster"
  assert_contains "$out" "desired=2 running=2" "status shows fleet counts"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "update-service" "status never mutates"
  assert_not_contains "$log" "run-task" "status never runs a producer"
)

echo "TEST: down scales a single fleet to zero and runs nothing else"
(
  make_sandbox
  out="$(bash "$RUN" down stats 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on down" || fail "exit 0 on down (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--service embedworker" "down targets the source's fleet"
  assert_contains "$log" "--desired-count 0" "down scales to zero"
  assert_not_contains "$log" "run-task" "down never runs a producer"
)

echo "TEST: an unknown source is rejected before any aws call"
(
  make_sandbox
  out="$(bash "$RUN" bogus --yes 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unknown source" || fail "non-zero exit on unknown source (got $rc)"
  assert_contains "$out" "unknown source" "names the bad source"
  [[ ! -s "$AWS_CALL_LOG" ]] && ok "unknown source makes no aws call" || fail "unknown source makes no aws call (log: $(cat "$AWS_CALL_LOG"))"
)

echo "TEST: DRY_RUN drives the whole lifecycle without a real mutating aws call"
(
  DRY_RUN=1 make_sandbox
  out="$(bash "$RUN" stats --yes 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 under DRY_RUN" || fail "exit 0 under DRY_RUN (got $rc)"
  assert_contains "$out" "DRY-RUN aws ecs update-service" "dry-runs the scale-up"
  assert_contains "$out" "DRY-RUN aws ecs run-task" "dry-runs the producer"
  # No real update-service or run-task should hit the stub under DRY_RUN.
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ecs update-service" "DRY_RUN makes no real update-service call"
  assert_not_contains "$log" "ecs run-task" "DRY_RUN makes no real run-task call"
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "ingest-run.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
