#!/usr/bin/env bash
# Load the already-embedded LOCAL Postgres + pgvector database into the private
# RDS instance over an open SSM tunnel (scripts/db-tunnel.sh). This is a one-time
# bulk load, not a re-embed: the local dump already carries the claims vectors
# and the wiki_chunks halfvec embeddings.
#
# Vector fidelity: the transfer is a pg_dump custom-format dump piped through
# pg_restore, both in TEXT format. pg_dump emits `COPY ... TO` in text (halfvec
# serialized via halfvec_out as `[v1,...,vN]`) and pg_restore replays it via
# `COPY ... FROM` in text (halfvec_in). The pgx BINARY CopyFrom path that
# corrupts halfvec (phantom rows) is never used here, so embeddings round-trip
# exactly. This is the same text-COPY guarantee covered by the round-trip test
# in stack/backend/internal/dbbackup.
#
# Usage: db-push.sh [env] [-p|--port <local_port>] [-f|--file <dump>]
#
#   env          prod (default) or dev; selects the SSO profile and the target
#                DATABASE_URL secret (only used to obtain the RDS credentials).
#   -p, --port   local port the tunnel forwards RDS to (default 5432); the load
#                connects to localhost:<port>, NOT the RDS host directly.
#   -f, --file   restore this existing dump instead of taking a fresh one.
#
# Credentials are never embedded: the RDS user/password/dbname are read from the
# DATABASE_URL secret in Secrets Manager at runtime and handed to pg_restore via
# PGPASSWORD in the container's environment only (never an argv, never logged).
#
# Order of operations for a full load (see stack/terraform/README.md runbook):
#   1. terraform apply with enable_bastion=true        (brings the bastion up)
#   2. run the migration task against RDS               (creates the schema)
#   3. ./scripts/db-tunnel.sh prod                      (open the tunnel)
#   4. ./scripts/db-push.sh prod                        (this script; loads data)
#
# Prerequisites: the local `postgres` compose service running (the dump runs
# inside it, matching client/server versions), Docker (the restore runs in a
# throwaway pgvector client container on the host network so localhost:<port>
# reaches the tunnel), the AWS CLI v2 with a valid SSO session, and an open
# tunnel from db-tunnel.sh.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
. "$ROOT/scripts/db-common.sh"

REGION="${AWS_REGION:-eu-west-3}"
PG_IMAGE="${PG_IMAGE:-pgvector/pgvector:pg16}"
DEFAULT_PORT=5432

usage() {
  echo "Usage: $0 [env] [-p|--port <local_port>] [-f|--file <dump>]" >&2
  echo "  env: prod (default) or dev" >&2
}

profile_for() {
  case "$1" in
    dev) echo "verovec-dev" ;;
    prod) echo "verovec-prod" ;;
    *) return 1 ;;
  esac
}

local_port="$DEFAULT_PORT"
env=""
dump_file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p | --port)
      [ $# -ge 2 ] || { usage; exit 1; }
      local_port="$2"; shift 2 ;;
    -f | --file)
      [ $# -ge 2 ] || { usage; exit 1; }
      dump_file="$2"; shift 2 ;;
    -h | --help)
      usage; exit 0 ;;
    -*)
      echo "Unknown option: $1" >&2; usage; exit 1 ;;
    *)
      [ -z "$env" ] || { echo "Unexpected argument: $1" >&2; usage; exit 1; }
      env="$1"; shift ;;
  esac
done

env="${env:-prod}"
if ! default_profile="$(profile_for "$env")"; then
  echo "Unknown environment: $env (expected dev or prod)" >&2
  exit 1
fi
profile="${AWS_PROFILE:-$default_profile}"

case "$local_port" in
  '' | *[!0-9]*)
    echo "Invalid --port value: ${local_port} (expected a port number)" >&2
    exit 1 ;;
esac

secret_id="${PROJECT:-truth-in-stream}/${env}/rds/dsn"

