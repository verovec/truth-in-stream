#!/usr/bin/env bash
#
# Tests for scripts/pipeline-health.sh, the read-only pipeline health snapshot.
# Stubs the `aws` CLI, `psql`, and `docker` so the local (corpus counts, fleet
# containers) and cloud (host states, queue/DLQ backlog, run recency) sections and
# their graceful degradation are exercised without a database, a Docker daemon, or
# an AWS account. `jq` is used for real. Run: ./scripts/pipeline-health.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$SCRIPT_DIR/pipeline-health.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with stubbed aws/psql/docker on PATH, a throwaway targets.json, and
# CLUSTER injected so the guard never touches terraform. Behaviour knobs:
#   LIVE_ACCOUNT   account sts reports (default 111111111111, the dev target)
#   DB_DOWN        psql exits non-zero (local database unreachable)
#   DOCKER_RUNNING space-separated running compose services (default a full fleet)
#   HOST_STATE     ec2 describe-instances state for the hosts (default running)
#   METRICS_ABSENT get-metric-data returns None (no backlog, no run recency)
#   BACKLOG        backlog value get-metric-data reports (default 3)
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  TARGETS="$SANDBOX/targets.json"
  cat >"$TARGETS" <<'JSON'
{ "dev":{"account_id":"111111111111","region":"eu-west-3"}, "prod":{"account_id":"999999999999","region":"eu-west-3"} }
JSON

  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "sts get-caller-identity")
    printf '%s\tarn:aws:iam::%s:user/operator\n' "${LIVE_ACCOUNT:-111111111111}" "${LIVE_ACCOUNT:-111111111111}" ;;
  "ec2 describe-instances")
    echo "${HOST_STATE:-running}" ;;
  "cloudwatch get-metric-data")
    if [[ -n "${METRICS_ABSENT:-}" ]]; then echo "None"
    elif [[ "$*" == *RunSuccess* ]]; then echo "2026-07-13T04:05:00+00:00"
    else echo "${BACKLOG:-3}"; fi ;;
  *) echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"

  cat >"$BIN/psql" <<'PSQL'
#!/usr/bin/env bash
if [[ -n "${DB_DOWN:-}" ]]; then exit 1; fi
# claims, evidence embedded, evidence un-embedded, political_claims, voting_records
printf '10\t20\t5\t3\t7\n'
PSQL
  chmod +x "$BIN/psql"

  cat >"$BIN/docker" <<'DOCKER'
#!/usr/bin/env bash
# Only `docker compose ... ps --status running --services` is used; print the
# configured running services one per line.
for svc in ${DOCKER_RUNNING:-scheduler crawlworker embedworker}; do echo "$svc"; done
DOCKER
  chmod +x "$BIN/docker"

  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    LIVE_ACCOUNT="${LIVE_ACCOUNT:-111111111111}" \
    DB_DOWN="${DB_DOWN:-}" \
    DOCKER_RUNNING="${DOCKER_RUNNING:-scheduler crawlworker embedworker}" \
    HOST_STATE="${HOST_STATE:-running}" \
    METRICS_ABSENT="${METRICS_ABSENT:-}" \
    BACKLOG="${BACKLOG:-3}" \
    TARGETS_FILE="$TARGETS" \
    CLUSTER="${CLUSTER:-truth-in-stream-dev-cluster}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    ENVIRONMENT="${ENVIRONMENT:-dev}"
}

echo "TEST: a full run renders local corpus counts, fleet containers, and the cloud sections"
(
  make_sandbox
  out="$(bash "$RUN" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on a full run" || fail "exit 0 on a full run (got $rc)"
  assert_contains "$out" "LOCAL PIPELINE" "prints the local section header"
  assert_contains "$out" "20 embedded, 5 un-embedded" "renders the evidence embedded/un-embedded split"
  assert_contains "$out" "voting_records" "renders the voting_records count"
  assert_contains "$out" "scheduler: running" "reports the scheduler container state"
  assert_contains "$out" "workers running: crawlworker embedworker" "lists the running workers"
  assert_contains "$out" "CLOUD PIPELINE" "prints the cloud section header"
  assert_contains "$out" "111111111111" "shows the resolved account"
  assert_contains "$out" "crawler" "reports the crawler host"
  assert_contains "$out" "consumer" "reports the consumer host"
  assert_contains "$out" "backlog=3" "shows the queue backlog"
  assert_contains "$out" "dlq=3" "shows the DLQ depth"
  assert_contains "$out" "last successful run" "shows per-source run recency"
  assert_contains "$out" "2026-07-13T04:05:00" "renders a source's last success timestamp"
)

echo "TEST: the report never issues a mutating AWS call (read-only)"
(
  make_sandbox
  bash "$RUN" >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "start-instances" "never starts a host"
  assert_not_contains "$log" "stop-instances" "never stops a host"
  assert_not_contains "$log" "send-command" "never sends a command"
  assert_not_contains "$log" "put-metric-data" "never writes a metric"
)

echo "TEST: the local section degrades when the database is unreachable"
(
  DB_DOWN=1 make_sandbox
  out="$(bash "$RUN" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "still exits 0 with the database down" || fail "still exits 0 with the database down (got $rc)"
  assert_contains "$out" "corpus (local database): unavailable" "reports the database is unavailable"
  assert_contains "$out" "make up" "tells the operator how to bring the stack up"
  assert_contains "$out" "CLOUD PIPELINE" "still prints the cloud section"
)

echo "TEST: the fleet line notes when no worker is running"
(
  DOCKER_RUNNING="scheduler" make_sandbox
  out="$(bash "$RUN" 2>&1)"
  assert_contains "$out" "workers running: none" "reports no workers running"
)

echo "TEST: the cloud section degrades when the account guard cannot resolve"
(
  LIVE_ACCOUNT=222222222222 make_sandbox
  out="$(bash "$RUN" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "still exits 0 on a guard mismatch" || fail "still exits 0 on a guard mismatch (got $rc)"
  assert_contains "$out" "CLOUD PIPELINE" "still prints the cloud header"
  assert_contains "$out" "the account guard did not resolve" "explains the guard failure"
  assert_contains "$out" "deploy/targets.json" "points at the targets file"
  assert_contains "$out" "20 embedded, 5 un-embedded" "the local section still renders"
)

echo "TEST: the cloud section degrades to an actionable note when targets.json is absent"
(
  make_sandbox
  rm -f "$TARGETS_FILE"
  out="$(bash "$RUN" 2>&1)"
  assert_contains "$out" "the account guard did not resolve" "reports the guard could not resolve without targets.json"
)

echo "TEST: queue and run metrics degrade to n/a when the metric is absent"
(
  METRICS_ABSENT=1 make_sandbox
  out="$(bash "$RUN" 2>&1)"
  assert_contains "$out" "backlog=n/a" "backlog degrades to n/a without a metric"
  assert_contains "$out" "dlq=n/a" "DLQ degrades to n/a without a metric"
  assert_contains "$out" "no success in window" "run recency degrades without a metric"
)

echo "TEST: an absent host is reported, not fatal"
(
  make_sandbox
  # describe-instances returns None (no such instance) -> reported as absent.
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "sts get-caller-identity") printf '111111111111\tarn:aws:iam::111111111111:user/operator\n' ;;
  "ec2 describe-instances") echo "None" ;;
  "cloudwatch get-metric-data") echo "None" ;;
  *) echo "unexpected: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  out="$(bash "$RUN" 2>&1)"
  assert_contains "$out" "absent" "an absent host is reported as absent"
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "pipeline-health.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
