#!/usr/bin/env bash
set -euo pipefail

# Drive one ingestion source on an on-demand EC2 host over SSM, then let the
# operator stop the host for cost control. This is the orchestrator behind the
# /crawler and /consumer commands; it composes the account guard
# (aws-target-guard.sh) with EC2 start/stop and `aws ssm send-command`, and runs
# docker-compose.ingest.yml on the host - no ECS, no Fargate.
#
# Two roles, two hosts (resolved by Name tag, like the bastion tunnels):
#   crawler  -> truth-in-stream-<env>-crawler-host : runs a source's PRODUCER,
#               a one-shot that fills the queue and exits.
#   consumer -> truth-in-stream-<env>-consumer-host: brings a source's WORKER up
#               (detached) to drain the queue into the database.
#
# The source -> producer / queue / worker mapping is read from the connector
# registry manifest (stack/backend/internal/connector/sources.json), the single
# source of truth this script and the Go scheduler share, so a new source is a
# registry entry, not an edit here. Today it covers:
#   wikipedia  wikicrawl      / crawl.chunks     / crawlworker
#   stats      statsingest    / embedding.jobs   / embedworker
#   factcheck  factcheckcrawl / factcheck.claims / factcheckworker
#   scrutins   scrutinscrawl  / scrutins.votes   / scrutinsworker
#
# Lifecycle of `up` (the default action):
#   1. Guard    - refuse on the wrong AWS account (deploy/targets.json vs live sts).
#   2. Validate - a crawler producer's required non-secret env is present (secrets
#                 come from Secrets Manager on the host, never from the operator).
#   3. Resolve  - the role's host instance by Name tag; a clear message if absent
#                 (enable_ingestion_hosts is off or not applied) or wrong-account.
#   4. Start    - if the host is stopped, ec2 start-instances, wait running, wait
#                 the SSM agent Online.
#   5. Run      - ssm send-command (AWS-RunShellScript): sync the repo, materialize
#                 the env from Secrets Manager (ingest-fetch-env.sh), log in to ECR,
#                 and `docker compose -f docker-compose.ingest.yml` the service.
#                 The command's output streams to CloudWatch Logs and is mirrored
#                 back here; the container exit code is surfaced.
#   6. Stop     - with --stop-after (or a later `down`), ec2 stop-instances so the
#                 host bills only its EBS volume between runs.
#
# Sub-actions:
#   up      (default) start the host if needed and run the service.
#   down    stop the role's host (all its sources) for cost control.
#   status  read-only: the host's instance state and the source's queue depth.
#
# Usage:
#   scripts/ingest-host.sh <crawler|consumer> <source> [up|down|status] [--stop-after]
#   <source>: any name in the registry manifest (wikipedia, stats, factcheck,
#             scrutins, ...)
#
# Configuration resolves through ingestion-common.sh (PROJECT, ENVIRONMENT,
# CLUSTER for the guard summary) and the guard (expected account from
# deploy/targets.json). Image: IMAGE_TAG (default latest) selects the backend ECR
# tag. Host command: INGEST_REPO_URL (default the local origin), INGEST_REPO_REF
# (default main), INGEST_HOST_WORKDIR (default /opt/truth-in-stream),
# INGEST_COMPOSE_FILE (default docker-compose.ingest.yml). Timeouts: INGEST_CMD_TIMEOUT
# (host-side execution bound, default 7200s), INGEST_CMD_POLL_INTERVAL (default 10s),
# INGEST_HOST_START_TIMEOUT (default 300s), INGEST_HOST_POLL_INTERVAL (default 5s),
# INGEST_SSM_ONLINE_TIMEOUT (default 180s), INGEST_CMD_DELIVERY_TIMEOUT (default 600s).
# Queue depth reuses INGEST_METRICS_NAMESPACE (default TruthInStream/RabbitMQ) and
# INGEST_BROKER_NAME (default <project>-<env>). DRY_RUN=1 drives the whole path,
# printing the mutating start/stop/send-command calls and skipping the real waits.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"
# shellcheck source=scripts/aws-target-guard.sh
. "$SCRIPT_DIR/aws-target-guard.sh"

