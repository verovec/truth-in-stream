#!/usr/bin/env bash
#
# Tests for scripts/run-ingest-task.sh. Stubs the `aws` CLI so the one-shot
# run-task launch (with its network config and optional command override), the
# wait-for-stop, and the exit-code report are exercised without an AWS account or
# a real ECS cluster. `jq` is used for real. The cluster/subnets/SG are injected
# via env so no terraform output or SSM is needed. Run: ./scripts/run-ingest-task.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$SCRIPT_DIR/run-ingest-task.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH. The stub serves the calls the script
# makes (ecs run-task, then describe-tasks for the lastStatus poll and again for
# the exitCode/stop info) and logs every invocation so the test can assert on the
# run-task arguments. describe-tasks dispatches on the --query so one stub covers
# both the poll (lastStatus -> STOPPED) and the report (exitCode). Behaviour knobs:
#   TASK_EXIT       the container exit code describe-tasks reports (default 0)
#   NO_TASK         run-task returns no taskArn (None)
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
args="$*"
case "$1 $2" in
  "ecs run-task")
    if [[ -n "${NO_TASK:-}" ]]; then echo "None"; else
      echo "arn:aws:ecs:eu-west-3:1:task/cl/abc123"; fi ;;
  "ecs describe-tasks")
    case "$args" in
      *lastStatus*)        echo "STOPPED" ;;
      *stopCode*)          echo "TaskFailedToStart	CannotPullContainerError" ;;
      *)                   echo "${TASK_EXIT:-0}" ;;
    esac ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    TASK_EXIT="${TASK_EXIT:-}" \
    NO_TASK="${NO_TASK:-}" \
    INGEST_POLL_INTERVAL=0 \
    CLUSTER="${CLUSTER:-truth-in-stream-prod-cluster}" \
    SUBNETS="${SUBNETS:-subnet-aaa,subnet-bbb}" \
    SECURITY_GROUP="${SECURITY_GROUP:-sg-xyz}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    ENVIRONMENT="${ENVIRONMENT:-prod}" \
    DRY_RUN="${DRY_RUN:-}"
}

echo "TEST: statsingest launches its task with the resolved network config and waits"
(
  make_sandbox
  out="$(bash "$RUN" statsingest 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on a clean ingest" || fail "exit 0 on a clean ingest (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "ecs run-task" "calls run-task"
  assert_contains "$log" "--cluster truth-in-stream-prod-cluster" "targets the resolved cluster"
  assert_contains "$log" "--task-definition truth-in-stream-prod-statsingest" "targets the statsingest family"
  assert_contains "$log" "--launch-type FARGATE" "launches on Fargate"
  assert_contains "$log" "subnet-aaa" "passes the resolved subnets"
  assert_contains "$log" "sg-xyz" "passes the resolved security group"
  assert_contains "$log" "assignPublicIp" "sets an explicit public-ip policy"
  assert_contains "$log" "DISABLED" "private tasks get no public ip"
  assert_contains "$log" "lastStatus" "polls describe-tasks for the stop status"
  assert_contains "$log" "exitCode" "reads the container exit code"
  # statsingest has no default override: the task definition's own command stands.
  assert_not_contains "$log" "--overrides" "statsingest passes no command override"
)

echo "TEST: wikisync defaults to the -mode=delta override on the wikisync family"
(
  make_sandbox
  bash "$RUN" wikisync >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-wikisync" "wikisync targets the prefixed family"
  assert_contains "$log" "--overrides" "wikisync passes a command override"
  assert_contains "$log" "-mode=delta" "wikisync defaults to delta mode"
  # The container override name MUST be the bare container name (var.name in the
  # task definition), never the prefixed family - ECS silently ignores a
  # mismatched containerOverride name, dropping the override.
  assert_contains "$log" '"name":"wikisync"' "override targets the bare container name"
  assert_not_contains "$log" '"name":"truth-in-stream-prod-wikisync"' "override never uses the prefixed family as the container name"
)

