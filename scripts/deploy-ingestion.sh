#!/usr/bin/env bash
set -euo pipefail

# Deploy the ingestion pipeline to a new backend image, optionally rolling the
# queue message version. The pipeline has two parallel paths that share the one
# backend image with different entry points, so the deploy workflow has already
# built, scanned, and pushed it; this script ships that image to all of them:
#
#   - producers: on-demand `aws ecs run-task` task definitions. The dump producer
#     (`producer`, /wikisync) and the crawl producer (`wikicrawl`, /wikicrawl).
#     For each we register a new task definition revision pinned to the deployed
#     image so the next run uses the exact image, not a moving :latest.
#   - worker fleets: ECS services under an EXTERNAL deployment controller owned by
#     the worker-lifecycle deploy lambda. The embedding fleet (`embedworker`) and
#     the crawl fleet (`crawlworker`). We invoke that lambda to create and promote
#     a new task set, so in-flight messages drain on the old version before the
#     old task set is retired. We never update the service directly, which would
#     bypass the lambda and drop in-flight work.
#
# Queue version roll (optional, explicit, reversible): set QUEUE_VERSIONS to the
# new comma-separated, oldest-first version list (e.g. "1,2") to advance the
# active queue message version in the same deploy. Each producer revision and
# each worker family revision are stamped with RABBITMQ_QUEUE_VERSIONS=$QUEUE_VERSIONS
# (the lambda copies the rolled version when it registers its image revision); the
# dump and crawl queues share the version machinery, so one roll advances both.
# Leave QUEUE_VERSIONS empty to deploy the image without touching the version.
# Roll back by re-running with the previous list.
#
# Configuration (environment variables):
#   PROJECT           required  project slug, e.g. truth-in-stream
#   ENVIRONMENT       required  dev or prod
#   IMAGE             required  the deployed backend image, pinned to an immutable
#                              tag (e.g. <registry>/truth-in-stream-dev-backend:sha-abc1234)
#   QUEUE_VERSIONS    optional  new RABBITMQ_QUEUE_VERSIONS value; empty = no roll
#   PRODUCER_SERVICES optional  comma-separated producer task families' service
#                              suffixes (default: producer,wikicrawl)
#   WORKER_SERVICES   optional  comma-separated worker ECS service names
#                              (default: embedworker,crawlworker)
#   DEPLOY_FUNCTION   optional  deploy lambda name
#                              (default: <project>-<environment>-workerlifecycle-deploy)
#   AWS_REGION        optional  read by the AWS CLI as usual
#
# A workload that is not provisioned yet (a producer task definition, a worker
# task definition, or the deploy lambda is absent) is skipped, not fatal, so the
# deploy succeeds while the pipeline is being stood up - mirroring the migrate-task
# skip in the workflow. This is what lets the crawl path's workloads default in
# before they exist in every environment.

require() {
  if [[ -z "${!1:-}" ]]; then
    echo "deploy-ingestion: $1 is required" >&2
    exit 1
  fi
}
require PROJECT
require ENVIRONMENT
require IMAGE

QUEUE_VERSIONS="${QUEUE_VERSIONS:-}"
PRODUCER_SERVICES="${PRODUCER_SERVICES:-producer,wikicrawl}"
WORKER_SERVICES="${WORKER_SERVICES:-embedworker,crawlworker}"
DEPLOY_FUNCTION="${DEPLOY_FUNCTION:-${PROJECT}-${ENVIRONMENT}-workerlifecycle-deploy}"

# split_csv LIST: echo each non-empty, whitespace-stripped token of a
# comma-separated list, one per line, so callers read it into a clean array.
split_csv() {
  local raw item
  IFS=',' read -ra raw <<<"$1"
  for item in "${raw[@]}"; do
    item="${item//[[:space:]]/}"
    [[ -n "$item" ]] && printf '%s\n' "$item"
  done
}

