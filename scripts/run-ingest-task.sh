#!/usr/bin/env bash
set -euo pipefail

# Run a one-shot ingest as a Fargate task, wait for it to stop, and report its
# container exit code, on demand. The one-shot ingests (statsingest, wikisync,
# wiki-populate) are producers that publish to the broker and exit; they are not
# long-running services, so they run as `aws ecs run-task` against a registered
# task-definition family, not as a service. Nothing schedules itself - this is
# entirely operator-triggered.
#
# Each ingest maps to a task-definition family <project>-<env>-<family> and an
# optional command override appended to the image entry point:
#   statsingest    -> family statsingest    (no override; runs the full sweep)
#   wikisync       -> family wikisync        (override -mode=delta, overridable)
#   wiki-populate  -> family wikisync        (override -mode=bulk,  the bulk ingest)
#
# Precondition: the target task-definition family must be provisioned in the
# environment. In prod the wikisync family is gated behind enable_wiki_sync and
# the statsingest family is provisioned by its own enable flag; run-task fails
# fast with a clear message if the family does not exist yet. This script does
# not create infrastructure - it drives families terraform already published.
#
# The launched task drains into the embedding/crawl worker fleet, so bring the
# matching fleet up first (scripts/worker-fleet.sh up embedworker N) for it to
# embed as it ingests.
#
# Usage:
#   scripts/run-ingest-task.sh <ingest> [-- <command override...>]
#
# Configuration comes from terraform outputs / SSM / env (ingestion-common.sh):
# PROJECT, ENVIRONMENT, CLUSTER, SUBNETS, SECURITY_GROUP. DRY_RUN=1 prints the
# run-task call and skips the launch+wait, so the target is exercisable without
# infra or credentials.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"

# resolve_ingest INGEST: set FAMILY and DEFAULT_OVERRIDE for a known ingest, or
# fail. wiki-populate and wikisync share the one wikisync family, differing only
# by the mode override - the same image, two entry points by command. The crawl
# producers (wikicrawl, factcheckcrawl, scrutinscrawl) each own a distinct family
# and take no default override; their behaviour is driven by environment the
# /ingest orchestrator validates, not by a command flag.
resolve_ingest() {
  case "$1" in
    statsingest)
      FAMILY="statsingest"; DEFAULT_OVERRIDE=() ;;
    wikisync)
      FAMILY="wikisync"; DEFAULT_OVERRIDE=("-mode=delta") ;;
    wiki-populate)
      FAMILY="wikisync"; DEFAULT_OVERRIDE=("-mode=bulk") ;;
    wikicrawl)
      FAMILY="wikicrawl"; DEFAULT_OVERRIDE=() ;;
    factcheckcrawl)
      FAMILY="factcheckcrawl"; DEFAULT_OVERRIDE=() ;;
    scrutinscrawl)
      FAMILY="scrutinscrawl"; DEFAULT_OVERRIDE=() ;;
    *)
      ig_fatal "unknown ingest '$1'; one of: statsingest wikisync wiki-populate wikicrawl factcheckcrawl scrutinscrawl" ;;
  esac
}

# build_overrides CONTAINER CMD...: echo the run-task --overrides JSON that pins
# the container command. CONTAINER must equal the container name in the task
# definition - the scheduled-task/worker/service modules set that to the bare
# suffix (container_definitions[].name = var.name, e.g. "wikisync"), NOT the
# <project>-<env>- prefixed family. A containerOverride whose name does not match
# a container in the task definition is silently ignored by ECS, so this MUST be
# the bare name. The caller guarantees at least one command token.
build_overrides() {
  local container="$1"; shift
  local cmd_json
  cmd_json="$(printf '%s\n' "$@" | jq -Rsc 'split("\n") | map(select(length > 0))')"
  jq -cn --arg name "$container" --argjson cmd "$cmd_json" \
    '{containerOverrides: [{name: $name, command: $cmd}]}'
}

