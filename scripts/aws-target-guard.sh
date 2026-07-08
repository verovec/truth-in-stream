#!/usr/bin/env bash
set -euo pipefail

# Refuse to drive an ingestion run against the wrong AWS account. The /crawler and
# /consumer orchestrator (scripts/ingest-host.sh) sources this and calls
# guard_resolve before it starts a host or runs a service; a `status` action and a
# manual preflight run it directly with --check for a read-only summary. It
# performs no mutation of its own.
#
# The expected account id comes from a LOCAL source of truth (deploy/targets.json),
# never from the account being targeted: reading the expected id from SSM in that
# same account would be circular - a wrong account would certify itself.
# deploy/targets.json is GITIGNORED: this repository is public, so the real account
# ids are kept out of the tree. The committed template is deploy/targets.example.json
# (placeholders only); the operator copies it to deploy/targets.json and fills the
# real ids locally. Keeping the file local rather than committed does not weaken the
# guard - the operator trusts their own machine, and the expected id is still
# resolved independently of the account being targeted.
#
# guard_resolve:
#   1. aws sts get-caller-identity -> live account id + caller ARN. A failure here
#      (no/expired credentials) aborts with a clear "not authenticated" message.
#   2. Loads targets.json[ENVIRONMENT] for the expected account id and region; a
#      missing environment or region aborts.
#   3. Refuses (non-zero, no mutation) if the live account != the expected id,
#      printing expected vs actual.
#   4. On a match, resolves the ECS cluster (ingestion-common.sh) and sets the
#      GUARD_* globals the caller reads.
#
# guard_summary prints the read-only preflight summary (environment, account, ARN,
# region, cluster, and any source/fleet/count/producer context the orchestrator
# exports) for the confirmation gate.
#
# Direct usage:
#   scripts/aws-target-guard.sh --check    resolve read-only and print the summary
#
# Configuration: ENVIRONMENT (dev|prod, default prod via ingestion-common.sh),
# TARGETS_FILE (default <repo>/deploy/targets.json), CLUSTER (else resolved).
# DRY_RUN is irrelevant here: the guard only reads (sts, ssm, terraform output).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"

# The committed source of truth for expected account ids. Overridable for tests.
TARGETS_FILE="${TARGETS_FILE:-$SCRIPT_DIR/../deploy/targets.json}"

# Optional run context the orchestrator exports so the summary describes the run.
# Empty here means status/preflight with no specific producer in flight.
GUARD_SOURCE="${GUARD_SOURCE:-}"
GUARD_FLEET="${GUARD_FLEET:-}"
GUARD_COUNT="${GUARD_COUNT:-}"
GUARD_PRODUCER="${GUARD_PRODUCER:-}"

# Set by guard_resolve for the caller to read.
GUARD_ACCOUNT=""
GUARD_ARN=""
GUARD_REGION=""
GUARD_CLUSTER=""

# guard_resolve: read the live identity, compare it to the committed expected
# account for ENVIRONMENT, and refuse on mismatch. On success the GUARD_* globals
# hold the verified identity, region, and cluster. Read-only.
guard_resolve() {
  ig_require_cmd aws jq

  [[ -f "$TARGETS_FILE" ]] || ig_fatal "targets file not found: $TARGETS_FILE - copy deploy/targets.example.json to deploy/targets.json and fill in the real per-environment account ids (targets.json is gitignored on purpose; this repository is public)"

  local expected region
  expected="$(jq -r --arg e "$ENVIRONMENT" '.[$e].account_id // empty' "$TARGETS_FILE")" \
    || ig_fatal "cannot read $TARGETS_FILE"
  [[ -n "$expected" ]] || ig_fatal "no expected account id for environment '$ENVIRONMENT' in $TARGETS_FILE; add it before running /crawler or /consumer against $ENVIRONMENT"
  region="$(jq -r --arg e "$ENVIRONMENT" '.[$e].region // empty' "$TARGETS_FILE")"
  [[ -n "$region" ]] || ig_fatal "no region for environment '$ENVIRONMENT' in $TARGETS_FILE"

  # Live identity. A non-zero sts means no or expired credentials: abort before
  # any compare so the operator sees an auth problem, not a spurious mismatch.
  local identity account arn
  identity="$(aws sts get-caller-identity --query '[Account,Arn]' --output text 2>/dev/null)" \
    || ig_fatal "not authenticated: 'aws sts get-caller-identity' failed; your credentials are missing or expired"
  account="$(printf '%s' "$identity" | cut -f1)"
  arn="$(printf '%s' "$identity" | cut -f2)"
  [[ -n "$account" && "$account" != "None" ]] || ig_fatal "not authenticated: could not read the live AWS account id"

  if [[ "$account" != "$expected" ]]; then
    cat >&2 <<EOF
error: wrong AWS account for environment '$ENVIRONMENT'; refusing.
  expected (deploy/targets.json): $expected
  actual   (sts get-caller-identity): $account
  caller ARN: $arn
Switch your AWS profile/credentials to the '$ENVIRONMENT' account, or fix the expected id in $TARGETS_FILE.
EOF
    exit 1
  fi

  GUARD_ACCOUNT="$account"
  GUARD_ARN="$arn"
  GUARD_REGION="$region"
  GUARD_CLUSTER="$(ig_resolve_cluster)"
}

# guard_summary: print the read-only preflight summary. Call after guard_resolve.
guard_summary() {
  {
    echo "ingest preflight:"
    echo "  environment: $ENVIRONMENT"
    echo "  account:     $GUARD_ACCOUNT  (matches expected)"
    echo "  caller ARN:  $GUARD_ARN"
    echo "  region:      $GUARD_REGION"
    echo "  cluster:     $GUARD_CLUSTER"
    [[ -n "$GUARD_SOURCE" ]]   && echo "  source:      $GUARD_SOURCE"
    [[ -n "$GUARD_FLEET" ]]    && echo "  fleet:       $GUARD_FLEET"
    [[ -n "$GUARD_COUNT" ]]    && echo "  count:       $GUARD_COUNT"
    [[ -n "$GUARD_PRODUCER" ]] && echo "  producer:    $GUARD_PRODUCER"
    return 0
  } >&2
}

# Direct execution: --check resolves read-only and prints the summary. Sourcing
# (the orchestrator) skips this so it can call guard_resolve/guard_summary itself.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  case "${1:-}" in
    --check)
      guard_resolve
      guard_summary
      ;;
    *)
      echo "usage: aws-target-guard.sh --check" >&2
      exit 2
      ;;
  esac
fi