mapfile -t PRODUCER_SVCS < <(split_csv "$PRODUCER_SERVICES")
mapfile -t WORKER_SVCS_REQUESTED < <(split_csv "$WORKER_SERVICES")
if [[ ${#WORKER_SVCS_REQUESTED[@]} -eq 0 ]]; then
  echo "deploy-ingestion: WORKER_SERVICES has no service names" >&2
  exit 1
fi

# register_revision FAMILY: read the family's active task definition, swap the
# first container's image to $IMAGE, upsert RABBITMQ_QUEUE_VERSIONS when a roll is
# requested, strip the read-only fields RegisterTaskDefinition rejects, and
# register a new revision. The caller guarantees the family exists (it describes
# first and skips when absent).
register_revision() {
  local family="$1" td input
  td="$(aws ecs describe-task-definition --task-definition "$family")"
  input="$(
    printf '%s' "$td" | jq -c --arg img "$IMAGE" --arg ver "$QUEUE_VERSIONS" '
      .taskDefinition
      | .containerDefinitions[0].image = $img
      | (if $ver != "" then
          .containerDefinitions[0].environment =
            (((.containerDefinitions[0].environment // [])
              | map(select(.name != "RABBITMQ_QUEUE_VERSIONS")))
             + [{name: "RABBITMQ_QUEUE_VERSIONS", value: $ver}])
        else . end)
      | {family, taskRoleArn, executionRoleArn, networkMode, containerDefinitions,
         volumes, placementConstraints, requiresCompatibilities, cpu, memory,
         runtimePlatform, ephemeralStorage, ipcMode, pidMode, proxyConfiguration,
         inferenceAccelerators}
      | with_entries(select(.value != null))
    '
  )"
  aws ecs register-task-definition --cli-input-json "$input" >/dev/null
}

# deploy_producers re-pins each producer family to the deployed image, skipping a
# family that is not provisioned yet. A version roll stamps it in the same call.
deploy_producers() {
  local svc family
  for svc in "${PRODUCER_SVCS[@]}"; do
    family="${PROJECT}-${ENVIRONMENT}-${svc}"
    if ! aws ecs describe-task-definition --task-definition "$family" >/dev/null 2>&1; then
      echo "No producer task definition (${family}); skipping producer deploy." >&2
      continue
    fi
    echo "Deploying producer ${family} -> ${IMAGE}" >&2
    register_revision "$family"
    if [[ -n "$QUEUE_VERSIONS" ]]; then
      echo "  rolled RABBITMQ_QUEUE_VERSIONS=${QUEUE_VERSIONS}" >&2
    fi
  done
}

# filter_provisioned_workers reduces the requested worker services to those whose
# task-definition family exists, so an unprovisioned fleet (e.g. crawlworker
# before the crawl path is stood up) is skipped rather than failing the lambda
# roll, which would error on a service it cannot resolve. Sets WORKER_SVCS.
filter_provisioned_workers() {
  local svc family
  WORKER_SVCS=()
  for svc in "${WORKER_SVCS_REQUESTED[@]}"; do
    family="${PROJECT}-${ENVIRONMENT}-${svc}"
    if aws ecs describe-task-definition --task-definition "$family" >/dev/null 2>&1; then
      WORKER_SVCS+=("$svc")
    else
      echo "No worker task definition (${family}); skipping its roll." >&2
    fi
  done
}

# preroll_worker_versions stamps each worker family with the new queue versions
# before the lambda runs, so the lambda's image-revision registration (which
# copies the live revision) carries the rolled version. Only runs on a version
# roll; without one the lambda copies the existing revision unchanged. Operates on
# the already-filtered, provisioned worker list.
preroll_worker_versions() {
  [[ -z "$QUEUE_VERSIONS" ]] && return 0
  local svc family
  for svc in "${WORKER_SVCS[@]}"; do
    family="${PROJECT}-${ENVIRONMENT}-${svc}"
    register_revision "$family"
    echo "Rolled ${family} to RABBITMQ_QUEUE_VERSIONS=${QUEUE_VERSIONS}" >&2
  done
}

roll_worker_fleet() {
  if [[ ${#WORKER_SVCS[@]} -eq 0 ]]; then
    echo "No provisioned worker services; skipping worker fleet roll." >&2
    return 0
  fi

  local services_json payload outfile errfile resp rc
  services_json="$(printf '%s\n' "${WORKER_SVCS[@]}" | jq -Rsc 'split("\n") | map(select(length > 0))')"
  payload="$(jq -cn --arg img "$IMAGE" --argjson svcs "$services_json" '{image: $img, services: $svcs}')"

  outfile="$(mktemp)"; errfile="$(mktemp)"
  # Guarantee the temp files are removed on every exit path, including an
  # unexpected one under set -e.
  trap 'rm -f "$outfile" "$errfile"' RETURN
  echo "Rolling worker fleet via ${DEPLOY_FUNCTION}: ${payload}" >&2

  set +e
  resp="$(aws lambda invoke \
    --function-name "$DEPLOY_FUNCTION" \
    --cli-binary-format raw-in-base64-out \
    --payload "$payload" \
    "$outfile" 2>"$errfile")"
  rc=$?
  set -e

  if [[ $rc -ne 0 ]]; then
    if grep -q "ResourceNotFoundException" "$errfile"; then
      echo "No worker-lifecycle deploy lambda (${DEPLOY_FUNCTION}); skipping worker roll." >&2
      return 0
    fi
    echo "Worker roll failed (aws lambda invoke exit ${rc}):" >&2
    cat "$errfile" >&2
    return 1
  fi

  # aws lambda invoke exits 0 even when the function itself errors; a FunctionError
  # in the response metadata is the failure signal.
  if printf '%s' "$resp" | jq -e 'has("FunctionError")' >/dev/null 2>&1; then
    echo "Worker roll lambda returned FunctionError:" >&2
    printf '%s\n' "$resp" >&2
    cat "$outfile" >&2
    return 1
  fi

  echo "Worker fleet roll requested for: ${WORKER_SVCS[*]}" >&2
}

echo "deploy-ingestion: ${PROJECT}-${ENVIRONMENT} image=${IMAGE}${QUEUE_VERSIONS:+ queue-versions=${QUEUE_VERSIONS}}" >&2
deploy_producers
filter_provisioned_workers
preroll_worker_versions
roll_worker_fleet
echo "deploy-ingestion: done" >&2