# Take a fresh dump from the running local container unless one was supplied. The
# custom format and the fidelity-critical flags match dbbackup.DumpArgs; running
# inside the container keeps the pg_dump client version in lock-step with the
# local server and removes any host Postgres dependency.
cleanup_dump=""
if [ -z "$dump_file" ]; then
  require_postgres_running
  mkdir -p "$BACKUP_DIR"
  timestamp="$(date -u +%Y%m%d-%H%M%S)"
  dump_file="$BACKUP_DIR/${DB_NAME}-load-${timestamp}.dump"
  tmp="$dump_file.partial"
  cleanup_dump="$tmp"
  trap 'rm -f "$cleanup_dump"' EXIT
  echo "dumping local '$DB_NAME' to $dump_file" >&2
  docker compose exec -T postgres \
    pg_dump --format=custom --no-owner --no-privileges \
    --username "$DB_USER" --dbname "$DB_NAME" >"$tmp"
  mv "$tmp" "$dump_file"
  cleanup_dump=""
  trap - EXIT
fi

if [ ! -f "$dump_file" ]; then
  echo "error: dump not found: $dump_file" >&2
  exit 1
fi

# Read the RDS credentials from Secrets Manager. The DSN is
# postgres://user:pass@host:port/dbname?...; we keep only user/pass/dbname and
# point the connection at the LOCAL tunnel (localhost:<port>), never at the
# private RDS host (which is unreachable except through the tunnel anyway).
echo "fetching RDS credentials from ${secret_id} (profile=${profile})" >&2
if ! dsn="$(
  aws secretsmanager get-secret-value \
    --secret-id "$secret_id" \
    --query SecretString \
    --output text \
    --region "$REGION" \
    --profile "$profile" 2>/dev/null
)"; then
  echo "No DATABASE_URL secret at ${secret_id} (profile=${profile})." >&2
  echo "Is the SSO session valid (aws sso login --profile ${profile}) and RDS deployed?" >&2
  exit 1
fi

# Split on the LAST '@' so userinfo (user:pass) and host part are separated
# correctly even if a rotated password ever contained an '@'. The generated
# master password is URL-safe alphanumeric (rds module: random_password
# special=false), so the user:pass split on the first ':' is unambiguous. The
# userinfo MUST contain a ':' (a password); a userinfo with no ':' would make
# db_pass equal db_user and silently send the wrong password to pg_restore, so
# reject it explicitly.
no_scheme="${dsn#*://}"
userinfo="${no_scheme%@*}"
after_at="${no_scheme##*@}"
case "$userinfo" in
  *:*) : ;;
  *)
    echo "error: ${secret_id} DSN has no password in its userinfo (expected user:pass@host)" >&2
    exit 1 ;;
esac
db_user="${userinfo%%:*}"
db_pass="${userinfo#*:}"
pathq="${after_at#*/}"
db_name="${pathq%%\?*}"

if [ -z "$db_user" ] || [ -z "$db_pass" ] || [ -z "$db_name" ]; then
  echo "error: could not parse user/password/dbname from ${secret_id}" >&2
  exit 1
fi

cat >&2 <<EOF

Loading $dump_file into RDS '${db_name}' as '${db_user}' via the tunnel at
localhost:${local_port}. Vector columns transfer as text COPY (halfvec-safe).
This replaces the target schema and data (pg_restore --clean --if-exists).
EOF

# Restore in a throwaway pgvector client container on the HOST network so
# localhost:<port> resolves to the tunnel. The password is exported into this
# shell's environment and passed to the container by NAME only (`-e PGPASSWORD`,
# no value), so the secret never appears in any argv — not docker's, not
# pg_restore's — nor in a log line. The restore flags match dbbackup.RestoreArgs;
# --clean --if-exists makes the load idempotent and independent of whether the
# target was pre-migrated. PGSSLMODE=require because the tunnel terminates at a
# TLS-only RDS endpoint.
export PGPASSWORD="$db_pass"
if ! docker run --rm -i --network host \
  -e PGPASSWORD \
  -e PGSSLMODE=require \
  "$PG_IMAGE" \
  pg_restore --clean --if-exists --no-owner --no-privileges \
  --host localhost --port "$local_port" \
  --username "$db_user" --dbname "$db_name" <"$dump_file"; then
  # The dump is deliberately kept on a failed restore so the load can be retried
  # without re-dumping: `./scripts/db-push.sh ${env} --file <dump>`.
  echo "error: pg_restore failed. The dump is kept at: $dump_file" >&2
  echo "Is the tunnel up (make db-tunnel) on localhost:${local_port}? Retry with --file ${dump_file}." >&2
  exit 1
fi

echo "load complete: $dump_file -> RDS '${db_name}' (${env})" >&2
echo "verify with row counts and a sample vector search (see the README runbook)." >&2
