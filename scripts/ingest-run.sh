#!/usr/bin/env bash
set -euo pipefail

# Drive a full, managed ingestion run for one source against the AWS account the
# operator's CLI is authenticated to, then return the worker fleet to zero. This
# is the orchestrator behind the /ingest command; it composes the lower-level
# primitives (aws-target-guard.sh, worker-fleet.sh, run-ingest-task.sh) and adds
# no AWS calls of its own beyond the family/service preflight and the drain poll.
#
# Lifecycle for a run:
#   1. Guard      - aws-target-guard.sh: refuse on a wrong account; present the
#                   preflight summary and stop unless --yes.
#   2. Validate   - the source's required env vars are present (fail fast naming
#                   the missing ones).
#   3. Preflight  - the producer task-definition family and the worker service
#                   exist/are ACTIVE in the target account (fail fast naming what
#                   is missing; no fleet is scaled up).
#   4. Fleet up   - worker-fleet.sh up <fleet> <count>.
#   5. Producer   - run-ingest-task.sh <producer> [-- override]; check the exit.
#   6. Drain      - poll the CloudWatch queue-depth metric until near-zero and
#                   stable, bounded by INGEST_DRAIN_TIMEOUT; degrade when the
#                   metric is absent.
#   7. Teardown   - worker-fleet.sh down <fleet>, unless --keep-fleet.
#
# A shell trap runs the teardown on any error, signal, or early exit (unless
# --keep-fleet), so an aborted or failed run never leaves a fleet billing idle.
# Teardown is idempotent. DRY_RUN=1 drives the whole path without mutating AWS.
#
# Usage:
#   scripts/ingest-run.sh <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]
#   scripts/ingest-run.sh status [source]
#   scripts/ingest-run.sh down <source>
#
#   <source>: stats | wiki | wiki-delta | wiki-categories | factcheck | scrutins
#
# Configuration resolves through ingestion-common.sh (PROJECT, ENVIRONMENT,
# CLUSTER, SUBNETS, SECURITY_GROUP); the expected account comes from
# deploy/targets.json (aws-target-guard.sh). Drain knobs: INGEST_DRAIN_TIMEOUT
# (default 3600s), INGEST_DRAIN_POLL_INTERVAL (default 20s), INGEST_DRAIN_STABLE_POLLS
# (default 3). The CloudWatch namespace is INGEST_METRICS_NAMESPACE
# (default TruthInStream/RabbitMQ) and the broker dimension INGEST_BROKER_NAME
# (default <project>-<env>).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"
# shellcheck source=scripts/aws-target-guard.sh
. "$SCRIPT_DIR/aws-target-guard.sh"

WORKER_FLEET="$SCRIPT_DIR/worker-fleet.sh"
RUN_INGEST="$SCRIPT_DIR/run-ingest-task.sh"

INGEST_METRICS_NAMESPACE="${INGEST_METRICS_NAMESPACE:-TruthInStream/RabbitMQ}"
INGEST_BROKER_NAME="${INGEST_BROKER_NAME:-${PROJECT}-${ENVIRONMENT}}"

usage() {
  cat >&2 <<'USAGE'
usage:
  ingest-run.sh <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]
  ingest-run.sh status [source]
  ingest-run.sh down <source>
  <source>: stats | wiki | wiki-delta | wiki-categories | factcheck | scrutins
USAGE
  exit "${1:-2}"
}

# Set by resolve_source for a known source.
SRC_FLEET=""        # ECS worker service name
SRC_PRODUCER=""     # run-ingest-task.sh ingest name (selects the family + override)
SRC_FAMILY=""       # bare task-definition family suffix (for the preflight)
SRC_QUEUE_BASE=""   # versioned-queue base name the fleet drains (for drain detection)
SRC_REQUIRED_ENV="" # space-separated required env var names (empty = none)

