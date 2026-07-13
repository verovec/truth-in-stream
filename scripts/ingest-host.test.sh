#!/usr/bin/env bash
#
# Tests for scripts/ingest-host.sh, the /crawler and /consumer orchestrator. Stubs
# the `aws` CLI so the full guarded lifecycle (account guard -> required-env
# validation -> instance resolve by Name tag -> start-if-stopped + SSM-online wait
# -> ssm send-command with AWS-RunShellScript -> get-command-invocation poll ->
# optional stop) is exercised without an AWS account or real EC2/SSM. `jq` is used
# for real. Run: ./scripts/ingest-host.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$SCRIPT_DIR/ingest-host.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH, a throwaway targets.json, CLUSTER injected
# (so the shared guard never touches terraform), and INGEST_REPO_URL set (so the
# host-clone URL never depends on the local git remote). The aws stub serves every
# call the lifecycle makes and logs each so a test can assert order and payloads:
#   sts get-caller-identity            -> account + arn (guard)
#   ec2 describe-instances (InstanceId)-> resolve: "<id> <state>" (or None if missing)
#   ec2 describe-instances (State.Name)-> running (the start-wait poll)
#   ec2 start-instances / stop-instances -> logged
#   ssm describe-instance-information   -> Online (the SSM-online wait)
#   ssm send-command                    -> a CommandId
#   ssm get-command-invocation          -> Status+ResponseCode, then stdout/stderr
#   cloudwatch get-metric-data          -> queue depth (or None)
# Behaviour knobs:
#   LIVE_ACCOUNT     account sts reports (default 111111111111, the dev target)
#   INSTANCE_STATE   state describe-instances reports for the host (default running)
#   INSTANCE_MISSING describe-instances reports no instance (None)
#   CMD_STATUS       get-command-invocation Status (default Success)
#   CMD_CODE         get-command-invocation ResponseCode (default 0)
#   METRICS_ABSENT   get-metric-data returns None
#   DEPTH            get-metric-data backlog value (default 0)
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
args="$*"
case "$1 $2" in
  "sts get-caller-identity")
    printf '%s\tarn:aws:iam::%s:user/operator\n' "${LIVE_ACCOUNT:-111111111111}" "${LIVE_ACCOUNT:-111111111111}" ;;
  "ec2 describe-instances")
    case "$args" in
      *InstanceId*)
        if [[ -n "${INSTANCE_MISSING:-}" ]]; then echo "None"; else
          printf 'i-abc123\t%s\n' "${INSTANCE_STATE:-running}"; fi ;;
      *) echo "running" ;;
    esac ;;
  "ec2 start-instances")  echo '{"StartingInstances":[]}' ;;
  "ec2 stop-instances")   echo '{"StoppingInstances":[]}' ;;
  "ssm describe-instance-information") echo "Online" ;;
  "ssm send-command")     echo "cmd-0001" ;;
  "ssm get-command-invocation")
    case "$args" in
      *StandardOutputContent*) echo "producer ran on the host" ;;
      *StandardErrorContent*)  echo "" ;;
      *) printf '%s\t%s\n' "${CMD_STATUS:-Success}" "${CMD_CODE:-0}" ;;
    esac ;;
  "cloudwatch get-metric-data")
    if [[ -n "${METRICS_ABSENT:-}" ]]; then echo "None"; else echo "${DEPTH:-0}"; fi ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    LIVE_ACCOUNT="${LIVE_ACCOUNT:-111111111111}" \
    INSTANCE_STATE="${INSTANCE_STATE:-running}" \
    INSTANCE_MISSING="${INSTANCE_MISSING:-}" \
    CMD_STATUS="${CMD_STATUS:-Success}" \
    CMD_CODE="${CMD_CODE:-0}" \
    METRICS_ABSENT="${METRICS_ABSENT:-}" \
    DEPTH="${DEPTH:-0}" \
    TARGETS_FILE="$TARGETS" \
    CLUSTER="${CLUSTER:-truth-in-stream-dev-cluster}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    ENVIRONMENT="${ENVIRONMENT:-dev}" \
    INGEST_REPO_URL="${INGEST_REPO_URL:-https://example.com/truth-in-stream.git}" \
    INGEST_HOST_POLL_INTERVAL=0 \
    INGEST_CMD_POLL_INTERVAL=0 \
    INGEST_HOST_START_TIMEOUT="${INGEST_HOST_START_TIMEOUT:-5}" \
    INGEST_SSM_ONLINE_TIMEOUT="${INGEST_SSM_ONLINE_TIMEOUT:-5}" \
    INGEST_CMD_TIMEOUT="${INGEST_CMD_TIMEOUT:-5}" \
    DRY_RUN="${DRY_RUN:-}"
}

