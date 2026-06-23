#!/usr/bin/env bash
#
# release-guard.sh - assert a release tag's commit is on main before deploying.
#
# A v* tag is the production-release gesture (see .github/workflows/release.yml),
# so it must point at a commit that is on main. A tag cut from a side branch (or
# a deleted/rewritten commit) must fail fast and deploy nothing. We verify the
# tagged commit is an ancestor of (or equal to) origin/main rather than trusting
# the tag's ref name.
#
# Usage: release-guard.sh <tag-commit-sha> [main-ref]
#   tag-commit-sha  the commit the tag resolves to (e.g. $GITHUB_SHA)
#   main-ref        the ref to test ancestry against (default: origin/main)
#
# Exits 0 when the commit is on main, non-zero otherwise. Run inside a checkout
# whose origin/main is fetched to full depth (the workflow fetches it first).

set -euo pipefail

usage() {
  echo "usage: release-guard.sh <tag-commit-sha> [main-ref]" >&2
  exit 2
}

COMMIT="${1:-}"
MAIN_REF="${2:-origin/main}"

[ -n "$COMMIT" ] || usage

# Resolve both refs up front so a missing/unfetched main-ref is a clear error
# rather than a confusing "not an ancestor" verdict.
if ! MAIN_SHA="$(git rev-parse --verify --quiet "${MAIN_REF}^{commit}")"; then
  echo "release-guard: cannot resolve ${MAIN_REF}; fetch main to full depth before the guard." >&2
  exit 1
fi
if ! COMMIT_SHA="$(git rev-parse --verify --quiet "${COMMIT}^{commit}")"; then
  echo "release-guard: cannot resolve tag commit '${COMMIT}'." >&2
  exit 1
fi

# merge-base --is-ancestor A B exits 0 iff A is an ancestor of B. A tip commit is
# its own ancestor here (the tag may sit exactly on the main HEAD), which is what
# we want: a tag on the current main HEAD must pass.
if git merge-base --is-ancestor "$COMMIT_SHA" "$MAIN_SHA"; then
  echo "release-guard: ${COMMIT_SHA} is on ${MAIN_REF} (${MAIN_SHA}); release authorized."
  exit 0
fi

echo "release-guard: ${COMMIT_SHA} is NOT on ${MAIN_REF} (${MAIN_SHA})." >&2
echo "A v* tag must be cut from a commit on main. Refusing to deploy." >&2
exit 1
