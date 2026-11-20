#!/usr/bin/env bash
set -euo pipefail

# Materialize the docker-compose.ingest.yml env file ON an ingestion host from
# Secrets Manager, using the host's instance profile. scripts/ingest-host.sh runs
# this over SSM as the first step of every /crawler and /consumer run, before it
# invokes `docker compose -f docker-compose.ingest.yml`; Compose then interpolates
# the written values into each service's environment.
#
# The instance profile scopes GetSecretValue to exactly this host role's secret
# ARNs (the crawler host: broker URL, RDS DSN, the producer-side keys; the
# consumer host: broker URL, RDS DSN, the embedding key; the tvcapture host: the
# tv-capture client secret and the Slack webhook only - it uses neither the broker
# nor RDS), so the secret set is selected by role here to match. A secret this
# role's profile cannot read is a misconfiguration and fails loudly.
#
# Secret VALUES are NEVER printed: only the variable name and its secret id are
# logged, and set -x is never enabled. The env file is written 0600 via a private
# temp file and an atomic rename, so a half-written file is never read and the
# values never land in a world-readable path.
#
# Usage:
#   scripts/ingest-fetch-env.sh <crawler|consumer|tvcapture> [env]
#
# Configuration (environment): PROJECT (default truth-in-stream), AWS_REGION
# (default eu-west-3), INGEST_ENV_FILE (default ./.env). DRY_RUN=1 lists the
# name<-secret mappings it would fetch without calling AWS or writing the file.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="${PROJECT:-truth-in-stream}"
REGION="${AWS_REGION:-eu-west-3}"
DRY_RUN="${DRY_RUN:-}"
ENV_FILE="${INGEST_ENV_FILE:-.env}"
# The connector registry manifest is the source of truth for a source's declared
# secrets, so a source that declares a Secrets entry actually gets its key fetched
# on the crawler host - no second hand-edited list here.
INGEST_SOURCES_MANIFEST="${INGEST_SOURCES_MANIFEST:-$SCRIPT_DIR/../stack/backend/internal/connector/sources.json}"

fatal() {
  echo "ingest-fetch-env: error: $1" >&2
  exit 1
}

command -v aws >/dev/null 2>&1 || fatal "aws is required but not on PATH"
command -v jq >/dev/null 2>&1 || fatal "jq is required but not on PATH"

role="${1:-}"
env="${2:-${INGEST_ENV:-dev}}"
[[ -n "$role" ]] || fatal "usage: ingest-fetch-env.sh <crawler|consumer|tvcapture> [env]"

# manifest_producer_secrets: echo the "<secret-suffix> <ENV_VAR>" mappings for every
# per-source producer secret declared in the connector registry manifest, deduped by
# env var. This is what makes a Descriptor's Secrets entry real: declaring one wires
# the crawler-host fetch, no edit here. Declared secrets are producer (crawler-host)
# side; a worker-side secret is a role-baseline entry below.
manifest_producer_secrets() {
  [[ -r "$INGEST_SOURCES_MANIFEST" ]] || fatal "cannot read source manifest ${INGEST_SOURCES_MANIFEST}; set INGEST_SOURCES_MANIFEST"
  jq -r '[.sources[].secrets[]?] | unique_by(.env_var)[] | "\(.secret_suffix) \(.env_var)"' "$INGEST_SOURCES_MANIFEST"
}

# secret_pairs ROLE: echo the "<secret-suffix> <ENV_VAR>" mappings this host role
# reads. The crawler and consumer hosts share the broker URL and the RDS DSN; the
# crawler adds every source's declared producer secret from the manifest, the
# consumer adds the embedding key its workers call. The tvcapture host uses neither
# the broker nor RDS - only its own client secret and the Slack webhook - matching
# the exact secret ARNs each host's instance profile is granted
# (stack/terraform/dev/main.tf).
secret_pairs() {
  case "$1" in
    crawler)
      echo "rabbitmq/url RABBITMQ_URL"
      echo "rds/dsn DATABASE_URL"
      manifest_producer_secrets
      ;;
    consumer)
      echo "rabbitmq/url RABBITMQ_URL"
      echo "rds/dsn DATABASE_URL"
      echo "app/embedding-api-key EMBEDDING_API_KEY"
      ;;
    tvcapture)
      echo "app/tv-capture-client-secret TV_CAPTURE_CLIENT_SECRET"
      echo "app/slack-webhook-url SLACK_WEBHOOK_URL"
      ;;
    *)
      fatal "unknown role '$1'; one of: crawler consumer tvcapture"
      ;;
  esac
}

# read_secret SECRET_ID: echo the plaintext SecretString for the id, or fail. The
# value is captured into a shell variable by the caller and never logged.
read_secret() {
  local val
  val="$(aws secretsmanager get-secret-value --secret-id "$1" \
    --query SecretString --output text --region "$REGION" 2>/dev/null)" \
    || return 1
  [[ -n "$val" && "$val" != "None" ]] || return 1
  printf '%s' "$val"
}

pairs="$(secret_pairs "$role")"

if [[ -n "$DRY_RUN" ]]; then
  echo "DRY-RUN ingest-fetch-env would write ${ENV_FILE} for role ${role} (${PROJECT}/${env}):" >&2
  while read -r suffix var; do
    [[ -n "$suffix" ]] || continue
    echo "  ${var} <- ${PROJECT}/${env}/${suffix}" >&2
  done <<<"$pairs"
  exit 0
fi

# Write to a private temp file first (umask 077), then rename over the target so a
# reader never sees a partial file and the secrets never touch a world-readable
# path. A trap removes the temp file on any early exit.
umask 077
tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

{
  echo "# Machine-generated by scripts/ingest-fetch-env.sh - do not commit or edit."
  echo "# Secret values from Secrets Manager (${PROJECT}/${env}); role ${role}."
} >"$tmp"

while read -r suffix var; do
  [[ -n "$suffix" ]] || continue
  secret_id="${PROJECT}/${env}/${suffix}"
  if ! value="$(read_secret "$secret_id")"; then
    fatal "cannot read secret ${secret_id} for ${var} (is this host's instance profile scoped for it, and does the secret hold a value?)"
  fi
  # printf, not echo, so a value that looks like an echo flag is written verbatim;
  # the value goes only into the file, never to a log stream.
  printf '%s=%s\n' "$var" "$value" >>"$tmp"
  echo "wrote ${var} from ${secret_id}" >&2
  unset value
done <<<"$pairs"

mv "$tmp" "$ENV_FILE"
trap - EXIT
chmod 600 "$ENV_FILE"
echo "ingest-fetch-env: wrote ${ENV_FILE} (0600) for role ${role}" >&2