line_of() { grep -nF -- "$2" "$1" | head -1 | cut -d: -f1; }

echo "TEST: crawler up on a stopped host runs the full lifecycle in order (guard, resolve, start, send)"
(
  INSTANCE_STATE=stopped make_sandbox
  out="$(CRAWL_CATEGORIES='Category:Climate' bash "$RUN" crawler wikipedia up 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on a clean crawler run" || fail "exit 0 on a clean crawler run (got $rc)"
  log="$AWS_CALL_LOG"
  assert_contains "$(cat "$log")" "sts get-caller-identity" "runs the account guard"
  assert_contains "$(cat "$log")" "Name=tag:Name,Values=truth-in-stream-dev-crawler-host" "resolves the crawler host by Name tag"
  assert_contains "$(cat "$log")" "ec2 start-instances" "starts the stopped host"
  assert_contains "$(cat "$log")" "AWS-RunShellScript" "runs the host command via AWS-RunShellScript"
  res="$(line_of "$log" "sts get-caller-identity")"; desc="$(line_of "$log" "Name=tag:Name")"; st="$(line_of "$log" "ec2 start-instances")"; snd="$(line_of "$log" "ssm send-command")"
  [[ -n "$res" && -n "$desc" && -n "$st" && -n "$snd" && "$res" -lt "$desc" && "$desc" -lt "$st" && "$st" -lt "$snd" ]] \
    && ok "guard, then resolve, then start, then send" || fail "lifecycle order (guard=$res resolve=$desc start=$st send=$snd)"
)

echo "TEST: crawler up sends the source's producer with run --rm and forwards CRAWL_CATEGORIES"
(
  make_sandbox
  CRAWL_CATEGORIES='Category:Physics' bash "$RUN" crawler wikipedia up >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "run --rm" "producer runs one-shot with run --rm"
  assert_contains "$log" "wikicrawl" "runs the wikicrawl producer service"
  assert_contains "$log" "CRAWL_CATEGORIES=" "forwards the non-secret CRAWL_CATEGORIES"
  assert_contains "$log" "ingest-fetch-env.sh crawler dev" "materializes the env from Secrets Manager on the host"
  assert_contains "$log" "docker compose -f docker-compose.ingest.yml" "drives the cloud compose file"
  assert_contains "$log" ".dkr.ecr.eu-west-3.amazonaws.com/truth-in-stream-dev-backend:" "runs the prebuilt backend ECR image"
)

echo "TEST: a running crawler host is not started again"
(
  INSTANCE_STATE=running make_sandbox
  CRAWL_CATEGORIES='Category:Physics' bash "$RUN" crawler wikipedia up >/dev/null 2>&1
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "ec2 start-instances" "a running host is not re-started"
)

echo "TEST: consumer up brings the worker up detached and resolves the consumer host"
(
  make_sandbox
  out="$(bash "$RUN" consumer wikipedia up 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on a clean consumer run" || fail "exit 0 on a clean consumer run (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "Name=tag:Name,Values=truth-in-stream-dev-consumer-host" "resolves the consumer host by Name tag"
  assert_contains "$log" "up -d" "brings the worker up detached"
  assert_contains "$log" "crawlworker" "runs the crawlworker worker service"
)

