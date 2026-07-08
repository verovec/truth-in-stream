# shellcheck shell=bash
# Shared configuration resolution for the on-demand ingestion control scripts
# (aws-target-guard.sh, ingest-host.sh, insee-idempotency-check.sh). Sourced,
# never executed directly.
#
# Everything is sourced from terraform outputs, SSM, or the environment - never
# hard-coded - so the same scripts drive any environment the infra publishes:
#   PROJECT      project slug              (default: truth-in-stream)
#   ENVIRONMENT  dev | prod                (default: prod)
#   CLUSTER      ECS cluster name          (default: terraform output ecs_cluster_name)
#   SUBNETS      comma-separated subnet ids (default: SSM .../deploy/private-subnet-ids)
#   SECURITY_GROUP  tasks security group   (default: SSM .../deploy/tasks-security-group-id)
#   AWS_REGION   read by the AWS CLI as usual
#
# DRY_RUN=1 makes every helper that would mutate AWS print the command it would
# run (to stdout, prefixed "DRY-RUN") and skip the call, so a target can be
# exercised end to end without touching infra or needing credentials.

PROJECT="${PROJECT:-truth-in-stream}"
ENVIRONMENT="${ENVIRONMENT:-prod}"
DRY_RUN="${DRY_RUN:-}"

# The terraform root that owns this environment's state, used as the fallback
# source for the cluster name when CLUSTER is not set in the environment.
TF_DIR="${TF_DIR:-stack/terraform/${ENVIRONMENT}}"

# ig_fatal MSG: print an error and exit non-zero. Named to avoid clashing with a
# caller's own helpers.
ig_fatal() {
  echo "error: $1" >&2
  exit 1
}

# ig_require_cmd CMD...: exit non-zero unless every named command is on PATH.
ig_require_cmd() {
  local cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 || ig_fatal "$cmd is required but not on PATH"
  done
}

# ig_ssm_param NAME: read a single SSM parameter's value, decrypted. Used to
# resolve the deploy network config (subnets, security group) the prod root
# publishes under /<project>/<env>/deploy/*.
ig_ssm_param() {
  aws ssm get-parameter --name "$1" --with-decryption \
    --query 'Parameter.Value' --output text
}

# ig_resolve_cluster: echo the ECS cluster name. Prefers an explicit CLUSTER,
# then the environment's terraform output. Fails loudly if neither resolves so a
# misconfigured run never silently targets the wrong cluster.
ig_resolve_cluster() {
  if [[ -n "${CLUSTER:-}" ]]; then
    printf '%s' "$CLUSTER"
    return 0
  fi
  local out
  if out="$(terraform -chdir="$TF_DIR" output -raw ecs_cluster_name 2>/dev/null)" \
    && [[ -n "$out" ]]; then
    printf '%s' "$out"
    return 0
  fi
  ig_fatal "cannot resolve the ECS cluster: set CLUSTER, or run from a checkout where 'terraform -chdir=$TF_DIR output -raw ecs_cluster_name' works"
}

# ig_resolve_ssm_default OVERRIDE SUFFIX NOUN: echo OVERRIDE if non-empty,
# otherwise read /<project>/<env>/deploy/<suffix> from SSM. Rejects an empty or
# "None" value. NOUN names the value in the error messages. Backs both deploy
# network lookups below so their validation and messages never drift.
ig_resolve_ssm_default() {
  local override="$1" suffix="$2" noun="$3"
  if [[ -n "$override" ]]; then
    printf '%s' "$override"
    return 0
  fi
  local val
  val="$(ig_ssm_param "/${PROJECT}/${ENVIRONMENT}/deploy/${suffix}")" \
    || ig_fatal "cannot read deploy ${noun} from SSM; set the override or check credentials"
  [[ -n "$val" && "$val" != "None" ]] || ig_fatal "deploy ${noun} parameter is empty; set the override"
  printf '%s' "$val"
}

# ig_resolve_subnets: echo the comma-separated private subnet ids for run-task.
# Prefers an explicit SUBNETS, then the SSM parameter the prod root publishes.
ig_resolve_subnets() {
  ig_resolve_ssm_default "${SUBNETS:-}" "private-subnet-ids" "subnets (set SUBNETS)"
}

# ig_resolve_security_group: echo the tasks security group id for run-task.
# Prefers an explicit SECURITY_GROUP, then the SSM parameter.
ig_resolve_security_group() {
  ig_resolve_ssm_default "${SECURITY_GROUP:-}" "tasks-security-group-id" "security group (set SECURITY_GROUP)"
}

# ig_aws ARGS...: run an AWS CLI call, or, under DRY_RUN, print it and skip. A
# mutating call (ec2 start/stop-instances, ssm send-command) routes through here
# so the targets are dry-runnable without infra. Read-only calls (describe, wait,
# ssm get-parameter) call aws directly so a dry run still resolves real state
# when credentials are present, and is harmless when they are not.
ig_aws() {
  if [[ -n "$DRY_RUN" ]]; then
    # To stderr so a caller's stdout redirection (e.g. `>/dev/null` on the real
    # call's output) never hides the dry-run line.
    echo "DRY-RUN aws $*" >&2
    return 0
  fi
  aws "$@"
}