echo "TEST: wiki-populate runs the bulk override on the same wikisync family"
(
  make_sandbox
  bash "$RUN" wiki-populate >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-wikisync" "wiki-populate reuses the wikisync family"
  assert_contains "$log" "-mode=bulk" "wiki-populate runs bulk mode"
)

echo "TEST: wikicrawl resolves to its own family with no default override"
(
  make_sandbox
  bash "$RUN" wikicrawl >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-wikicrawl" "wikicrawl targets its prefixed family"
  assert_not_contains "$log" "--overrides" "wikicrawl passes no command override"
)

echo "TEST: factcheckcrawl resolves to its own family with no default override"
(
  make_sandbox
  bash "$RUN" factcheckcrawl >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-factcheckcrawl" "factcheckcrawl targets its prefixed family"
  assert_not_contains "$log" "--overrides" "factcheckcrawl passes no command override"
)

echo "TEST: scrutinscrawl resolves to its own family with no default override"
(
  make_sandbox
  bash "$RUN" scrutinscrawl >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "--task-definition truth-in-stream-prod-scrutinscrawl" "scrutinscrawl targets its prefixed family"
  assert_not_contains "$log" "--overrides" "scrutinscrawl passes no command override"
)

echo "TEST: an explicit -- override replaces the default command"
(
  make_sandbox
  bash "$RUN" wikisync -- -mode=bulk -atomic >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "-atomic" "passes the explicit override through"
  assert_contains "$log" "-mode=bulk" "uses the explicit mode"
)

echo "TEST: a non-zero container exit fails the script"
(
  TASK_EXIT=1 make_sandbox
  out="$(bash "$RUN" statsingest 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the container fails" || fail "non-zero exit when the container fails (got $rc)"
  assert_contains "$out" "FAILED" "reports the failure"
  assert_contains "$out" "exit code 1" "reports the container exit code"
)

echo "TEST: a task that stops before the container runs reports the stop reason"
(
  TASK_EXIT=None make_sandbox
  out="$(bash "$RUN" statsingest 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the container never ran" || fail "non-zero exit when the container never ran (got $rc)"
  assert_contains "$out" "before the container produced an exit code" "explains the container never ran"
  assert_contains "$out" "CannotPullContainerError" "surfaces the task stop reason for diagnosis"
)

echo "TEST: run-task launching no task fails clearly (missing task definition)"
(
  NO_TASK=1 make_sandbox
  out="$(bash "$RUN" statsingest 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when no task launches" || fail "non-zero exit when no task launches (got $rc)"
  assert_contains "$out" "no task" "explains nothing launched"
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "describe-tasks" "never polls when nothing launched"
)

echo "TEST: an unknown ingest is rejected before any aws call"
(
  make_sandbox
  out="$(bash "$RUN" bogus 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unknown ingest" || fail "non-zero exit on unknown ingest (got $rc)"
  assert_contains "$out" "unknown ingest" "names the bad ingest"
  [[ ! -s "$AWS_CALL_LOG" ]] && ok "unknown ingest makes no aws call" || fail "unknown ingest makes no aws call"
)

echo "TEST: DRY_RUN prints the run-task and never launches or waits"
(
  DRY_RUN=1 make_sandbox
  out="$(bash "$RUN" statsingest 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 under DRY_RUN" || fail "exit 0 under DRY_RUN (got $rc)"
  assert_contains "$out" "DRY-RUN aws ecs run-task" "prints the dry-run run-task"
  assert_contains "$out" "would then" "explains the poll it would do"
  if [[ -s "$AWS_CALL_LOG" ]]; then
    fail "DRY_RUN must make no real aws call (log: $(cat "$AWS_CALL_LOG"))"
  else
    ok "DRY_RUN made no real aws call"
  fi
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "run-ingest-task.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