IMAGE_TAG="${IMAGE_TAG:-latest}"
INGEST_REPO_REF="${INGEST_REPO_REF:-main}"
INGEST_HOST_WORKDIR="${INGEST_HOST_WORKDIR:-/opt/truth-in-stream}"
INGEST_COMPOSE_FILE="${INGEST_COMPOSE_FILE:-docker-compose.ingest.yml}"
INGEST_FETCH_ENV_SCRIPT="${INGEST_FETCH_ENV_SCRIPT:-scripts/ingest-fetch-env.sh}"
# The source registry manifest (internal/connector/sources.json) is the single
# source of truth for a source's producer/worker/queue and forwarded env, so
# adding a source is a registry entry, not an edit to this script's case table.
INGEST_SOURCES_MANIFEST="${INGEST_SOURCES_MANIFEST:-$SCRIPT_DIR/../stack/backend/internal/connector/sources.json}"

INGEST_CMD_TIMEOUT="${INGEST_CMD_TIMEOUT:-7200}"
INGEST_CMD_DELIVERY_TIMEOUT="${INGEST_CMD_DELIVERY_TIMEOUT:-600}"
INGEST_CMD_POLL_INTERVAL="${INGEST_CMD_POLL_INTERVAL:-10}"
INGEST_HOST_START_TIMEOUT="${INGEST_HOST_START_TIMEOUT:-300}"
INGEST_HOST_POLL_INTERVAL="${INGEST_HOST_POLL_INTERVAL:-5}"
INGEST_SSM_ONLINE_TIMEOUT="${INGEST_SSM_ONLINE_TIMEOUT:-180}"

INGEST_METRICS_NAMESPACE="${INGEST_METRICS_NAMESPACE:-TruthInStream/RabbitMQ}"
INGEST_BROKER_NAME="${INGEST_BROKER_NAME:-${PROJECT}-${ENVIRONMENT}}"

# --stop-when-idle drain-to-idle self-stop knobs. WORKER_IDLE_TIMEOUT is the
# window a worker's queue must be empty before it exits, handed to the consumer
# containers so they drain and stop themselves; INGEST_DRAIN_TIMEOUT bounds how
# long the host waits for every worker to idle out before it gives up (and leaves
# the host running for inspection), and INGEST_DRAIN_POLL_INTERVAL is that wait's
# poll cadence.
WORKER_IDLE_TIMEOUT="${WORKER_IDLE_TIMEOUT:-300s}"
INGEST_DRAIN_TIMEOUT="${INGEST_DRAIN_TIMEOUT:-3600}"
INGEST_DRAIN_POLL_INTERVAL="${INGEST_DRAIN_POLL_INTERVAL:-15}"

usage() {
  local sources="(see ${INGEST_SOURCES_MANIFEST})"
  if [[ -r "$INGEST_SOURCES_MANIFEST" ]]; then
    sources="$(jq -r '.sources[].name' "$INGEST_SOURCES_MANIFEST" 2>/dev/null | paste -sd' ' - || true)"
  fi
  cat >&2 <<USAGE
usage:
  ingest-host.sh <crawler|consumer> <source> [up|down|status] [--stop-after] [--stop-when-idle]
  <source>: ${sources}
  up               (default) start the host if needed and run the service over SSM
  down             stop the role's host for cost control
  status           read-only: instance state + queue depth
  --stop-after     stop the host after the run (crawler one-shots)
  --stop-when-idle consumer only: hand the workers a drain-to-idle window, wait for
                   every worker to idle-exit, then stop the host (hands-off drain)
USAGE
  exit "${1:-2}"
}

