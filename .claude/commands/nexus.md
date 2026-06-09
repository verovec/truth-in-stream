---
description: Update the GitNexus index for this repo and report status
---

Keep the GitNexus knowledge graph current for this workspace. Arguments: `$ARGUMENTS`
(pass `force` to trigger a full re-index).

1. Run `gitnexus status` to compare the indexed commit against the current commit.
2. Bring the index up to date:
   - Default: `gitnexus analyze --index-only` (incremental; cheap when already current).
   - If the user passed `force` in the arguments, use `gitnexus analyze --index-only --force`.
   - `--index-only` is mandatory here: it keeps GitNexus from mutating tracked files
     (`CLAUDE.md`, `AGENTS.md`, `.claude/skills/`).
3. Report concisely: indexed commit, node/edge/cluster counts, and whether it was already
   up to date or was refreshed.

If `gitnexus` is not on PATH (`command -v gitnexus`), tell the user it is missing (CLI lives
at `/usr/local/bin/gitnexus`, install via `gitnexus setup`) and stop.

Note: a SessionStart hook (`.claude/hooks/nexus-sync.sh`) already refreshes the index in the
background each session. Use `/nexus` for an on-demand or `force` refresh.
