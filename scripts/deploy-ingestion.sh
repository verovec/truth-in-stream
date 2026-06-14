#!/usr/bin/env bash
set -euo pipefail

# Deploy the ingestion pipeline (embedding-queue producer and embedding-worker
# fleet) to a new backend image, optionally rolling the queue message version.
# The producer and worker both run the single backend image with different entry
# points, so the deploy workflow has already built, scanned, and pushed it; this
# script ships that image to the two ingestion workloads:
#
#   - producer: an on-demand `aws ecs run-task` task. We register a new task
#     definition revision pinned to the deployed image so the next run uses the
#     exact image, not a moving :latest.
#   - worker fleet: an ECS service under an EXTERNAL deployment controller owned
#     by the worker-lifecycle deploy lambda. We invoke that lambda to create and
#     promote a new task set, so in-flight messages drain on the old version
#     before the old task set is retired. We never update the service directly,
#     which would bypass the lambda and drop in-flight work.
#
# Queue version roll (optional, explicit, reversible): set QUEUE_VERSIONS to the
# new comma-separated, oldest-first version list (e.g. "1,2") to advance the
# active queue message version in the same deploy. The producer revision and each
# worker family revision are stamped with RABBITMQ_QUEUE_VERSIONS=$QUEUE_VERSIONS
# (the lambda copies the rolled version when it registers its image revision).
# Leave QUEUE_VERSIONS empty to deploy the image without touching the version.
# Roll back by re-running with the previous list.
#
# Configuration (environment variables):
#   PROJECT          required  project slug, e.g. truth-in-stream
#   ENVIRONMENT      required  dev or prod
#   IMAGE            required  the deployed backend image, pinned to an immutable
#                             tag (e.g. <registry>/truth-in-stream-dev-backend:sha-abc1234)
#   QUEUE_VERSIONS   optional  new RABBITMQ_QUEUE_VERSIONS value; empty = no roll
#   WORKER_SERVICES  optional  comma-separated worker ECS service names
#                             (default: embedworker)
#   DEPLOY_FUNCTION  optional  deploy lambda name
#                             (default: <project>-<environment>-workerlifecycle-deploy)
#   AWS_REGION       optional  read by the AWS CLI as usual
#
# A workload that is not provisioned yet (the producer task definition or the
# deploy lambda is absent) is skipped, not fatal, so the deploy succeeds while the
# pipeline is being stood up - mirroring the migrate-task skip in the workflow.

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
WORKER_SERVICES="${WORKER_SERVICES:-embedworker}"
DEPLOY_FUNCTION="${DEPLOY_FUNCTION:-${PROJECT}-${ENVIRONMENT}-workerlifecycle-deploy}"

# Parse WORKER_SERVICES once into a clean array used for both the version roll and
# the lambda payload, so the two never diverge.
WORKER_SVCS=()
IFS=',' read -ra _raw_svcs <<<"$WORKER_SERVICES"
for _s in "${_raw_svcs[@]}"; do
  _s="${_s//[[:space:]]/}"
  [[ -n "$_s" ]] && WORKER_SVCS+=("$_s")
done
if [[ ${#WORKER_SVCS[@]} -eq 0 ]]; then
  echo "deploy-ingestion: WORKER_SERVICES has no service names" >&2
  exit 1
fi

# register_revision FAMILY: read the family's active task definition, swap the
# first container's image to $IMAGE, upsert RABBITMQ_QUEUE_VERSIONS when a roll is
# requested, strip the read-only fields RegisterTaskDefinition rejects, and
# register a new revision. Prints the new revision JSON path to stderr. The caller
# guarantees the family exists (it describes first and skips when absent).
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

deploy_producer() {
  local family="${PROJECT}-${ENVIRONMENT}-producer"
  if ! aws ecs describe-task-definition --task-definition "$family" >/dev/null 2>&1; then
    echo "No producer task definition (${family}); skipping producer deploy." >&2
    return 0
  fi
  echo "Deploying producer ${family} -> ${IMAGE}" >&2
  register_revision "$family"
  if [[ -n "$QUEUE_VERSIONS" ]]; then
    echo "  rolled RABBITMQ_QUEUE_VERSIONS=${QUEUE_VERSIONS}" >&2
  fi
}

# preroll_worker_versions stamps each worker family with the new queue versions
# before the lambda runs, so the lambda's image-revision registration (which
# copies the live revision) carries the rolled version. Only runs on a version
# roll; without one the lambda copies the existing revision unchanged.
preroll_worker_versions() {
  [[ -z "$QUEUE_VERSIONS" ]] && return 0
  local svc family
  for svc in "${WORKER_SVCS[@]}"; do
    family="${PROJECT}-${ENVIRONMENT}-${svc}"
    if ! aws ecs describe-task-definition --task-definition "$family" >/dev/null 2>&1; then
      echo "No worker task definition (${family}); skipping its version roll." >&2
      continue
    fi
    register_revision "$family"
    echo "Rolled ${family} to RABBITMQ_QUEUE_VERSIONS=${QUEUE_VERSIONS}" >&2
  done
}

roll_worker_fleet() {
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
deploy_producer
preroll_worker_versions
roll_worker_fleet
echo "deploy-ingestion: done" >&2