# resolve_source SOURCE: map a source to its fleet, producer, family, queue base,
# and required env, or fail. The producer name is what run-ingest-task.sh resolves
# (it owns the family<->override mapping); SRC_FAMILY is the bare family the
# preflight describes. wiki/wiki-delta share the wikisync family by mode override.
resolve_source() {
  case "$1" in
    stats)
      SRC_FLEET="embedworker";     SRC_PRODUCER="statsingest";    SRC_FAMILY="statsingest"
      SRC_QUEUE_BASE="embedding.jobs"; SRC_REQUIRED_ENV="" ;;
    wiki)
      SRC_FLEET="embedworker";     SRC_PRODUCER="wiki-populate";  SRC_FAMILY="wikisync"
      SRC_QUEUE_BASE="embedding.jobs"; SRC_REQUIRED_ENV="" ;;
    wiki-delta)
      SRC_FLEET="embedworker";     SRC_PRODUCER="wikisync";       SRC_FAMILY="wikisync"
      SRC_QUEUE_BASE="embedding.jobs"; SRC_REQUIRED_ENV="" ;;
    wiki-categories)
      SRC_FLEET="crawlworker";     SRC_PRODUCER="wikicrawl";      SRC_FAMILY="wikicrawl"
      SRC_QUEUE_BASE="crawl.chunks";   SRC_REQUIRED_ENV="CRAWL_CATEGORIES" ;;
    factcheck)
      SRC_FLEET="factcheckworker"; SRC_PRODUCER="factcheckcrawl"; SRC_FAMILY="factcheckcrawl"
      SRC_QUEUE_BASE="factcheck.claims"; SRC_REQUIRED_ENV="FACTCHECK_API_KEY FACTCHECK_QUERIES" ;;
    scrutins)
      SRC_FLEET="scrutinsworker";  SRC_PRODUCER="scrutinscrawl";  SRC_FAMILY="scrutinscrawl"
      SRC_QUEUE_BASE="scrutins.votes"; SRC_REQUIRED_ENV="" ;;
    *)
      ig_fatal "unknown source '$1'; one of: stats wiki wiki-delta wiki-categories factcheck scrutins" ;;
  esac
}

# validate_env: fail fast if any of the source's required env vars are unset or
# empty, naming every one that is missing so the operator fixes them in one go.
# Secrets are only checked for presence; their values are never printed.
validate_env() {
  [[ -n "$SRC_REQUIRED_ENV" ]] || return 0
  local var missing=()
  for var in $SRC_REQUIRED_ENV; do
    [[ -n "${!var:-}" ]] || missing+=("$var")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    ig_fatal "missing required env for this source: ${missing[*]} (set them and re-run)"
  fi
}

# preflight_family: confirm the producer task-definition family exists and is
# ACTIVE in the target account, else fail fast naming it. describe-task-definition
# exits non-zero when the family is absent. Read-only.
preflight_family() {
  local family="${PROJECT}-${ENVIRONMENT}-${SRC_FAMILY}" status
  status="$(aws ecs describe-task-definition --task-definition "$family" \
    --query 'taskDefinition.status' --output text 2>/dev/null)" || status=""
  [[ "$status" == "ACTIVE" ]] || ig_fatal "producer family '${family}' is not provisioned in this account; Terraform must publish it before /ingest can run this source"
}

# preflight_service: confirm the worker service exists and is ACTIVE on the
# cluster, else fail fast naming it. describe-services exits 0 even for a missing
# service (it lands in failures with status reported as None), so we test the
# status string, not the exit code. Read-only.
preflight_service() {
  [[ -n "$GUARD_CLUSTER" ]] || ig_fatal "internal: GUARD_CLUSTER not resolved; guard_resolve must run before preflight_service"
  local status
  status="$(aws ecs describe-services --cluster "$GUARD_CLUSTER" --services "$SRC_FLEET" \
    --query 'services[0].status' --output text 2>/dev/null)" || status=""
  [[ "$status" == "ACTIVE" ]] || ig_fatal "worker service '${SRC_FLEET}' is not ACTIVE on ${GUARD_CLUSTER} (status: ${status:-none}); Terraform must provision it before /ingest can run this source"
}

# queue_depth: echo the latest Backlog datapoint for the source's queue base, or
# "None" when the metric has no data (lambda disabled or not measuring this
# queue). Read-only; uses the CloudWatch get-metric-data form (current best
# practice over get-metric-statistics) and reads the most recent value.
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