# Set by resolve_source.
SRC_PRODUCER=""     # producer compose service (crawler role)
SRC_WORKER=""       # worker compose service (consumer role)
SRC_QUEUE_BASE=""   # versioned-queue base name (for the status queue-depth read)
SRC_SERVICE=""      # the compose service this run drives (producer or worker)
SRC_REQUIRED_ENV="" # required non-secret producer env (crawler role only)
SRC_FORWARD_ENV=""  # non-secret producer env forwarded into the compose run

# resolve_source ROLE SOURCE: read a source's producer, worker, queue base, and
# non-secret producer env from the connector registry manifest (the single source
# of truth both this script and the Go scheduler share), so adding a source needs
# no edit here. SRC_SERVICE is the producer for the crawler role and the worker for
# the consumer role. Forwarded env is producer config only (workers need nothing
# from the operator) and, by the registry's contract, never contains an API key -
# secrets are read from Secrets Manager on the host, never passed through the SSM
# command, so no secret is ever logged.
resolve_source() {
  local role="$1" source="$2"
  [[ -r "$INGEST_SOURCES_MANIFEST" ]] || ig_fatal "cannot read source manifest ${INGEST_SOURCES_MANIFEST}; set INGEST_SOURCES_MANIFEST"

  local entry
  entry="$(jq -c --arg n "$source" '.sources[] | select(.name==$n)' "$INGEST_SOURCES_MANIFEST" 2>/dev/null || true)"
  if [[ -z "$entry" ]]; then
    local known
    known="$(jq -r '.sources[].name' "$INGEST_SOURCES_MANIFEST" 2>/dev/null | paste -sd' ' - || true)"
    ig_fatal "unknown source '$source'; one of: ${known}"
  fi

  SRC_PRODUCER="$(jq -r '.producer' <<<"$entry")"
  SRC_WORKER="$(jq -r '.worker' <<<"$entry")"
  SRC_QUEUE_BASE="$(jq -r '.queue' <<<"$entry")"
  SRC_REQUIRED_ENV="$(jq -r '(.required_env // []) | join(" ")' <<<"$entry")"
  SRC_FORWARD_ENV="$(jq -r '(.forward_env // []) | join(" ")' <<<"$entry")"

  if [[ "$role" == "crawler" ]]; then
    SRC_SERVICE="$SRC_PRODUCER"
  else
    # The consumer host runs the worker, which needs nothing from the operator.
    SRC_SERVICE="$SRC_WORKER"
    SRC_REQUIRED_ENV=""
    SRC_FORWARD_ENV=""
  fi
}

# validate_env: fail fast if any required non-secret producer env is unset, naming
# every one that is missing. Only the crawler role has required env; secrets are
# never checked here (the host reads them from Secrets Manager).
validate_env() {
  [[ -n "$SRC_REQUIRED_ENV" ]] || return 0
  local var missing=()
  for var in $SRC_REQUIRED_ENV; do
    [[ -n "${!var:-}" ]] || missing+=("$var")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    ig_fatal "missing required env for source '${SOURCE}': ${missing[*]} (set them and re-run)"
  fi
}

# Set by resolve_instance. Named HOST_* (not INSTANCE_*) so the value is never
# confused with, or clobbered by, an inherited environment variable.
HOST_ID=""
HOST_STATE=""

# host_name ROLE: echo the Name tag of the role's host.
host_name() { printf '%s-%s-%s-host' "$PROJECT" "$ENVIRONMENT" "$1"; }

