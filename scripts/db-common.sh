# Shared configuration and helpers for db-backup.sh and db-restore.sh.
# Sourced by those scripts, never executed directly.

DB_NAME="${DB_NAME:-truthinstream}"
DB_USER="${DB_USER:-postgres}"
BACKUP_DIR="${BACKUP_DIR:-backups}"
DB_BACKUP_PREFIX="${DB_BACKUP_PREFIX:-db-backups}"

# require_postgres_running exits non-zero unless the compose `postgres` service
# is up, since pg_dump/pg_restore run inside that container. `docker compose
# ps -q` lists only running services, so an empty result means it is not up.
require_postgres_running() {
	if [ -z "$(docker compose ps -q postgres 2>/dev/null)" ]; then
		echo "error: the postgres service is not running; start it with 'make up'" >&2
		exit 1
	fi
}
