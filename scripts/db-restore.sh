#!/usr/bin/env bash
# Restore the local Postgres + pgvector database from a dump produced by
# db-backup.sh. With an argument, restores that local file; otherwise, when
# DB_BACKUP_BUCKET is set, downloads the most recent dump from S3. The restore
# replaces schema and data, so the result is independent of whether migrations
# or seed ran first.
#
# pg_restore runs inside the running `postgres` container. The flags
# (--clean --if-exists --no-owner --no-privileges) match dbbackup.RestoreArgs;
# halfvec embeddings round-trip exactly, as guaranteed by the test in
# stack/backend/internal/dbbackup.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
. "$ROOT/scripts/db-common.sh"

require_postgres_running

archive="${1:-}"
if [ -z "$archive" ]; then
	if [ -z "${DB_BACKUP_BUCKET:-}" ]; then
		echo "error: no dump given and DB_BACKUP_BUCKET unset" >&2
		echo "usage: $0 [path/to/dump]   (or set DB_BACKUP_BUCKET to pull the latest from S3)" >&2
		exit 1
	fi
	prefix="s3://${DB_BACKUP_BUCKET}/${DB_BACKUP_PREFIX}/"
	# Timestamped keys sort chronologically, so the last line is the newest dump.
	# `|| true` keeps an empty listing (grep exits non-zero) from tripping the
	# `set -e`/`pipefail` pair before the friendly empty-result check below.
	latest="$(aws s3 ls "$prefix" | awk '{print $4}' | grep -E '\.dump$' | sort | tail -1 || true)"
	if [ -z "$latest" ]; then
		echo "error: no dumps found under $prefix" >&2
		exit 1
	fi
	mkdir -p "$BACKUP_DIR"
	archive="$BACKUP_DIR/$latest"
	echo "downloading ${prefix}${latest}"
	aws s3 cp "${prefix}${latest}" "$archive"
fi

if [ ! -f "$archive" ]; then
	echo "error: dump not found: $archive" >&2
	exit 1
fi

echo "restoring '$DB_NAME' from $archive"
docker compose exec -T postgres \
	pg_restore --clean --if-exists --no-owner --no-privileges \
	--username "$DB_USER" --dbname "$DB_NAME" <"$archive"
echo "restore complete from $archive"