# wait_for_drain: poll the queue depth until it reads near zero for K consecutive
# polls (debounce a transient empty read mid-run), bounded by INGEST_DRAIN_TIMEOUT.
# Returns 0 when drained, 1 on timeout. If the metric is absent (None) on the very
# first read, returns 2 so the caller degrades to the manual-teardown path rather
# than waiting on a metric that will never appear.
#
# Note on the timeout/stable interplay: the timeout is checked between polls, so a
# tiny INGEST_DRAIN_TIMEOUT (e.g. 0) times out before K stable reads can accrue
# unless INGEST_DRAIN_STABLE_POLLS is 1. With the production defaults (timeout
# 3600s, stable 3) a genuinely empty queue confirms long before the timeout.
wait_for_drain() {
  local timeout="${INGEST_DRAIN_TIMEOUT:-3600}" interval="${INGEST_DRAIN_POLL_INTERVAL:-20}"
  local stable_target="${INGEST_DRAIN_STABLE_POLLS:-3}"
  local waited=0 stable=0 first=1 depth
  while :; do
    depth="$(queue_depth)"
    if [[ "$first" == "1" && ( -z "$depth" || "$depth" == "None" ) ]]; then
      return 2
    fi
    first=0
    if [[ -z "$depth" || "$depth" == "None" ]]; then
      # A transient empty read after data has been seen: treat as not-yet-drained
      # rather than drained, so we never tear down on a metric blip.
      stable=0
    elif awk -v d="$depth" 'BEGIN{exit !(d+0 <= 0)}'; then
      stable=$((stable + 1))
      [[ "$stable" -ge "$stable_target" ]] && return 0
    else
      stable=0
    fi
    if [[ "$waited" -ge "$timeout" ]]; then
      return 1
    fi
    sleep "$interval"
    waited=$((waited + interval))
  done
}

# teardown: scale the fleet back to zero, unless --keep-fleet. Idempotent (zeroing
# an already-zero fleet is a no-op) and safe to run from the trap. Guarded by
# TEARDOWN_DONE so the explicit end-of-run call and the EXIT trap do not double it.
TEARDOWN_DONE=""
teardown() {
  [[ -n "$KEEP_FLEET" ]] && return 0
  [[ -n "$TEARDOWN_DONE" ]] && return 0
  [[ -n "$FLEET_UP" ]] || return 0
  TEARDOWN_DONE=1
  echo "tearing ${SRC_FLEET} back down to zero" >&2
  "$WORKER_FLEET" down "$SRC_FLEET" || echo "warning: teardown of ${SRC_FLEET} failed; run 'scripts/worker-fleet.sh down ${SRC_FLEET}' or '/ingest down ${SOURCE}'" >&2
}

# do_status SOURCE: read-only report of caller identity, region, cluster, and the
# source's fleet desired/running counts plus queue depth when available. No
# mutation. The guard still runs (so status refuses against the wrong account).
do_status() {
  local source="$1"
  resolve_source "$source"
  guard_resolve
  guard_summary
  local counts
  counts="$(aws ecs describe-services --cluster "$GUARD_CLUSTER" --services "$SRC_FLEET" \
    --query 'services[0].[desiredCount,runningCount]' --output text 2>/dev/null)" || counts=""
  if [[ -z "$counts" || "$counts" == "None"* ]]; then
    echo "fleet ${SRC_FLEET}: not provisioned on ${GUARD_CLUSTER}" >&2
  else
    # shellcheck disable=SC2086
    set -- $counts
    echo "fleet ${SRC_FLEET}: desired=${1} running=${2}" >&2
  fi
  local depth
  depth="$(queue_depth)"
  if [[ -z "$depth" || "$depth" == "None" ]]; then
    echo "queue ${SRC_QUEUE_BASE}: depth unavailable (metrics lambda disabled or not measuring this queue)" >&2
  else
    echo "queue ${SRC_QUEUE_BASE}: backlog=${depth}" >&2
  fi
}

# do_down SOURCE: explicit teardown of a single source's fleet to zero (for a run
# left up with --keep-fleet). Guarded; no producer runs.
do_down() {
  local source="$1"
  resolve_source "$source"
  guard_resolve
  echo "scaling ${SRC_FLEET} to zero on ${GUARD_CLUSTER}" >&2
  "$WORKER_FLEET" down "$SRC_FLEET"
}

