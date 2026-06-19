#!/usr/bin/env bash
set -euo pipefail

# Scale an ingestion worker fleet up for a run and back to zero afterwards, on
# demand, so nothing ingestion-related bills idle between runs. The worker
# services (embedworker, crawlworker, and the gated factcheckworker/scrutinsworker)
# are headless ECS services; their steady-state desired count is zero. This
# script sets the desired count directly with `aws ecs update-service`, which the
# deploy role is already scoped for (ecs:UpdateService on the cluster).
#
# We scale with update-service rather than the worker-lifecycle scale lambda on
# purpose: that lambda is a queue-depth autoscaler (a scheduled tick that reads
# backlog and picks a count), not a "set N now" control. For a human-triggered
# run the operator wants an explicit count up and an explicit zero down, which is
# exactly update-service. The lifecycle lambda still owns image rolls
# (deploy-ingestion.sh); this script only moves the replica count.
#
# Usage:
#   scripts/worker-fleet.sh up   <fleet> [count]   scale the fleet to <count> (default 2)
#   scripts/worker-fleet.sh down <fleet>           scale the fleet to zero
#   scripts/worker-fleet.sh status <fleet>         print desired/running counts
#
#   <fleet> is the ECS service name: embedworker | crawlworker | factcheckworker |
#   scrutinsworker. The worker modules name the service by this bare suffix
#   (aws_ecs_service.name = var.name); only the task-definition family carries the
#   <project>-<env>- prefix, so update-service targets the bare name directly.
#
# Precondition for a non-zero scale to launch tasks: the worker services run
# under an EXTERNAL deployment controller, so a PRIMARY task set must exist (the
# worker-lifecycle deploy lambda creates it via scripts/deploy-ingestion.sh).
# update-service sets the desired count - exactly as the lifecycle scale lambda
# does - but with no task set the count has nothing to launch. Roll the fleet
# once with deploy-ingestion.sh before the first on-demand scale-up.
#
# Configuration comes from terraform outputs / env (see ingestion-common.sh):
# PROJECT, ENVIRONMENT, CLUSTER. DRY_RUN=1 prints the update-service call instead
# of running it, so the target is exercisable without infra or credentials.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"

# The worker fleets this control understands. embedworker and crawlworker are
# provisioned (gated) in prod; factcheckworker and scrutinsworker are foundation
# names that follow the same <project>-<env>-<name> convention once wired.
WORKER_FLEETS="embedworker crawlworker factcheckworker scrutinsworker"

usage() {
  cat >&2 <<'USAGE'
usage: worker-fleet.sh <up|down|status> <fleet> [count]
  up   <fleet> [count]  scale the fleet to <count> (default 2)
  down <fleet>          scale the fleet to zero
  status <fleet>        print desired/running counts
  <fleet>: embedworker | crawlworker | factcheckworker | scrutinsworker
USAGE
  exit "${1:-2}"
}

# valid_fleet NAME: succeed if NAME is one of the known worker fleets.
valid_fleet() {
  local f
  for f in $WORKER_FLEETS; do
    [[ "$1" == "$f" ]] && return 0
  done
  return 1
}

# describe_counts SERVICE: echo "<desired> <running>" for the service, reading
# the desired count and the running count across its deployments. Read-only.
describe_counts() {
  local service="$1"
  aws ecs describe-services \
    --cluster "$CLUSTER" \
    --services "$service" \
    --query 'services[0].[desiredCount,runningCount]' \
    --output text
}

main() {
  local action="${1:-}" fleet="${2:-}"
  [[ -n "$action" && -n "$fleet" ]] || usage 2
  valid_fleet "$fleet" || ig_fatal "unknown worker fleet '$fleet'; one of: $WORKER_FLEETS"

  ig_require_cmd aws
  CLUSTER="$(ig_resolve_cluster)"
  # The ECS service is named by the bare suffix (aws_ecs_service.name = var.name
  # in modules/worker and modules/service); only the task-definition family
  # carries the <project>-<env>- prefix. update-service matches on the service
  # name, so it is the bare fleet name, not the prefixed family.
  local service="${fleet}"

  case "$action" in
    up)
      local count="${3:-2}"
      case "$count" in
        ''|*[!0-9]*) ig_fatal "count must be a non-negative integer, got '$count'" ;;
      esac
      [[ "$count" -ge 1 ]] || ig_fatal "use 'down' to scale to zero, not 'up 0'"
      echo "scaling ${service} up to ${count} on ${CLUSTER}" >&2
      ig_aws ecs update-service \
        --cluster "$CLUSTER" \
        --service "$service" \
        --desired-count "$count" >/dev/null
      echo "${fleet}: desired-count set to ${count}; run an ingest, then 'scripts/worker-fleet.sh down ${fleet}' when the queue drains" >&2
      ;;
    down)
      echo "scaling ${service} to zero on ${CLUSTER}" >&2
      ig_aws ecs update-service \
        --cluster "$CLUSTER" \
        --service "$service" \
        --desired-count 0 >/dev/null
      echo "${fleet}: desired-count set to 0; idle cost is now zero for this fleet" >&2
      ;;
    status)
      local counts
      counts="$(describe_counts "$service")" || ig_fatal "cannot describe ${service}"
      [[ -n "$counts" && "$counts" != "None"* ]] || ig_fatal "service ${service} not found on ${CLUSTER}"
      # shellcheck disable=SC2086
      set -- $counts
      echo "${fleet}: desired=${1} running=${2}"
      ;;
    *)
      ig_fatal "unknown action '$action'; one of: up down status"
      ;;
  esac
}

main "$@"
