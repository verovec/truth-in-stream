#!/usr/bin/env bash
set -euo pipefail

# Open an SSM port-forward tunnel through the hardened bastion to the private
# Amazon MQ broker, then run the embedding worker locally against the tunnel so
# it drains the cloud queue into the local database. No SSH, no public IP, no
# cloud database: the cloud side holds the broker, the local side holds the data.
#
# Usage: ssm-port-forward.sh [env] [-p|--port <local_port>]
#
#   env          dev (default) or prod; selects the SSO profile and resources.
#   -p, --port   local port the broker is forwarded to (default 5671, AMQPS).
#
# The bastion is resolved by its Name tag; the broker host/port come from the
# RABBITMQ_URL secret in Secrets Manager. Override the SSO profile with
# AWS_PROFILE and the region with AWS_REGION.
#
# Example (dev), in a second terminal run the local worker against the tunnel:
#   ./scripts/ssm-port-forward.sh dev
#   RABBITMQ_URL='amqps://app:<pw>@localhost:5671/?tls=...' \
#     docker compose run --rm embedworker   # drains the cloud queue locally
#
# Prerequisites: the AWS CLI v2, the Session Manager plugin, and an SSO login
# for the profile (`aws sso login --profile verovec-dev`). The SSM port-forward
# document has no TCP keepalive, so an idle tunnel may drop and need restarting.

PROJECT="truth-in-stream"
DEFAULT_PORT=5671
REGION="${AWS_REGION:-eu-west-3}"

usage() {
  echo "Usage: $0 [env] [-p|--port <local_port>]" >&2
  echo "  env: dev (default) or prod" >&2
}

# SSO profile per environment; AWS_PROFILE overrides it.
profile_for() {
  case "$1" in
    dev) echo "verovec-dev" ;;
    prod) echo "verovec-prod" ;;
    *) return 1 ;;
  esac
}

local_port="$DEFAULT_PORT"
env=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -p | --port)
      [[ $# -ge 2 ]] || { usage; exit 1; }
      local_port="$2"; shift 2 ;;
    -h | --help)
      usage; exit 0 ;;
    -*)
      echo "Unknown option: $1" >&2; usage; exit 1 ;;
    *)
      [[ -z "$env" ]] || { echo "Unexpected argument: $1" >&2; usage; exit 1; }
      env="$1"; shift ;;
  esac
done

env="${env:-dev}"
if ! default_profile="$(profile_for "$env")"; then
  echo "Unknown environment: $env (expected dev or prod)" >&2
  exit 1
fi
profile="${AWS_PROFILE:-$default_profile}"

if ! [[ "$local_port" =~ ^[0-9]+$ ]]; then
  echo "Invalid --port value: ${local_port} (expected a port number)" >&2
  exit 1
fi

bastion_name="${PROJECT}-${env}-bastion"
secret_id="${PROJECT}/${env}/rabbitmq/url"

echo "Resolving bastion by tag Name=${bastion_name} (profile=${profile}, region=${REGION})..." >&2
# [0][0] yields exactly one ID (or "None"): with --output text a multi-match
# query would return whitespace-joined IDs that break --target. The `if !` keeps
# an aws failure (expired SSO, throttling) from aborting under set -e before the
# friendly message below.
if ! instance_id="$(
  aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=${bastion_name}" "Name=instance-state-name,Values=running" \
    --query 'Reservations[0].Instances[0].InstanceId' \
    --output text \
    --region "$REGION" \
    --profile "$profile"
)"; then
  echo "Could not query EC2 for the bastion (profile=${profile})." >&2
  echo "Is the SSO session valid? Try: aws sso login --profile ${profile}" >&2
  exit 1
fi
if [[ -z "$instance_id" || "$instance_id" == "None" ]]; then
  echo "No running bastion found with tag Name=${bastion_name} (profile=${profile})." >&2
  echo "Apply the dev stack with enable_bastion=true, or check the SSO profile." >&2
  exit 1
fi

echo "Fetching broker URL from secret ${secret_id}..." >&2
if ! broker_url="$(
  aws secretsmanager get-secret-value \
    --secret-id "$secret_id" \
    --query SecretString \
    --output text \
    --region "$REGION" \
    --profile "$profile" 2>/dev/null
)"; then
  echo "No broker URL secret at ${secret_id} (profile=${profile})." >&2
  echo "The broker (modules/rabbitmq) publishes this secret; confirm it is deployed." >&2
  exit 1
fi

# Parse host and port out of amqps://user:pass@host[:port]/... The generated
# broker password is URL-safe alphanumeric, so the userinfo holds no extra '@'
# or ':' to confuse this. Falls back to the AMQPS port when the URL omits it.
no_scheme="${broker_url#*://}"
hostport="${no_scheme#*@}"
hostport="${hostport%%/*}"
remote_host="${hostport%%:*}"
if [[ "$hostport" == *:* ]]; then
  remote_port="${hostport##*:}"
else
  remote_port="$DEFAULT_PORT"
fi

if [[ -z "$remote_host" ]]; then
  echo "Could not parse a broker host from secret ${secret_id}." >&2
  exit 1
fi

cat >&2 <<EOF

Tunnel ready (${env}):
  Local endpoint:  localhost:${local_port}
  Broker:          ${remote_host}:${remote_port} (private, via bastion ${instance_id})

Run the embedding worker locally against the tunnel (second terminal); it drains
the cloud queue and writes embeddings into the local database. Keep this session
open while the worker runs.
EOF

exec aws ssm start-session \
  --target "$instance_id" \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters "host=${remote_host},portNumber=${remote_port},localPortNumber=${local_port}" \
  --region "$REGION" \
  --profile "$profile"