# do_run: the managed lifecycle for a source. See the file header. Returns the
# run's exit status; main propagates it. The confirmation gate returns 1 (never
# `exit`) so it can never fire the teardown trap, which is only armed once a fleet
# could be up - well after this point.
do_run() {
  TEARDOWN_DONE=""
  resolve_source "$SOURCE"

  # Guard: refuse on a wrong account, then present the summary and stop unless
  # the operator already confirmed with --yes. No mutation happens before this.
  GUARD_SOURCE="$SOURCE" GUARD_FLEET="$SRC_FLEET" GUARD_COUNT="$COUNT" GUARD_PRODUCER="$SRC_PRODUCER"
  guard_resolve
  guard_summary
  if [[ -z "$ASSUME_YES" ]]; then
    echo "refusing to proceed without confirmation; re-run with --yes to scale the fleet and run the producer" >&2
    return 1
  fi

  validate_env
  preflight_family
  preflight_service

  # From here on a fleet may be up, so the teardown trap must be armed before the
  # first scale-up. Any error/signal past this point tears the fleet back to zero.
  trap 'teardown' EXIT INT TERM

  echo "scaling ${SRC_FLEET} up to ${COUNT} for the ${SOURCE} run" >&2
  "$WORKER_FLEET" up "$SRC_FLEET" "$COUNT"
  FLEET_UP=1

  echo "running the ${SOURCE} producer (${SRC_PRODUCER})" >&2
  if [[ ${#PRODUCER_ARGS[@]} -gt 0 ]]; then
    "$RUN_INGEST" "$SRC_PRODUCER" -- "${PRODUCER_ARGS[@]}"
  else
    "$RUN_INGEST" "$SRC_PRODUCER"
  fi

  # Drain. Producer succeeded; wait for the fleet to clear the backlog before
  # tearing it down so in-flight work is not cut off.
  local drain_rc=0
  wait_for_drain || drain_rc=$?
  case "$drain_rc" in
    0)
      echo "queue ${SRC_QUEUE_BASE} drained; tearing the fleet down" >&2
      teardown
      ;;
    2)
      # Metrics absent: cannot observe drain. Degrade rather than fail - the
      # producer exited 0; leave the fleet up and tell the operator to finish it.
      echo "metrics lambda disabled or not measuring ${SRC_QUEUE_BASE}: cannot confirm drain automatically." >&2
      echo "the producer succeeded; watch '/ingest status ${SOURCE}' and run '/ingest down ${SOURCE}' once the queue empties." >&2
      KEEP_FLEET=1
      ;;
    *)
      echo "producer succeeded but queue ${SRC_QUEUE_BASE} not confirmed drained within the timeout; leaving the fleet up." >&2
      echo "watch '/ingest status ${SOURCE}' and run '/ingest down ${SOURCE}' once it empties." >&2
      KEEP_FLEET=1
      ;;
  esac

  trap - EXIT INT TERM
  echo "ingest run for ${SOURCE} complete" >&2
}

main() {
  ig_require_cmd aws jq

  local sub="${1:-}"
  [[ -n "$sub" ]] || usage 2

  case "$sub" in
    status)
      do_status "${2:-stats}"
      return 0 ;;
    down)
      [[ -n "${2:-}" ]] || ig_fatal "down needs a source: ingest-run.sh down <source>"
      do_down "$2"
      return 0 ;;
  esac

  # A run: <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]
  SOURCE="$sub"; shift
  COUNT=2
  KEEP_FLEET=""
  ASSUME_YES=""
  FLEET_UP=""
  PRODUCER_ARGS=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --) shift; PRODUCER_ARGS=("$@"); break ;;
      --keep-fleet) KEEP_FLEET=1 ;;
      --yes) ASSUME_YES=1 ;;
      count=*)
        COUNT="${1#count=}"
        case "$COUNT" in
          ''|*[!0-9]*) ig_fatal "count must be a non-negative integer, got '${COUNT}'" ;;
        esac
        [[ "$COUNT" -ge 1 ]] || ig_fatal "count must be at least 1" ;;
      *) ig_fatal "unknown argument '$1'; usage: ingest-run.sh <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]" ;;
    esac
    shift
  done

  do_run
}

# Only run when executed, not when sourced (the guard is sourced above; nothing
# sources this file, but keep the guard so tests could source it).
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