# resolve_instance ROLE: resolve the role's host by Name tag into HOST_ID and
# HOST_STATE, or fail with an actionable message when none exists. Read-only.
# [0][0] yields exactly one id/state pair (or "None"), so a stray second match
# never whitespace-joins into --target-breaking output.
resolve_instance() {
  local name out
  name="$(host_name "$1")"
  out="$(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=${name}" \
      "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[0].Instances[0].[InstanceId,State.Name]' \
    --output text 2>/dev/null)" || out=""
  HOST_ID="$(printf '%s' "$out" | cut -f1)"
  HOST_STATE="$(printf '%s' "$out" | cut -f2)"
  if [[ -z "$HOST_ID" || "$HOST_ID" == "None" ]]; then
    ig_fatal "no host found with Name=${name} in account ${GUARD_ACCOUNT} (${GUARD_REGION}); enable_ingestion_hosts is off or not applied. Fill deploy/targets.json's ${ENVIRONMENT} account id, then 'terraform apply -var enable_ingestion_hosts=true' in stack/terraform/${ENVIRONMENT}."
  fi
}

# wait_running HOST_ID: poll describe-instances until the instance reports
# running, bounded by INGEST_HOST_START_TIMEOUT. Polls (not `aws ec2 wait`) to
# match the codebase's poll style and stay stubbable.
wait_running() {
  local id="$1" timeout="$INGEST_HOST_START_TIMEOUT" interval="$INGEST_HOST_POLL_INTERVAL"
  local waited=0 state
  while :; do
    state="$(aws ec2 describe-instances --instance-ids "$id" \
      --query 'Reservations[0].Instances[0].State.Name' --output text 2>/dev/null)" || state="describe-failed"
    [[ "$state" == "running" ]] && return 0
    [[ "$waited" -ge "$timeout" ]] && ig_fatal "timed out after ${timeout}s waiting for ${id} to reach running (last state: ${state})"
    sleep "$interval"
    waited=$((waited + interval))
  done
}

# wait_ssm_online HOST_ID: poll until the SSM agent reports the instance
# Online, bounded by INGEST_SSM_ONLINE_TIMEOUT. A freshly started host registers
# with Session Manager a little after it reaches running, so send-command would
# fail with InvalidInstanceId until this passes.
wait_ssm_online() {
  local id="$1" timeout="$INGEST_SSM_ONLINE_TIMEOUT" interval="$INGEST_HOST_POLL_INTERVAL"
  local waited=0 ping
  while :; do
    ping="$(aws ssm describe-instance-information \
      --filters "Key=InstanceIds,Values=${id}" \
      --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null)" || ping="unknown"
    [[ "$ping" == "Online" ]] && return 0
    [[ "$waited" -ge "$timeout" ]] && ig_fatal "timed out after ${timeout}s waiting for the SSM agent on ${id} to come Online (last ping: ${ping})"
    sleep "$interval"
    waited=$((waited + interval))
  done
}

# ensure_running: start the host if it is stopped and wait until it is running and
# SSM-online, or no-op when it is already running. Under DRY_RUN the start is
# printed and the waits are skipped (nothing actually starts).
ensure_running() {
  case "$HOST_STATE" in
    running)
      echo "host ${HOST_ID} (${HOST_STATE}) already running" >&2 ;;
    stopped|stopping|pending)
      if [[ "$HOST_STATE" == "stopped" ]]; then
        echo "starting host ${HOST_ID} (was ${HOST_STATE})" >&2
        ig_aws ec2 start-instances --instance-ids "$HOST_ID" >/dev/null
      else
        echo "host ${HOST_ID} is ${HOST_STATE}; waiting for it to come up" >&2
      fi
      if [[ -n "$DRY_RUN" ]]; then
        echo "DRY-RUN would then: wait for ${HOST_ID} running, then SSM Online" >&2
      else
        wait_running "$HOST_ID"
        wait_ssm_online "$HOST_ID"
        echo "host ${HOST_ID} is running and SSM-online" >&2
      fi ;;
    *)
      ig_fatal "host ${HOST_ID} is in an unexpected state '${HOST_STATE}'; retry once it settles" ;;
  esac
}

# shquote VALUE: echo VALUE single-quoted and safe to embed in a shell command,
# so a forwarded producer value with spaces or metacharacters is passed verbatim.
shquote() {
  printf "'%s'" "${1//\'/\'\\\'\'}"
}