echo "TEST: the source map picks the right producer (crawler) and worker (consumer) per source"
for row in "wikipedia wikicrawl crawlworker" "stats statsingest embedworker" "factcheck factcheckcrawl factcheckworker" "scrutins scrutinscrawl scrutinsworker"; do
  set -- $row; src="$1"; producer="$2"; worker="$3"
  (
    make_sandbox
    # Supply the producer's required env so validation passes for every source.
    CRAWL_CATEGORIES='C' FACTCHECK_QUERIES='q' bash "$RUN" crawler "$src" up >/dev/null 2>&1
    assert_contains "$(cat "$AWS_CALL_LOG")" "run --rm" "crawler $src uses run --rm"
    assert_contains "$(cat "$AWS_CALL_LOG")" "$producer" "crawler $src runs the $producer producer"
  )
  (
    make_sandbox
    bash "$RUN" consumer "$src" up >/dev/null 2>&1
    assert_contains "$(cat "$AWS_CALL_LOG")" "up -d" "consumer $src uses up -d"
    assert_contains "$(cat "$AWS_CALL_LOG")" "$worker" "consumer $src runs the $worker worker"
  )
done

echo "TEST: the example template is NOT an operable source (cannot touch a real env)"
(
  make_sandbox
  # The example template is deliberately kept out of the registry manifest, so no
  # operator action can run it against a real environment. It must be rejected as
  # an unknown source before any host start or send-command.
  out="$(EXAMPLE_LABEL='demo' bash "$RUN" crawler example up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "example is rejected as a source" || fail "example is rejected as a source (got $rc)"
  assert_contains "$out" "unknown source 'example'" "names example as unknown"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ec2 start-instances" "example never starts a host"
  assert_not_contains "$log" "ssm send-command" "example never sends a command"
)

echo "TEST: a crawler source's missing required env fails fast before any start or send"
(
  make_sandbox
  unset CRAWL_CATEGORIES
  out="$(bash "$RUN" crawler wikipedia up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on missing producer env" || fail "non-zero exit on missing producer env (got $rc)"
  assert_contains "$out" "CRAWL_CATEGORIES" "names the missing variable"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ec2 start-instances" "missing env never starts a host"
  assert_not_contains "$log" "ssm send-command" "missing env never sends a command"
)

echo "TEST: no API key is ever forwarded into the SSM command (secrets stay in Secrets Manager)"
(
  make_sandbox
  FACTCHECK_QUERIES='retraites' FACTCHECK_API_KEY='super-secret' bash "$RUN" crawler factcheck up >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "FACTCHECK_QUERIES=" "forwards the non-secret FACTCHECK_QUERIES"
  assert_not_contains "$log" "FACTCHECK_API_KEY" "never forwards the FACTCHECK_API_KEY secret"
  assert_not_contains "$log" "super-secret" "never puts a secret value in the command"
)

echo "TEST: a wrong account refuses before any start or send"
(
  LIVE_ACCOUNT=222222222222 make_sandbox
  out="$(CRAWL_CATEGORIES='C' bash "$RUN" crawler wikipedia up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on wrong account" || fail "non-zero exit on wrong account (got $rc)"
  assert_contains "$out" "111111111111" "prints the expected account"
  assert_contains "$out" "222222222222" "prints the actual account"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ec2 start-instances" "wrong account never starts a host"
  assert_not_contains "$log" "ssm send-command" "wrong account never sends a command"
)

echo "TEST: a failed host command surfaces a non-zero exit and reports the status"
(
  CMD_STATUS=Failed CMD_CODE=7 make_sandbox
  out="$(CRAWL_CATEGORIES='C' bash "$RUN" crawler wikipedia up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the host command fails" || fail "non-zero exit when the host command fails (got $rc)"
  assert_contains "$out" "Failed" "reports the failed status"
  assert_contains "$out" "exit 7" "surfaces the container exit code"
)

echo "TEST: --stop-after stops the host after the run"
(
  make_sandbox
  CRAWL_CATEGORIES='C' bash "$RUN" crawler wikipedia up --stop-after >/dev/null 2>&1
  log="$AWS_CALL_LOG"
  assert_contains "$(cat "$log")" "ssm send-command" "the run was sent"
  assert_contains "$(cat "$log")" "ec2 stop-instances" "--stop-after stops the host"
  snd="$(line_of "$log" "ssm send-command")"; stp="$(line_of "$log" "ec2 stop-instances")"
  [[ -n "$snd" && -n "$stp" && "$snd" -lt "$stp" ]] && ok "stops after the run, not before" || fail "stop-after ordering (send=$snd stop=$stp)"
)

