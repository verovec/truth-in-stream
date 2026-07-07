#!/usr/bin/env bash
set -euo pipefail

# Fail if a real AWS account id is committed to a tracked file. This repository is
# public; account ids belong in gitignored local files (deploy/targets.json, *.tfvars)
# or a secrets store, never in the tree. See the public-repo-hygiene skill.
#
# The scan flags any 12-digit AWS-account-shaped token in a config/doc/script file that
# is not an allow-listed placeholder or an obviously-fake test fixture id. It deliberately
# does NOT hard-code the real account ids - naming them here would re-commit them and the
# scan would flag itself. Any unrecognized 12-digit token is treated as a possible leak.
#
# Allow-listed placeholder / fixture ids (safe to appear in tracked files):
#   000000000000  placeholder for a real account id
#   111111111111  test fixture
#   222222222222  test fixture
#   999999999999  test fixture
#   123456789012  AWS documentation's canonical example account id
#
# Scope: Terraform, docs, shell scripts, JSON/YAML config, slash-command/skill markdown,
# and every committed *.example template (e.g. terraform.tfvars.example, .env.example) -
# the places an account id realistically leaks, including the templates that historically
# carried one. Go source and the embedding seed cache hold unrelated long numbers and are
# out of scope by construction.
#
# Usage:
#   scripts/secret-scan.sh            scan the current repo's tracked files
#   SECRET_SCAN_ROOT=<dir> ...        scan a different working tree (used by the test)

ALLOW_IDS_RE='000000000000|111111111111|222222222222|999999999999|123456789012'

ROOT="${SECRET_SCAN_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
cd "$ROOT"

# Match by trailing extension OR any *.example template (name.ext.example ends in
# .example, so it is caught here even though its base extension is buried).
mapfile -t SCAN_FILES < <(git ls-files 2>/dev/null \
  | grep -E '\.(tf|tfvars|md|sh|json|ya?ml)$|\.example$' \
  | grep -vE '(^|/)(package-lock\.json|pnpm-lock\.yaml)$' \
  | grep -vE '(^|/)node_modules/' \
  || true)

if [[ ${#SCAN_FILES[@]} -eq 0 ]]; then
  echo "secret-scan: no scannable files."
  exit 0
fi

# `grep -o` prints one match per line as "path:line:<12 digits>"; drop the allow-listed
# placeholder/fixture ids by their trailing value. Word boundaries keep a 12-digit run
# inside a longer number (hashes, ids) from matching.
hits="$(grep -HnoE '\b[0-9]{12}\b' "${SCAN_FILES[@]}" 2>/dev/null \
  | grep -vE ":($ALLOW_IDS_RE)$" \
  || true)"

if [[ -n "$hits" ]]; then
  {
    echo "error: unrecognized 12-digit token (possible AWS account id) in tracked files:"
    printf '%s\n' "$hits"
    echo ""
    echo "This repository is public. If this is a real account id, externalize it"
    echo "(gitignored deploy/targets.json or *.tfvars, an env var, or a secrets store)"
    echo "and leave a placeholder in the tree. If it is a legitimate placeholder, add it"
    echo "to ALLOW_IDS_RE in scripts/secret-scan.sh. See the public-repo-hygiene skill."
  } >&2
  exit 1
fi

echo "secret-scan: clean (no committed AWS account ids)."