# forward_flags: echo the `-e VAR='value'` flags for every set, non-empty
# forwarded producer var. Only non-secret producer config is ever forwarded (see
# resolve_source), so nothing sensitive reaches the SSM command payload.
forward_flags() {
  local var out=""
  for var in $SRC_FORWARD_ENV; do
    if [[ -n "${!var:-}" ]]; then
      out+=" -e ${var}=$(shquote "${!var}")"
    fi
  done
  printf '%s' "$out"
}

# build_remote_script ROLE: echo the bash script send-command runs on the host. It
# syncs the repo (git, installed by the host user-data), materializes the compose
# env from Secrets Manager, logs in to ECR, pulls the pinned image, and runs the
# one service - the producer with `run --rm` (one-shot) for the crawler role, the
# worker with `up -d` (detached, keeps draining) for the consumer role. Every
# interpolated value here is a non-secret identifier (account, region, image tag,
# repo, forwarded producer config); the secrets are fetched on the host.
# drain_wait_snippet: echo the host-side loop that waits for every consumer worker
# container to idle-exit (drain-to-idle), so the caller can stop the host once the
# queues are drained. It reads `docker compose ps` for running worker services and
# breaks when none remain, bounded by INGEST_DRAIN_TIMEOUT. Every host-evaluated
# `$` is escaped so it defers to the host, not this script.
drain_wait_snippet() {
  cat <<SNIP
echo "waiting up to ${INGEST_DRAIN_TIMEOUT}s for consumer workers to drain to idle" >&2
__deadline=\$(( \$(date +%s) + ${INGEST_DRAIN_TIMEOUT} ))
while :; do
  __running="\$(docker compose -f ${INGEST_COMPOSE_FILE} ps --status running --services 2>/dev/null | grep -E 'worker\$' || true)"
  if [ -z "\$__running" ]; then
    echo "all consumer workers have idle-exited; host can stop" >&2
    break
  fi
  if [ "\$(date +%s)" -ge "\$__deadline" ]; then
    echo "timed out after ${INGEST_DRAIN_TIMEOUT}s; workers still running: \$__running" >&2
    exit 1
  fi
  sleep ${INGEST_DRAIN_POLL_INTERVAL}
done
SNIP
}

build_remote_script() {
  local role="$1"
  local registry="${GUARD_ACCOUNT}.dkr.ecr.${GUARD_REGION}.amazonaws.com"
  local image="${registry}/${PROJECT}-${ENVIRONMENT}-backend:${IMAGE_TAG}"
  # Under --stop-when-idle the workers get a drain-to-idle window so they exit once
  # their queue is empty; otherwise it is empty (workers run until SIGTERM). It is
  # non-secret producer/worker config, safe in the SSM payload.
  local idle_window=""
  [[ -n "$STOP_WHEN_IDLE" ]] && idle_window="$WORKER_IDLE_TIMEOUT"
  local run_line
  if [[ "$role" == "crawler" ]]; then
    run_line="docker compose -f ${INGEST_COMPOSE_FILE} run --rm$(forward_flags) ${SRC_SERVICE}"
  else
    run_line="docker compose -f ${INGEST_COMPOSE_FILE} up -d ${SRC_SERVICE}"
    if [[ -n "$STOP_WHEN_IDLE" ]]; then
      run_line+=$'\n'"$(drain_wait_snippet)"
    fi
  fi
  cat <<REMOTE
set -euo pipefail
export INGEST_IMAGE=$(shquote "$image")
export INGEST_ENV=$(shquote "$ENVIRONMENT")
export AWS_REGION=$(shquote "$GUARD_REGION")
export WORKER_IDLE_TIMEOUT=$(shquote "$idle_window")
WORKDIR=$(shquote "$INGEST_HOST_WORKDIR")
REPO=$(shquote "$INGEST_REPO_URL")
REF=$(shquote "$INGEST_REPO_REF")
mkdir -p "\$WORKDIR"
if [ -d "\$WORKDIR/.git" ]; then
  git -C "\$WORKDIR" remote set-url origin "\$REPO"
  git -C "\$WORKDIR" fetch --depth 1 origin "\$REF"
  git -C "\$WORKDIR" checkout -q -B ingest FETCH_HEAD
else
  git clone --depth 1 --branch "\$REF" "\$REPO" "\$WORKDIR"
fi
cd "\$WORKDIR"
bash ${INGEST_FETCH_ENV_SCRIPT} ${role} ${ENVIRONMENT}
aws ecr get-login-password --region "\$AWS_REGION" | docker login --username AWS --password-stdin ${registry}
docker compose -f ${INGEST_COMPOSE_FILE} pull ${SRC_SERVICE}
${run_line}
REMOTE
}

