#!/usr/bin/env bash
#
# Push the application runtime secrets from the local .env into AWS Secrets
# Manager under the <project>/<env>/app/ prefix, idempotently. The terraform root
# creates the secret containers (no values); this fills them.
#
# Usage: ./scripts/push-secrets.sh <dev|prod>
#
# Only the keys in ALLOWLIST below are pushed. The DATABASE_URL and RABBITMQ_URL
# secrets are deliberately NOT in it: terraform generates those, so pushing them
# from .env would cause drift. A value never passes through a shell argument, a
# log, or stdout: it is written to a chmod-600 temp file and handed to the CLI as
# `--secret-string file://...`, then shredded. For each key the script checks
# existence (describe-secret) and either create-secret or put-secret-value, so a
# re-run never duplicates and never leaks.
#
# The allowlist here is the source of truth for the push and MUST match the
# `aws_secretsmanager_secret` resources declared in stack/terraform/prod/main.tf
# and wired into the task definitions' `secrets` there.
#
# Overridable via env (mainly for tests):
#   SECRETS_PROJECT        secret name prefix project segment (default truth-in-stream)
#   SECRETS_REGION         AWS region (default eu-west-3)
#   SECRETS_DEV_PROFILE    dev SSO profile (default verovec-dev)
#   SECRETS_PROD_PROFILE   prod SSO profile (default verovec-prod)
#   PUSH_SECRETS_ENV_FILE  path to the .env to read (default <repo-root>/.env)

set -euo pipefail

usage() {
  echo "Usage: $0 <dev|prod>" >&2
  exit 1
}

[[ $# -eq 1 ]] || usage

ENV="$1"
PROJECT="${SECRETS_PROJECT:-truth-in-stream}"
REGION="${SECRETS_REGION:-eu-west-3}"

case "$ENV" in
  dev)  PROFILE="${SECRETS_DEV_PROFILE:-verovec-dev}" ;;
  prod) PROFILE="${SECRETS_PROD_PROFILE:-verovec-prod}" ;;
  *)    usage ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${PUSH_SECRETS_ENV_FILE:-$SCRIPT_DIR/../.env}"

PREFIX="${PROJECT}/${ENV}/app/"
AWS=(aws --profile "$PROFILE" --region "$REGION")

note() { echo "$@" >&2; }

# Allowlist of .env keys pushed as app secrets. Each maps to a kebab-cased secret
# name under PREFIX. DATABASE_URL / RABBITMQ_URL are intentionally absent (they
# are terraform-owned). Keep in lockstep with prod/main.tf.
ALLOWLIST=(
  EMBEDDING_API_KEY
  TRANSCRIPTION_API_KEY
  AUTH_EMAIL
  AUTH_PASSWORD_HASH
  SESSION_SECRET
  DEEPSEEK_API_KEY
  GEMINI_API_KEY
  SLACK_WEBHOOK_URL
  TV_CAPTURE_CLIENT_SECRET
)

# Lowercase + underscores-to-dashes, e.g. EMBEDDING_API_KEY -> embedding-api-key.
secret_name_for() {
  printf '%s' "$1" | tr '[:upper:]_' '[:lower:]-'
}

if [[ ! -f "$ENV_FILE" ]]; then
  note "No .env at $ENV_FILE."
  note "Create it (make bootstrap) before pushing secrets."
  exit 1
fi

# Read one allowlisted key's value from the .env without exporting the whole file
# or echoing the value. Strips an optional surrounding pair of quotes. Prints the
# raw value to stdout (captured by the caller, never logged); empty if unset.
read_env_value() {
  local key="$1" line val
  # Last assignment wins, matching dotenv precedence. `|| true` keeps a no-match
  # grep (exit 1) from aborting under pipefail.
  line="$(grep -E "^${key}=" "$ENV_FILE" || true)"
  line="$(printf '%s' "$line" | tail -n1)"
  [[ -z "$line" ]] && return 0
  val="${line#"${key}"=}"
  # Drop a trailing CR so a CRLF .env does not push a value ending in \r.
  val="${val%$'\r'}"
  # Strip a single matching pair of surrounding quotes.
  if [[ "$val" == \"*\" && ${#val} -ge 2 ]]; then
    val="${val:1:${#val}-2}"
  elif [[ "$val" == \'*\' && ${#val} -ge 2 ]]; then
    val="${val:1:${#val}-2}"
  fi
  printf '%s' "$val"
}

# Refresh the SSO session if the current one is missing or expired.
if ! "${AWS[@]}" sts get-caller-identity >/dev/null 2>&1; then
  note "Logging in to AWS SSO ($PROFILE)..."
  "${AWS[@]}" sso login >&2
fi

note ""
note "Pushing allowlisted app secrets to ${PREFIX} (${ENV})."
note "Source: $ENV_FILE"
note "Keys: ${ALLOWLIST[*]}"
note ""

if [[ "$ENV" == "prod" ]]; then
  if ! read -rp "This is PRODUCTION. Type '${ENV}' to confirm the push: " PROD_CONFIRM; then
    note "Aborted (no confirmation on stdin)."
    exit 1
  fi
  if [[ "$PROD_CONFIRM" != "$ENV" ]]; then
    note "Aborted."
    exit 0
  fi
fi

# One temp file reused per key; chmod 600 and shredded on exit so a value never
# lingers. file:// keeps the value out of the process table.
TMPFILE="$(mktemp)" || exit 1
chmod 600 "$TMPFILE"
cleanup() {
  if command -v shred >/dev/null 2>&1; then
    shred -u "$TMPFILE" 2>/dev/null || rm -f "$TMPFILE"
  else
    rm -f "$TMPFILE"
  fi
}
trap cleanup EXIT

pushed=0
skipped=0
for key in "${ALLOWLIST[@]}"; do
  name="${PREFIX}$(secret_name_for "$key")"
  value="$(read_env_value "$key")"
  if [[ -z "$value" ]]; then
    note "skip ${name} (unset or empty in .env)"
    skipped=$((skipped + 1))
    continue
  fi

  # Write the value to the temp file only; never an argv, never a log.
  printf '%s' "$value" >"$TMPFILE"

  # Distinguish "secret absent" from a real describe failure. The AWS CLI exits
  # 254 for a service exception (ResourceNotFoundException here); only that means
  # "create it". Any other non-zero (auth, throttle, network) is a real error -
  # do NOT fall through to create-secret, which would mask the cause.
  describe_rc=0
  "${AWS[@]}" secretsmanager describe-secret --secret-id "$name" >/dev/null 2>&1 || describe_rc=$?
  if [[ "$describe_rc" -eq 0 ]]; then
    "${AWS[@]}" secretsmanager put-secret-value \
      --secret-id "$name" \
      --secret-string "file://$TMPFILE" >/dev/null
    note "put  ${name}"
  elif [[ "$describe_rc" -eq 254 ]]; then
    "${AWS[@]}" secretsmanager create-secret \
      --name "$name" \
      --secret-string "file://$TMPFILE" >/dev/null
    note "make ${name}"
  else
    note "error: describe-secret on ${name} failed (exit ${describe_rc}); not pushing. Check credentials/permissions."
    exit 1
  fi
  pushed=$((pushed + 1))
done

# Overwrite the temp file before the trap shreds it, belt and suspenders.
: >"$TMPFILE"

echo ""
echo "Pushed ${pushed} secret(s), skipped ${skipped}, under ${PREFIX}"
