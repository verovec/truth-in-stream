#!/usr/bin/env bash
set -euo pipefail

# INSEE re-run idempotency checkpoint against the real (prod) RDS. Proves that a
# second statsingest ingest adds no duplicate INSEE passages, validating the
# VER-123/124 provenance-key scheme (the stable (series, period) key behind the
# (page_id, chunk_index) upsert in wiki_chunks) against real RDS rather than only
# the integration test's in-memory store.
#
# Method:
#   1. count INSEE passages in wiki_chunks now            (baseline)
#   2. re-run the statsingest ingest                       (run-ingest-task.sh)
#   3. count INSEE passages again                          (after)
#   4. assert after == baseline                            (no growth = idempotent)
#
# "INSEE passages" are every row whose corpus is one of the INSEE corpora
# (insee, insee-chomage, insee-emploi, insee-prix, insee-pib - see
# internal/domain/datapoint.go), matched by the `insee` prefix so a new
# economic-theme corpus is covered automatically.
#
# Connection: the checkpoint runs psql against the RDS reachable on
# localhost:<port> over an open `make db-tunnel` tunnel (the same private-RDS
# access path db-push.sh uses). Provide the connection string in PGURL, or it is
# built as postgres://<PGUSER>@localhost:<PGPORT>/<PGDATABASE> with PGPASSWORD
# from the environment - no credential is ever placed on an argv.
#
# Usage:
#   make db-tunnel                       # terminal 1: open the tunnel
#   scripts/insee-idempotency-check.sh   # terminal 2: run the checkpoint
#
# Env knobs:
#   PGURL       full libpq connection string (overrides the parts below)
#   PGHOST      default localhost   PGPORT default 5432
#   PGUSER      default postgres    PGDATABASE default truthinstream
#   PGPASSWORD  password (env only; never an argv)
#   SKIP_INGEST=1   only count baseline and after WITHOUT re-running the ingest
#                   (e.g. to re-check after a manual ingest); both counts run
#                   back to back so they must match trivially - used by the test.
#   DRY_RUN=1   passed through to run-ingest-task.sh (prints the run-task, skips it)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGDATABASE="${PGDATABASE:-truthinstream}"

# pg_conn: echo the connection string psql connects with. PGURL wins; otherwise
# assemble the localhost-tunnel form. The password rides in PGPASSWORD in the
# environment, never in this string, so it never reaches an argv or a log.
pg_conn() {
  if [[ -n "${PGURL:-}" ]]; then
    printf '%s' "$PGURL"
    return 0
  fi
  printf 'postgres://%s@%s:%s/%s' "$PGUSER" "$PGHOST" "$PGPORT" "$PGDATABASE"
}

# count_insee_passages: echo the number of INSEE passages currently in the live
# corpus. A single scalar, whitespace-trimmed.
count_insee_passages() {
  local conn="$1" out
  out="$(psql "$conn" -tApc \
    "SELECT count(*) FROM wiki_chunks WHERE corpus LIKE 'insee%';")" \
    || ig_fatal "count query failed; is the db-tunnel open and reachable on ${PGHOST}:${PGPORT}?"
  out="${out//[[:space:]]/}"
  # count(*) is always a non-negative integer; anything else (empty result,
  # error text that slipped past psql's exit code) must not be silently compared
  # as if it were a count - that would let two empty values read as "no growth".
  case "$out" in
    ''|*[!0-9]*) ig_fatal "count query returned a non-numeric result '${out}'; not a usable passage count" ;;
  esac
  printf '%s' "$out"
}

main() {
  ig_require_cmd psql
  local conn
  conn="$(pg_conn)"

  echo "INSEE idempotency checkpoint against $(printf '%s' "$conn" | sed 's#://[^@/]*@#://***@#')" >&2

  local baseline
  baseline="$(count_insee_passages "$conn")"
  echo "baseline INSEE passages: ${baseline}" >&2

  if [[ -z "${SKIP_INGEST:-}" ]]; then
    echo "re-running statsingest (this re-ingests INSEE; an idempotent ingest must not duplicate)" >&2
    "$SCRIPT_DIR/run-ingest-task.sh" statsingest
  else
    echo "SKIP_INGEST set: not re-running the ingest; counting again for a back-to-back equality check" >&2
  fi

  local after
  after="$(count_insee_passages "$conn")"
  echo "INSEE passages after re-run: ${after}" >&2

  if [[ "$after" == "$baseline" ]]; then
    echo "PASS: INSEE re-run added no passages (${baseline} -> ${after}); the provenance key is idempotent" >&2
    return 0
  fi
  echo "FAIL: INSEE re-run changed the passage count (${baseline} -> ${after}); a re-ingest duplicated passages" >&2
  return 1
}

main "$@"
