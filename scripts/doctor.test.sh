#!/usr/bin/env bash
#
# Tests for scripts/doctor.sh. Stubs `docker` (and, where needed, `make`) on a
# sandbox PATH so every check - tool presence, the Compose v2 plugin, and a
# running daemon - is exercised without Docker installed.
# Run: ./scripts/doctor.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCTOR="$SCRIPT_DIR/doctor.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }
assert_eq()           { [ "$1" = "$2" ] && ok "$3" || fail "$3 (want $2, got $1)"; }

# A stub `docker` whose `compose version` and `info` outcomes are driven by env
# (COMPOSE_MISSING, DAEMON_DOWN). Returned via DOCTOR_DOCKER as an absolute path.
make_docker_stub() {
  local bin="$TMPROOT/bin"; mkdir -p "$bin"
  cat >"$bin/docker" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  compose)
    [ "${2:-}" = version ] || exit 0
    [ -n "${COMPOSE_MISSING:-}" ] && exit 1
    echo "Docker Compose version v2.29.0"; exit 0 ;;
  info)
    [ -n "${DAEMON_DOWN:-}" ] && { echo "Cannot connect to the Docker daemon" >&2; exit 1; }
    echo "Server Version: 27.0.0"; exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$bin/docker"
  echo "$bin/docker"
}

DOCKER_STUB="$(make_docker_stub)"

echo "TEST: all checks pass"
out="$(DOCTOR_DOCKER="$DOCKER_STUB" bash "$DOCTOR" 2>&1)"; rc=$?
assert_eq "$rc" 0 "exit 0 when everything is present"
assert_contains "$out" "all good" "reports all good in verbose mode"

echo "TEST: --quiet is silent on success"
out="$(DOCTOR_DOCKER="$DOCKER_STUB" bash "$DOCTOR" --quiet 2>&1)"; rc=$?
assert_eq "$rc" 0 "exit 0 in quiet mode"
assert_eq "$out" "" "prints nothing on success in quiet mode"

echo "TEST: docker binary missing"
out="$(DOCTOR_DOCKER="$TMPROOT/bin/docker-absent" bash "$DOCTOR" --quiet 2>&1)"; rc=$?
assert_eq "$rc" 1 "exit 1 when docker is absent"
assert_contains "$out" "Docker not found" "names the missing docker binary"
assert_contains "$out" "docs.docker.com/engine/install" "points at the install docs"

echo "TEST: compose v2 plugin missing"
out="$(DOCTOR_DOCKER="$DOCKER_STUB" COMPOSE_MISSING=1 bash "$DOCTOR" --quiet 2>&1)"; rc=$?
assert_eq "$rc" 1 "exit 1 when the compose plugin is missing"
assert_contains "$out" "Compose v2 plugin not found" "names the missing compose plugin"

echo "TEST: docker daemon down"
out="$(DOCTOR_DOCKER="$DOCKER_STUB" DAEMON_DOWN=1 bash "$DOCTOR" --quiet 2>&1)"; rc=$?
assert_eq "$rc" 1 "exit 1 when the daemon is down"
assert_contains "$out" "daemon is not running" "reports the daemon is down"
assert_contains "$out" "systemctl start docker" "suggests starting the daemon"

echo "TEST: make missing"
out="$(DOCTOR_DOCKER="$DOCKER_STUB" DOCTOR_MAKE="$TMPROOT/bin/make-absent" bash "$DOCTOR" --quiet 2>&1)"; rc=$?
assert_eq "$rc" 1 "exit 1 when make is absent"
assert_contains "$out" "make not found" "names the missing make"

PASS=$(grep -c PASS "$TALLY" || true)
FAILED=$(grep -c FAIL "$TALLY" || true)
echo
echo "doctor.test.sh: $PASS passed, $FAILED failed"
[ "$FAILED" -eq 0 ]