# send_and_wait ROLE: send the host command via ssm send-command and, unless under
# DRY_RUN, poll get-command-invocation until it finishes, stream its output, and
# return the host command's exit status. The command's stdout/stderr also stream
# to CloudWatch Logs under /<project>/<env>/ingest/command for live tailing.
send_and_wait() {
  local role="$1" script params comment cw log_group
  script="$(build_remote_script "$role")"
  params="$(jq -cn --arg s "$script" --arg et "$INGEST_CMD_TIMEOUT" \
    '{commands: [$s], executionTimeout: [$et]}')"
  comment="ingest ${role} ${SOURCE} ${SRC_SERVICE}"
  log_group="/${PROJECT}/${ENVIRONMENT}/ingest/command"
  cw="CloudWatchOutputEnabled=true,CloudWatchLogGroupName=${log_group}"

  if [[ -n "$DRY_RUN" ]]; then
    ig_aws ssm send-command \
      --document-name AWS-RunShellScript \
      --instance-ids "$HOST_ID" \
      --comment "$comment" \
      --timeout-seconds "$INGEST_CMD_DELIVERY_TIMEOUT" \
      --cloud-watch-output-config "$cw" \
      --parameters "$params"
    echo "DRY-RUN would then: poll get-command-invocation until terminal, stream stdout/stderr, and surface the exit code" >&2
    return 0
  fi

  local command_id
  command_id="$(aws ssm send-command \
    --document-name AWS-RunShellScript \
    --instance-ids "$HOST_ID" \
    --comment "$comment" \
    --timeout-seconds "$INGEST_CMD_DELIVERY_TIMEOUT" \
    --cloud-watch-output-config "$cw" \
    --parameters "$params" \
    --query 'Command.CommandId' --output text)"
  [[ -n "$command_id" && "$command_id" != "None" ]] || ig_fatal "ssm send-command returned no command id"
  echo "sent command ${command_id} to ${HOST_ID}; streaming (also in CloudWatch ${log_group})" >&2
  wait_for_command "$command_id"
}

# wait_for_command COMMAND_ID: poll get-command-invocation until the command
# reaches a terminal status, print its captured stdout/stderr, and return 0 on
# Success or 1 otherwise. A transient error before the agent registers the
# invocation (InvocationDoesNotExist) is swallowed and retried, bounded by
# INGEST_CMD_TIMEOUT.
wait_for_command() {
  local command_id="$1" timeout="$INGEST_CMD_TIMEOUT" interval="$INGEST_CMD_POLL_INTERVAL"
  local waited=0 res status code
  while :; do
    res="$(aws ssm get-command-invocation --command-id "$command_id" --instance-id "$HOST_ID" \
      --query '[Status,ResponseCode]' --output text 2>/dev/null)" || res="Pending	-"
    status="$(printf '%s' "$res" | cut -f1)"
    code="$(printf '%s' "$res" | cut -f2)"
    case "$status" in
      Success)
        print_command_output "$command_id"
        echo "command ${command_id} succeeded (exit 0)" >&2
        return 0 ;;
      Failed|Cancelled|TimedOut|Undeliverable|Terminated)
        print_command_output "$command_id"
        echo "command ${command_id} ${status} (exit ${code})" >&2
        return 1 ;;
    esac
    if [[ "$waited" -ge "$timeout" ]]; then
      ig_fatal "timed out after ${timeout}s waiting for command ${command_id} on ${HOST_ID} (last status: ${status}); it may still be running - check CloudWatch or '/${SUBROLE} ${SOURCE} status'"
    fi
    sleep "$interval"
    waited=$((waited + interval))
  done
}

