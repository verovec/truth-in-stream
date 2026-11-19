#!/usr/bin/env bash
#
# Tests for the Makefile's ensure-worker guard, which the ingestion producer
# targets run before publishing. Stubs `docker` on a sandbox PATH so the guard is
# exercised without a Docker daemon: the stub logs every call and answers
# `compose ps -q <service>` from RUNNING, the space-separated list of services
# the test pretends are up.
# Run: ./scripts/ingest-workers.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

BIN="$TMPROOT/bin"; mkdir -p "$BIN"
cat >"$BIN/docker" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$DOCKER_LOG"
case " $* " in
  *" ps -q "*)
    service="${*: -1}"
    case " ${RUNNING:-} " in *" $service "*) echo "container-of-$service" ;; esac ;;
esac
exit 0
STUB
chmod +x "$BIN/docker"

# run_target <running services> <make target> [make args...] runs one producer
# target against the stub and leaves the docker call log in $LOG.
run_target() {
  local running="$1"; shift
  LOG="$TMPROOT/docker.$RANDOM.log"; : >"$LOG"
  PATH="$BIN:$PATH" DOCKER_LOG="$LOG" RUNNING="$running" \
    make -C "$ROOT" --no-print-directory "$@" >/dev/null 2>&1
  RC=$?
}

line_of() { grep -nF -- "$1" "$LOG" | head -1 | cut -d: -f1; }

# target | compose profile | worker service | producer service | extra make args
CASES="
stats-ingest|wiki|embedworker|stats-ingest|
wiki-populate|wiki|embedworker|wiki-populate|
factcheck-crawl|factcheck|factcheckworker|factcheckcrawl|
scrutins-crawl|scrutins|scrutinsworker|scrutinscrawl|
crawl|wiki|crawlworker|wikicrawl|CRAWL_SHARDS=1
"

while IFS='|' read -r target profile worker producer extra; do
  [ -n "$target" ] || continue
  start="--profile $profile up -d rabbitmq $worker"
  publish="run --rm --no-deps $producer"

  echo "TEST: $target starts a $worker when only the broker is running"
  # shellcheck disable=SC2086
  run_target "rabbitmq" "$target" $extra
  [ "$RC" -eq 0 ] && ok "exit 0" || fail "exit 0 (got $RC)"
  log="$(cat "$LOG")"
  assert_contains "$log" "--profile $profile ps -q $worker" "checks the worker, not the broker"
  assert_contains "$log" "$start" "starts the broker and one worker, detached"
  assert_contains "$log" "$publish" "runs the producer"
  started="$(line_of "$start")"; published="$(line_of "$publish")"
  if [ -n "$started" ] && [ -n "$published" ] && [ "$started" -lt "$published" ]; then
    ok "starts the worker before publishing"
  else
    fail "starts the worker before publishing (start line: ${started:-none}, publish line: ${published:-none})"
  fi

  echo "TEST: $target leaves a running $worker fleet untouched"
  # shellcheck disable=SC2086
  run_target "rabbitmq $worker" "$target" $extra
  [ "$RC" -eq 0 ] && ok "exit 0" || fail "exit 0 (got $RC)"
  log="$(cat "$LOG")"
  assert_not_contains "$log" " up " "does not bring anything up"
  assert_contains "$log" "$publish" "runs the producer"
done <<EOF
$CASES
EOF

PASS="$(grep -c PASS "$TALLY" || true)"
FAIL="$(grep -c FAIL "$TALLY" || true)"
echo "ingest-workers.test.sh: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
