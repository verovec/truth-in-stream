#!/usr/bin/env sh
#
# Ensure a throwaway integration-test database exists so `go test` with
# TEST_DATABASE_URL never runs the schema-dropping store/seed integration tests
# against the seeded dev database (`truthinstream`). This mirrors CI's dedicated
# `test` database (.github/workflows/_test.yml) on the local stack.
#
# Idempotent: creates the database only when absent, so it is safe to run before
# every integration run. The empty database needs no migration step - the
# integration tests reset the schema themselves (internal/store/postgres
# resetSchema drops the known tables and reapplies every up migration, and
# migration 0001 starts with `CREATE EXTENSION IF NOT EXISTS vector`), so a bare
# CREATE DATABASE is enough.
#
# Env:
#   TEST_DB_NAME  name of the throwaway database (default: truthinstream_test)
#   COMPOSE       compose command used to reach Postgres (default: docker compose)
set -eu

DB_NAME="${TEST_DB_NAME:-truthinstream_test}"
COMPOSE="${COMPOSE:-docker compose}"

# The name is interpolated into SQL and a createdb argument, so constrain it to a
# safe identifier shape (letters, digits, underscore) rather than trust the env.
case "$DB_NAME" in
	"" | *[!A-Za-z0-9_]*)
		echo "test-db: invalid TEST_DB_NAME '$DB_NAME' (allowed: letters, digits, underscore)" >&2
		exit 1
		;;
esac

# The compose command is a multi-word prefix on purpose; word-splitting it is
# the intended behaviour here.
# shellcheck disable=SC2086

# Bring Postgres up if it is not already running (compose start is idempotent).
if [ -z "$($COMPOSE ps -q postgres 2>/dev/null)" ]; then
	echo "test-db: starting postgres"
	$COMPOSE up -d postgres
fi

# Wait for the server to accept connections before touching it.
attempts=0
until $COMPOSE exec -T postgres pg_isready -U postgres >/dev/null 2>&1; do
	attempts=$((attempts + 1))
	if [ "$attempts" -ge 60 ]; then
		echo "test-db: postgres did not become ready in time" >&2
		exit 1
	fi
	sleep 1
done

# Create the database only when it is absent (idempotent).
exists="$($COMPOSE exec -T postgres psql -U postgres -tAc \
	"SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'" 2>/dev/null || true)"
if [ -n "$exists" ]; then
	echo "test-db: throwaway database '$DB_NAME' already present"
else
	$COMPOSE exec -T postgres createdb -U postgres "$DB_NAME"
	echo "test-db: created throwaway database '$DB_NAME'"
fi