echo "TEST: without --stop-after the host is left running for the operator to down"
(
  make_sandbox
  out="$(CRAWL_CATEGORIES='C' bash "$RUN" crawler wikipedia up 2>&1)"
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "ec2 stop-instances" "no auto-stop without --stop-after"
  assert_contains "$out" "down" "tells the operator how to stop the host"
)

echo "TEST: down stops a running host and sends no command"
(
  INSTANCE_STATE=running make_sandbox
  out="$(bash "$RUN" consumer wikipedia down 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on down" || fail "exit 0 on down (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "Name=tag:Name,Values=truth-in-stream-dev-consumer-host" "down targets the role's host"
  assert_contains "$log" "ec2 stop-instances" "down stops the host"
  assert_not_contains "$log" "ssm send-command" "down runs no command"
)

echo "TEST: down on an already-stopped host is a no-op"
(
  INSTANCE_STATE=stopped make_sandbox
  bash "$RUN" crawler wikipedia down >/dev/null 2>&1
  assert_not_contains "$(cat "$AWS_CALL_LOG")" "ec2 stop-instances" "already-stopped host is not stopped again"
)

echo "TEST: status is read-only and reports the host state and queue depth"
(
  INSTANCE_STATE=running DEPTH=42 make_sandbox
  out="$(bash "$RUN" consumer wikipedia status 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on status" || fail "exit 0 on status (got $rc)"
  assert_contains "$out" "111111111111" "status shows the account"
  assert_contains "$out" "state=running" "status shows the host state"
  assert_contains "$out" "backlog=42" "status shows the queue depth"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ec2 start-instances" "status never starts a host"
  assert_not_contains "$log" "ssm send-command" "status never runs a command"
  assert_not_contains "$log" "ec2 stop-instances" "status never stops a host"
)

echo "TEST: status degrades clearly when the queue metric is absent"
(
  METRICS_ABSENT=1 make_sandbox
  out="$(bash "$RUN" consumer stats status 2>&1)"
  assert_contains "$out" "depth unavailable" "reports the depth is unavailable"
)

echo "TEST: an absent host instance fails with an actionable message and no mutation"
(
  INSTANCE_MISSING=1 make_sandbox
  out="$(CRAWL_CATEGORIES='C' bash "$RUN" crawler wikipedia up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when the host is absent" || fail "non-zero exit when the host is absent (got $rc)"
  assert_contains "$out" "enable_ingestion_hosts" "points at the terraform toggle"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ec2 start-instances" "an absent host is never started"
  assert_not_contains "$log" "ssm send-command" "an absent host never gets a command"
)

echo "TEST: an unknown role is rejected before any aws call"
(
  make_sandbox
  out="$(bash "$RUN" bogus wikipedia up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unknown role" || fail "non-zero exit on unknown role (got $rc)"
  assert_contains "$out" "unknown role" "names the bad role"
  [[ ! -s "$AWS_CALL_LOG" ]] && ok "unknown role makes no aws call" || fail "unknown role makes no aws call"
)

echo "TEST: an unknown source is rejected"
(
  make_sandbox
  out="$(bash "$RUN" crawler bogus up 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unknown source" || fail "non-zero exit on unknown source (got $rc)"
  assert_contains "$out" "unknown source" "names the bad source"
)

echo "TEST: DRY_RUN drives the whole path without a real start or send"
(
  INSTANCE_STATE=stopped DRY_RUN=1 make_sandbox
  out="$(CRAWL_CATEGORIES='C' bash "$RUN" crawler wikipedia up 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 under DRY_RUN" || fail "exit 0 under DRY_RUN (got $rc)"
  assert_contains "$out" "DRY-RUN aws ec2 start-instances" "dry-runs the host start"
  assert_contains "$out" "DRY-RUN aws ssm send-command" "dry-runs the send-command"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "ec2 start-instances" "DRY_RUN makes no real start-instances call"
  assert_not_contains "$log" "ssm send-command" "DRY_RUN makes no real send-command call"
  assert_not_contains "$log" "ssm get-command-invocation" "DRY_RUN polls no command"
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "ingest-host.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
