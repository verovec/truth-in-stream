#!/usr/bin/env bash
# Snapshot the local Postgres + pgvector database to a timestamped custom-format
# dump under backups/, then upload it to S3 when DB_BACKUP_BUCKET is set. The
# point is to skip re-embedding the corpus after a `make reset`: the dump carries
# the claims vectors and the wiki_chunks halfvec embeddings intact.
#
# pg_dump runs inside the running `postgres` container, so the client version
# always matches the server and there is no host Postgres dependency. The
# fidelity-critical flags (--format=custom --no-owner --no-privileges) match
# dbbackup.DumpArgs; the round-trip guarantee is covered by the test in
# stack/backend/internal/dbbackup.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
. "$ROOT/scripts/db-common.sh"

require_postgres_running

mkdir -p "$BACKUP_DIR"
timestamp="$(date -u +%Y%m%d-%H%M%S)"
archive="$BACKUP_DIR/${DB_NAME}-${timestamp}.dump"

# Dump to a temp file and rename only on success, so a pg_dump that fails partway
# never leaves a truncated archive behind that a later restore could pick up.
tmp="$archive.partial"
trap 'rm -f "$tmp"' EXIT

echo "backing up '$DB_NAME' to $archive"
docker compose exec -T postgres \
	pg_dump --format=custom --no-owner --no-privileges \
	--username "$DB_USER" --dbname "$DB_NAME" >"$tmp"
mv "$tmp" "$archive"
echo "wrote $(du -h "$archive" | cut -f1) to $archive"

if [ -n "${DB_BACKUP_BUCKET:-}" ]; then
	dest="s3://${DB_BACKUP_BUCKET}/${DB_BACKUP_PREFIX}/$(basename "$archive")"
	echo "uploading to $dest"
	aws s3 cp "$archive" "$dest"
	echo "uploaded $dest"
else
	echo "DB_BACKUP_BUCKET unset; kept local copy only"
fi