# print_command_output COMMAND_ID: mirror the command's captured stdout to stdout
# and stderr to stderr. Best-effort: a fetch failure never masks the exit code the
# caller already read.
print_command_output() {
  local command_id="$1" out err
  out="$(aws ssm get-command-invocation --command-id "$command_id" --instance-id "$HOST_ID" \
    --query 'StandardOutputContent' --output text 2>/dev/null)" || out=""
  err="$(aws ssm get-command-invocation --command-id "$command_id" --instance-id "$HOST_ID" \
    --query 'StandardErrorContent' --output text 2>/dev/null)" || err=""
  [[ -n "$out" ]] && printf '%s\n' "$out"
  [[ -n "$err" ]] && printf '%s\n' "$err" >&2
  return 0
}

# queue_depth: echo the latest Backlog datapoint for the source's queue base, or
# "None" when the metric has no data. Read-only; the same get-metric-data form the
# /ingest orchestrator uses.
queue_depth() {
  local now start query
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  start="$(date -u -d '10 minutes ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)"
  query="$(jq -cn \
    --arg ns "$INGEST_METRICS_NAMESPACE" \
    --arg broker "$INGEST_BROKER_NAME" \
    --arg base "$SRC_QUEUE_BASE" \
    '[{Id:"d", MetricStat:{Metric:{Namespace:$ns, MetricName:"Backlog", Dimensions:[{Name:"Broker",Value:$broker},{Name:"QueueBase",Value:$base}]}, Period:60, Stat:"Maximum"}, ReturnData:true}]')"
  aws cloudwatch get-metric-data \
    --start-time "$start" --end-time "$now" \
    --metric-data-queries "$query" \
    --query 'MetricDataResults[0].Values[-1]' --output text 2>/dev/null || echo "None"
}

# do_up: start the role's host if needed and run the source's service over SSM,
# then optionally stop the host. Returns the host command's exit status.
do_up() {
  local role="$1"
  resolve_source "$role" "$SOURCE"
  GUARD_SOURCE="$SOURCE" GUARD_FLEET="$SRC_SERVICE" GUARD_PRODUCER="$SRC_SERVICE"
  guard_resolve
  guard_summary
  validate_env
  resolve_instance "$role"
  ensure_running

  local rc=0
  send_and_wait "$role" || rc=$?

  # --stop-after always stops (the crawler one-shot finished); --stop-when-idle
  # stops only on a clean drain (rc 0 means every worker idle-exited), leaving a
  # timed-out host running so the operator can inspect the undrained backlog.
  if [[ -n "$STOP_AFTER" ]] || { [[ -n "$STOP_WHEN_IDLE" && "$rc" -eq 0 ]]; }; then
    local why="--stop-after"; [[ -n "$STOP_WHEN_IDLE" ]] && why="workers idled out"
    echo "stopping host ${HOST_ID} (${why})" >&2
    ig_aws ec2 stop-instances --instance-ids "$HOST_ID" >/dev/null
    echo "host ${HOST_ID} stopping; idle cost drops to its EBS volume" >&2
  elif [[ -n "$STOP_WHEN_IDLE" ]]; then
    echo "workers did not drain to idle (rc=${rc}); host ${HOST_ID} left running for inspection" >&2
  else
    echo "host left running; run '/${SUBROLE} ${SOURCE} down' to stop it and cap cost" >&2
  fi
  return "$rc"
}

