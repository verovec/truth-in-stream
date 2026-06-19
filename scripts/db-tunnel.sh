#!/usr/bin/env bash
set -euo pipefail

# Open an SSM port-forward tunnel through the hardened bastion to the private
# RDS PostgreSQL endpoint, so an operator can load the already-embedded local
# database into RDS over the tunnel (scripts/db-push.sh). No SSH, no public IP,
# no public RDS: the bastion is reached only over Session Manager and forwards a
# local port to the private database.
#
# Usage: db-tunnel.sh [env] [-p|--port <local_port>]
#
#   env          prod (default) or dev; selects the SSO profile and resources.
#   -p, --port   local port RDS is forwarded to (default 5432, PostgreSQL).
#
# The bastion is resolved by its Name tag; the RDS host/port come from the
# DATABASE_URL DSN secret in Secrets Manager (the rds module publishes it).
# Override the SSO profile with AWS_PROFILE and the region with AWS_REGION.
#
# Example (prod): in a second terminal run the one-time load against the tunnel:
#   ./scripts/db-tunnel.sh prod
#   ./scripts/db-push.sh prod            # loads the local dump into RDS
#
# Prerequisites: the AWS CLI v2, the Session Manager plugin, and an SSO login
# for the profile (`aws sso login --profile verovec-prod`). The SSM port-forward
# document has no TCP keepalive, so an idle tunnel may drop and need restarting.

PROJECT="truth-in-stream"
DEFAULT_PORT=5432
REGION="${AWS_REGION:-eu-west-3}"

usage() {
  echo "Usage: $0 [env] [-p|--port <local_port>]" >&2
  echo "  env: prod (default) or dev" >&2
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

env="${env:-prod}"
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
secret_id="${PROJECT}/${env}/rds/dsn"

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
  echo "Apply the ${env} stack with enable_bastion=true, or check the SSO profile." >&2
  exit 1
fi

echo "Fetching the RDS endpoint from secret ${secret_id}..." >&2
if ! dsn="$(
  aws secretsmanager get-secret-value \
    --secret-id "$secret_id" \
    --query SecretString \
    --output text \
    --region "$REGION" \
    --profile "$profile" 2>/dev/null
)"; then
  echo "No DATABASE_URL secret at ${secret_id} (profile=${profile})." >&2
  echo "The rds module publishes this secret; confirm RDS is deployed (enable_rds)." >&2
  exit 1
fi

# Parse host and port out of postgres://user:pass@host[:port]/db?... The
# generated master password is URL-safe alphanumeric (rds module: random_password
# special=false), so the userinfo holds no extra ':' to confuse the split. Use
# ##*@ (strip to the LAST '@') so the host is still parsed correctly even if a
# rotated password ever contained an '@'. Falls back to the PostgreSQL port when
# the URL omits it.
no_scheme="${dsn#*://}"
hostport="${no_scheme##*@}"
hostport="${hostport%%/*}"
remote_host="${hostport%%:*}"
if [[ "$hostport" == *:* ]]; then
  remote_port="${hostport##*:}"
else
  remote_port="$DEFAULT_PORT"
fi

if [[ -z "$remote_host" ]]; then
  echo "Could not parse an RDS host from secret ${secret_id}." >&2
  exit 1
fi

cat >&2 <<EOF

Tunnel ready (${env}):
  Local endpoint:  localhost:${local_port}
  RDS:             ${remote_host}:${remote_port} (private, via bastion ${instance_id})

Run the one-time load against the tunnel (second terminal):
  ./scripts/db-push.sh ${env}        # loads the local dump into RDS over localhost:${local_port}
Keep this session open while the load runs.
EOF

exec aws ssm start-session \
  --target "$instance_id" \
  --document-name AWS-StartPortForwardingSessionToRemoteHost \
  --parameters "host=${remote_host},portNumber=${remote_port},localPortNumber=${local_port}" \
  --region "$REGION" \
  --profile "$profile"
