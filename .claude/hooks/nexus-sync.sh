#!/usr/bin/env bash
# SessionStart hook: keep the GitNexus index current for this repo.
# Runs detached in the background so it never delays session start, and uses
# --index-only so it never mutates tracked files (CLAUDE.md / AGENTS.md / skills).
# A flock guard prevents overlapping runs across concurrent sessions.

REPO="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"

command -v gitnexus >/dev/null 2>&1 || exit 0

slug="$(printf '%s' "$REPO" | tr '/ ' '__')"
lock="${TMPDIR:-/tmp}/nexus-sync-${slug}.lock"
log="${TMPDIR:-/tmp}/nexus-sync-${slug}.log"

setsid bash -c "
  exec 9>'$lock'
  flock -n 9 || exit 0
  cd '$REPO' || exit 0
  gitnexus analyze --index-only > '$log' 2>&1
" </dev/null >/dev/null 2>&1 &

exit 0
