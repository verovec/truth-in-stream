#!/usr/bin/env bash
#
# Edit and roll an application secret in AWS Secrets Manager from a local
# editor. Secret values are edited through a temp file and pushed to AWS via
# `file://`, so a value never passes through a shell argument, a log, or an
# AI model. The previous version is retained under a timestamped staging label.
#
# Usage: ./scripts/secrets.sh <dev|prod>
#
# The environment maps to a dedicated AWS SSO profile; an expired session is
# refreshed automatically. Pushing to prod requires typing the environment name.
#
# Overridable via env (mainly for tests):
#   SECRETS_PROJECT       secret name prefix project segment (default truth-in-stream)
#   SECRETS_REGION        AWS region (default eu-west-3)
#   SECRETS_DEV_PROFILE   dev SSO profile (default verovec-dev)
#   SECRETS_PROD_PROFILE  prod SSO profile (default verovec-prod)
#   EDITOR                editor command (default: code --wait if present, else vi)

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

PREFIX="${PROJECT}/${ENV}/"
# Hold the base invocation in an array so the profile/region flags survive
# quoting cleanly at every call site ("${AWS[@]}").
AWS=(aws --profile "$PROFILE" --region "$REGION")

note() { echo "$@" >&2; }

# Open $1 in the operator's editor. Prompts/reads use the script's stdin, so the
# editor grabs the controlling terminal directly when one is attached; without a
# tty (tests, CI) it is invoked plainly so a stub editor can drive it.
open_editor() {
  local file="$1" ed="${EDITOR:-}"
  if [[ -z "$ed" ]]; then
    if command -v code >/dev/null 2>&1; then ed="code --wait"; else ed="vi"; fi
  fi
  if [[ -t 0 && -e /dev/tty ]]; then
    # shellcheck disable=SC2086
    $ed "$file" </dev/tty >/dev/tty
  else
    # shellcheck disable=SC2086
    $ed "$file"
  fi
}

# Refresh the SSO session if the current one is missing or expired.
if ! "${AWS[@]}" sts get-caller-identity >/dev/null 2>&1; then
  note "Logging in to AWS SSO ($PROFILE)..."
  "${AWS[@]}" sso login >&2
fi

LIST_JSON="$("${AWS[@]}" secretsmanager list-secrets \
  --filters Key=name,Values="$PREFIX" \
  --output json)"

mapfile -t NAMES < <(printf '%s' "$LIST_JSON" | python3 -c '
import sys, json
data = json.load(sys.stdin).get("SecretList", [])
for name in sorted(s["Name"] for s in data):
    print(name)
')

if [[ ${#NAMES[@]} -eq 0 ]]; then
  note "No secrets found under ${PREFIX}."
  note "Terraform creates the secret containers; apply the ${ENV} root first."
  exit 1
fi

note ""
note "Secrets (${PREFIX})"
note ""
for i in "${!NAMES[@]}"; do
  printf '  %2d. %s\n' "$((i + 1))" "${NAMES[$i]#"$PREFIX"}" >&2
done
note ""

read -rp "Pick a number (1-${#NAMES[@]}): " CHOICE
# Validate before any arithmetic: under `set -u`, $(( non-numeric - 1 )) aborts
# with "unbound variable" rather than reaching this guard.
if [[ ! "$CHOICE" =~ ^[0-9]+$ ]]; then
  note "Invalid selection."
  exit 1
fi
IDX=$((CHOICE - 1))
if [[ $IDX -lt 0 || $IDX -ge ${#NAMES[@]} ]]; then
  note "Invalid selection."
  exit 1
fi
FULL_PATH="${NAMES[$IDX]}"

RESPONSE="$("${AWS[@]}" secretsmanager get-secret-value \
  --secret-id "$FULL_PATH" --output json)"
OUTGOING_VERSION="$(printf '%s' "$RESPONSE" | python3 -c 'import sys,json;print(json.load(sys.stdin)["VersionId"])')"
BASELINE="$(printf '%s' "$RESPONSE" | python3 -c 'import sys,json;sys.stdout.write(json.load(sys.stdin)["SecretString"])')"

note ""
note "$FULL_PATH"
note "Current version: $OUTGOING_VERSION"

TMPFILE="$(mktemp)" || exit 1
trap 'rm -f "$TMPFILE"' EXIT
chmod 600 "$TMPFILE"

printf '%s' "$BASELINE" >"$TMPFILE"
open_editor "$TMPFILE"

# Command substitution strips trailing newlines an editor may add, so a secret
# is compared and pushed without spurious whitespace.
NEW_VALUE="$(cat "$TMPFILE")"
if [[ "$NEW_VALUE" == "$BASELINE" ]]; then
  echo "No changes."
  exit 0
fi

note ""
note "Diff (- current, + new):"
diff <(printf '%s\n' "$BASELINE") <(printf '%s\n' "$NEW_VALUE") >&2 || true
note ""

read -rp "Apply this change to ${ENV}? [y/N] " CONFIRM
case "$CONFIRM" in
  y | Y | yes | YES) ;;
  *)
    note "Aborted."
    exit 0
    ;;
esac

if [[ "$ENV" == "prod" ]]; then
  read -rp "This is PRODUCTION. Type '${ENV}' to confirm: " PROD_CONFIRM
  if [[ "$PROD_CONFIRM" != "$ENV" ]]; then
    note "Aborted."
    exit 0
  fi
fi

# Re-normalise the file so the pushed value matches what was diffed, then push
# via file:// so the value is never an argv the process table can expose.
printf '%s' "$NEW_VALUE" >"$TMPFILE"
PUT_RESPONSE="$("${AWS[@]}" secretsmanager put-secret-value \
  --secret-id "$FULL_PATH" \
  --secret-string "file://$TMPFILE" \
  --output json)"
NEW_VERSION="$(printf '%s' "$PUT_RESPONSE" | python3 -c 'import sys,json;print(json.load(sys.stdin)["VersionId"])')"

# Label the outgoing version with a timestamp so it is retained beyond the
# single-slot AWSPREVIOUS and stays auditable/recoverable.
STAGE="v-$(date -u +%Y%m%d-%H%M%S)"
if ! "${AWS[@]}" secretsmanager update-secret-version-stage \
  --secret-id "$FULL_PATH" \
  --version-stage "$STAGE" \
  --move-to-version-id "$OUTGOING_VERSION" >/dev/null 2>&1; then
  note "warning: pushed AWSCURRENT but failed to label the outgoing version $OUTGOING_VERSION"
fi

echo ""
echo "Secret:   $FULL_PATH"
echo "Current:  $NEW_VERSION (AWSCURRENT)"
echo "Previous: $OUTGOING_VERSION (retained as $STAGE)"