main() {
  local ingest="${1:-}"
  [[ -n "$ingest" ]] || ig_fatal "usage: $0 <statsingest|wikisync|wiki-populate|wikicrawl|factcheckcrawl|scrutinscrawl> [-- override...]"
  shift

  local -a override=()
  if [[ "${1:-}" == "--" ]]; then
    shift
    override=("$@")
  fi

  resolve_ingest "$ingest"
  [[ ${#override[@]} -eq 0 ]] && override=("${DEFAULT_OVERRIDE[@]}")

  ig_require_cmd aws jq
  CLUSTER="$(ig_resolve_cluster)"
  local subnets security_group
  subnets="$(ig_resolve_subnets)"
  security_group="$(ig_resolve_security_group)"

  # The task-definition family carries the <project>-<env>- prefix; the container
  # inside it is named by the bare suffix (FAMILY), which is what a command
  # override must target.
  local family="${PROJECT}-${ENVIRONMENT}-${FAMILY}"
  local container="${FAMILY}"
  # awsvpcConfiguration as a structured JSON network config. Tasks run in private
  # subnets behind a NAT, so no public IP.
  local subnets_json network_config overrides_json=""
  # Split on commas and strip surrounding whitespace per token so a "subnet-a,
  # subnet-b" list (spaces after commas) yields clean ids, matching the
  # whitespace tolerance of scripts/deploy-ingestion.sh's split_csv.
  subnets_json="$(printf '%s' "$subnets" | jq -Rc 'split(",") | map(gsub("^\\s+|\\s+$";"")) | map(select(length > 0))')"
  network_config="$(jq -cn --argjson subnets "$subnets_json" --arg sg "$security_group" \
    '{awsvpcConfiguration: {subnets: $subnets, securityGroups: [$sg], assignPublicIp: "DISABLED"}}')"
  # Only build a command override when there is one; an empty override leaves the
  # task definition's own command standing.
  [[ ${#override[@]} -gt 0 ]] && overrides_json="$(build_overrides "$container" "${override[@]}")"

  local override_note=""
  [[ ${#override[@]} -gt 0 ]] && override_note=" override: ${override[*]}"
  echo "running ingest ${ingest} (family ${family}) on ${CLUSTER}${override_note}" >&2

  local -a run_args=(
    ecs run-task
    --cluster "$CLUSTER"
    --task-definition "$family"
    --launch-type FARGATE
    --network-configuration "$network_config"
  )
  [[ -n "$overrides_json" ]] && run_args+=(--overrides "$overrides_json")

  if [[ -n "$DRY_RUN" ]]; then
    ig_aws "${run_args[@]}"
    echo "DRY-RUN would then: poll describe-tasks until STOPPED, then report the container exit code" >&2
    return 0
  fi

  local task_arn
  task_arn="$(aws "${run_args[@]}" --query 'tasks[0].taskArn' --output text)"
  [[ -n "$task_arn" && "$task_arn" != "None" ]] || ig_fatal "run-task launched no task (check the ${family} task definition exists)"
  echo "launched ${task_arn}; waiting for it to stop" >&2

  wait_for_stop "$CLUSTER" "$task_arn"
  report_result "$ingest" "$CLUSTER" "$task_arn"
}

# wait_for_stop CLUSTER TASK_ARN: block until the task reaches STOPPED, polling
# describe-tasks. We poll rather than `aws ecs wait tasks-stopped` because that
# waiter caps at ~10 minutes (100 attempts x 6s) and exits non-zero on timeout -
# a bulk ingest (wiki-populate, the statsingest sweep) routinely runs longer, so
# the waiter would abort a healthy run. INGEST_TIMEOUT (default 7200s) bounds the
# wait; INGEST_POLL_INTERVAL (default 15s) is the poll cadence.
wait_for_stop() {
  local cluster="$1" task_arn="$2"
  local timeout="${INGEST_TIMEOUT:-7200}" interval="${INGEST_POLL_INTERVAL:-15}"
  local waited=0 status
  while :; do
    # A transient describe-tasks failure (throttle, network blip) over a long
    # poll must not abort a healthy run, so swallow the error and keep polling
    # until the timeout; the `|| status=` keeps it safe under set -e.
    status="$(aws ecs describe-tasks --cluster "$cluster" --tasks "$task_arn" \
      --query 'tasks[0].lastStatus' --output text 2>/dev/null)" || status="DESCRIBE_FAILED"
    [[ "$status" == "STOPPED" ]] && return 0
    if [[ "$waited" -ge "$timeout" ]]; then
      ig_fatal "timed out after ${timeout}s waiting for ${task_arn} to stop (last status: ${status}); the task may still be running"
    fi
    sleep "$interval"
    waited=$((waited + interval))
  done
}

# report_result INGEST CLUSTER TASK_ARN: read the container exit code and report.
# A task stopped before its container ran (image pull failure, capacity, manual
# stop) has no exitCode (the query yields "None"/null); in that case we surface
# the task's stopCode/stoppedReason so an infra failure is diagnosable, not just
# reported as "exit code None". Returns non-zero unless the container exited 0.
report_result() {
  local ingest="$1" cluster="$2" task_arn="$3"
  local exit_code
  exit_code="$(aws ecs describe-tasks --cluster "$cluster" --tasks "$task_arn" \
    --query 'tasks[0].containers[0].exitCode' --output text)"
  if [[ "$exit_code" == "0" ]]; then
    echo "ingest ${ingest} succeeded (exit 0)" >&2
    return 0
  fi
  if [[ -z "$exit_code" || "$exit_code" == "None" ]]; then
    local reason
    reason="$(aws ecs describe-tasks --cluster "$cluster" --tasks "$task_arn" \
      --query 'tasks[0].[stopCode,stoppedReason]' --output text)"
    echo "ingest ${ingest} FAILED before the container produced an exit code; task stop info: ${reason}" >&2
    return 1
  fi
  echo "ingest ${ingest} FAILED (container exit code ${exit_code})" >&2
  return 1
}

main "$@"