# do_down: stop the role's host (all its sources) for cost control. No-op when it
# is already stopped.
do_down() {
  local role="$1"
  resolve_source "$role" "$SOURCE"
  guard_resolve
  resolve_instance "$role"
  case "$HOST_STATE" in
    stopped|stopping)
      echo "host ${HOST_ID} already ${HOST_STATE}" >&2 ;;
    *)
      echo "stopping host ${HOST_ID} (${HOST_STATE})" >&2
      ig_aws ec2 stop-instances --instance-ids "$HOST_ID" >/dev/null
      echo "host ${HOST_ID} stopping; idle cost drops to its EBS volume" >&2 ;;
  esac
}

# do_status: read-only report of caller identity, region, cluster (guard summary),
# the role's host instance state, and the source's queue depth. The guard still
# runs, so status refuses against the wrong account.
do_status() {
  local role="$1"
  resolve_source "$role" "$SOURCE"
  GUARD_SOURCE="$SOURCE" GUARD_FLEET="$SRC_SERVICE"
  guard_resolve
  guard_summary
  resolve_instance "$role"
  echo "host $(host_name "$role"): ${HOST_ID} state=${HOST_STATE}" >&2
  local depth
  depth="$(queue_depth)"
  if [[ -z "$depth" || "$depth" == "None" ]]; then
    echo "queue ${SRC_QUEUE_BASE}: depth unavailable (metrics lambda disabled or not measuring this queue)" >&2
  else
    echo "queue ${SRC_QUEUE_BASE}: backlog=${depth}" >&2
  fi
}

main() {
  ig_require_cmd aws jq

  SUBROLE="${1:-}"
  SOURCE="${2:-}"
  [[ -n "$SUBROLE" && -n "$SOURCE" ]] || usage 2
  case "$SUBROLE" in
    crawler|consumer) ;;
    *) ig_fatal "unknown role '$SUBROLE'; one of: crawler consumer" ;;
  esac

  ACTION="up"
  STOP_AFTER=""
  STOP_WHEN_IDLE=""
  shift 2 || true
  while [[ $# -gt 0 ]]; do
    case "$1" in
      up|down|status) ACTION="$1" ;;
      --stop-after) STOP_AFTER=1 ;;
      --stop-when-idle) STOP_WHEN_IDLE=1 ;;
      *) ig_fatal "unknown argument '$1'; usage: ingest-host.sh <crawler|consumer> <source> [up|down|status] [--stop-after] [--stop-when-idle]" ;;
    esac
    shift
  done

  # --stop-when-idle is a consumer-only, up-only drain: the crawler runs one-shot
  # producers (use --stop-after), and down/status never run a worker to idle out.
  if [[ -n "$STOP_WHEN_IDLE" ]]; then
    [[ "$SUBROLE" == "consumer" ]] || ig_fatal "--stop-when-idle applies to the consumer role only (crawler producers are one-shot; use --stop-after)"
    [[ "$ACTION" == "up" ]] || ig_fatal "--stop-when-idle applies to the 'up' action only"
  fi

  # INGEST_REPO_URL defaults to the local origin so the host clones the same repo;
  # only needed for the `up` action, which runs a host command.
  INGEST_REPO_URL="${INGEST_REPO_URL:-$(git -C "$SCRIPT_DIR/.." config --get remote.origin.url 2>/dev/null || true)}"

  case "$ACTION" in
    up)
      [[ -n "$INGEST_REPO_URL" ]] || ig_fatal "cannot resolve the repository URL for the host to clone; set INGEST_REPO_URL"
      do_up "$SUBROLE" ;;
    down)
      do_down "$SUBROLE" ;;
    status)
      do_status "$SUBROLE" ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
