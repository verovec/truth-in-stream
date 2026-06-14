#!/usr/bin/env sh
#
# Preflight for the local stack: verify the host has the tools `make up` needs
# (GNU make, Docker Engine, the Compose v2 plugin) and that the Docker daemon is
# running. It fails with a clear, actionable remedy instead of letting the user
# hit a deep `docker compose` stack trace.
#
# Usage: scripts/doctor.sh [--quiet]
#   --quiet  print nothing on success (used as the fast preflight `make up` runs
#            before bringing the stack up); still prints the remedy and exits
#            non-zero on the first failed check.
#
# Overridable via env (mainly for tests):
#   DOCTOR_DOCKER  docker command (default: docker)
#   DOCTOR_MAKE    make command (default: make)

set -u

QUIET=0
[ "${1:-}" = "--quiet" ] && QUIET=1

DOCKER="${DOCTOR_DOCKER:-docker}"
MAKE="${DOCTOR_MAKE:-make}"

fail() {
  echo "preflight: $1" >&2
  [ -n "${2:-}" ] && echo "  -> $2" >&2
  exit 1
}

note() { [ "$QUIET" -eq 1 ] || echo "  ok: $1"; }

command -v "$MAKE" >/dev/null 2>&1 ||
  fail "GNU make not found" "install make (Debian/Ubuntu: apt-get install make; macOS: xcode-select --install)"
note "make present"

command -v "$DOCKER" >/dev/null 2>&1 ||
  fail "Docker not found" "install Docker Engine 24.0+ (it includes Compose v2): https://docs.docker.com/engine/install/"
note "docker present"

"$DOCKER" compose version >/dev/null 2>&1 ||
  fail "Docker Compose v2 plugin not found" "install the compose plugin (ships with Docker Engine 24.0+ and Docker Desktop): https://docs.docker.com/compose/install/"
note "docker compose v2 present"

"$DOCKER" info >/dev/null 2>&1 ||
  fail "Docker daemon is not running" "start Docker (Linux: sudo systemctl start docker; macOS/Windows: launch Docker Desktop), then retry"
note "docker daemon running"

[ "$QUIET" -eq 1 ] || echo "preflight: all good - run 'make up'"
