#!/usr/bin/env bash
set -euo pipefail

# The local card queue: the ordered list of work, its dependencies and its live
# status, kept in plain Markdown under cards/ at the root of the MAIN checkout.
# The directory is gitignored on purpose: every session on this machine, whatever
# worktree it runs in, reads and writes the same files, so a claim made by one
# session is visible to the others at once. Git would only share a claim after a
# commit and a push, which is too late to stop two sessions taking the same card.
#
# Layout:
#   cards/ROADMAP.md      Queue (row order is priority) and Journal (newest first)
#   cards/<ID>-<slug>.md  one file per card, status in its frontmatter
#   cards/.lock          lock file; the lock itself lives in the kernel
#
# Every mutation runs under an exclusive lock and rewrites files through a temp
# file and a rename, so concurrent sessions serialize and a reader never sees a
# half write. `pick` finds the next eligible card and claims it inside one locked
# section: that is what makes simultaneous `/pick` sessions end up with distinct
# cards.
#
# The lock is an flock(2) on cards/.lock, taken by a small perl wrapper that then
# execs this script again with the lock held. The kernel ties the lock to the open
# file, not to anything on disk, and drops it when the process exits for any
# reason, a SIGKILL included. So there is no stale lock to detect or recover, and
# no window in which a recovering session could remove a lock that is in use.
#
# Usage:
#   cards.sh root                         print the cards directory
#   cards.sh init                         create cards/ROADMAP.md when missing
#   cards.sh add <ID> <slug> <title> [--epic E] [--deps "A,B"] [--body-file F] [--note N]
#   cards.sh list                         one line per card: id, status, deps, title
#   cards.sh path <ID>                    print the card file path
#   cards.sh next [--epic E]              print the next eligible card id
#   cards.sh pick [--epic E] [--by WHO]   atomically claim the next eligible card
#   cards.sh claim <ID> [--by WHO]        atomically claim that card
#   cards.sh set <ID> <status> [note]     in-review | done | blocked | dropped | todo
#   cards.sh steal <ID> [--by WHO]        take over a card held by a dead session
#
# A card is eligible when its status is `todo` and every card it depends on is
# `done`. Exit codes: 0 ok, 2 usage, 3 nothing eligible, 4 refused, 5 lock timeout.
#
# Configuration: CARDS_DIR overrides the directory (tests); CARDS_TODAY overrides
# the journal date (tests); CARDS_LOCK_TIMEOUT is the wait in seconds (default 30).

STATUSES="todo claimed in-review done blocked dropped"

die() { echo "cards: $1" >&2; exit "${2:-2}"; }

cards_dir() {
  if [[ -n "${CARDS_DIR:-}" ]]; then
    printf '%s\n' "$CARDS_DIR"
    return
  fi
  local common
  common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" \
    || die "not inside a git repository; set CARDS_DIR" 2
  printf '%s/cards\n' "$(dirname "$common")"
}

DIR="$(cards_dir)"
ROADMAP="$DIR/ROADMAP.md"
LOCK="$DIR/.lock"
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
TODAY="${CARDS_TODAY:-$(date +%Y-%m-%d)}"

need_roadmap() { [[ -f "$ROADMAP" ]] || die "no queue at $ROADMAP; run: cards.sh init" 4; }

valid_id() { [[ "$1" =~ ^[A-Za-z0-9]+$ ]]; }

valid_status() {
  local s
  for s in $STATUSES; do [[ "$s" == "$1" ]] && return 0; done
  return 1
}

# Free text ends up in one table cell or one journal line: fold line breaks and
# tabs into spaces so it can never split a row.
one_line() { printf '%s' "$1" | tr '\n\r\t' '   '; }

# Prints the queue as tab-separated id, title, epic, deps, status, notes: one line
# per card, in queue order. Only rows between "## Queue" and the next heading
# count. An empty cell prints as "-" so no field is ever empty: `read` collapses
# consecutive tabs, which would otherwise shift the columns.
queue_rows() {
  awk '
    /^## Queue[ \t]*$/ { inq = 1; next }
    /^## / { inq = 0 }
    !inq || $0 !~ /^\|/ { next }
    {
      n = split($0, c, "|")
      for (i = 1; i <= n; i++) { gsub(/^[ \t]+|[ \t]+$/, "", c[i]) }
      if (c[2] == "ID" || c[2] ~ /^-+$/ || c[2] == "") next
      for (i = 3; i <= 7; i++) if (c[i] == "") c[i] = "-"
      printf "%s\t%s\t%s\t%s\t%s\t%s\n", c[2], c[3], c[4], c[5], c[6], c[7]
    }' "$ROADMAP"
}

status_of() { queue_rows | awk -F'\t' -v id="$1" '$1 == id { print $5; found = 1 } END { exit !found }'; }

# Prints the dependency ids of a card, one per line; nothing when it has none.
deps_of() {
  queue_rows | awk -F'\t' -v id="$1" '$1 == id { print $4 }' | tr ',' '\n' | awk '
    { gsub(/^[ \t]+|[ \t]+$/, "") }
    $0 != "" && $0 != "-" && $0 != "—" { print }'
}

# Prints the unmet dependencies of a card, space separated; empty when eligible.
unmet_deps() {
  local dep st out=""
  while IFS= read -r dep; do
    [[ -n "$dep" ]] || continue
    st="$(status_of "$dep" 2>/dev/null || echo missing)"
    [[ "$st" == "done" ]] || out="$out $dep($st)"
  done < <(deps_of "$1")
  printf '%s' "${out# }"
}

next_eligible() {
  local epic="$1" id title cepic deps status notes
  while IFS=$'\t' read -r id title cepic deps status notes; do
    [[ "$status" == "todo" ]] || continue
    [[ -z "$epic" || "$cepic" == "$epic" ]] || continue
    [[ -z "$(unmet_deps "$id")" ]] || continue
    printf '%s\n' "$id"
    return 0
  done < <(queue_rows)
  return 3
}

explain_nothing() {
  local epic="$1" id title cepic deps status notes
  while IFS=$'\t' read -r id title cepic deps status notes; do
    [[ -z "$epic" || "$cepic" == "$epic" ]] || continue
    case "$status" in
      todo) echo "  $id todo, waits on: $(unmet_deps "$id")" >&2 ;;
      claimed|in-review|blocked) echo "  $id $status" >&2 ;;
    esac
  done < <(queue_rows)
}

# Re-runs this script with the queue lock held, unless it already is. The wrapper
# opens the lock file, keeps the descriptor open across exec, and waits for an
# exclusive flock up to the timeout; the lock is released when the re-run exits.
with_lock() {
  [[ "${CARDS_LOCK_HELD:-}" == "$LOCK" ]] && return 0
  command -v perl >/dev/null 2>&1 || die "perl is required to lock the queue" 4
  [[ "${CARDS_LOCK_TIMEOUT:-30}" =~ ^[0-9]+$ ]] || die "CARDS_LOCK_TIMEOUT must be a whole number of seconds"
  CARDS_LOCK_HELD="$LOCK" exec perl -e '
    use strict; use warnings;
    use Fcntl qw(:flock F_GETFD F_SETFD FD_CLOEXEC);
    my ($file, $timeout, @cmd) = @ARGV;
    open(my $fh, ">>", $file) or do { print STDERR "cards: cannot open lock $file: $!\n"; exit 4 };
    my $flags = fcntl($fh, F_GETFD, 0);
    fcntl($fh, F_SETFD, $flags & ~FD_CLOEXEC) or do { print STDERR "cards: cannot keep lock open: $!\n"; exit 4 };
    my $deadline = time + $timeout;
    until (flock($fh, LOCK_EX | LOCK_NB)) {
      if (time >= $deadline) { print STDERR "cards: queue is locked by another session\n"; exit 5 }
      select(undef, undef, undef, 0.1);
    }
    exec { $cmd[0] } @cmd or do { print STDERR "cards: cannot re-run under the lock: $!\n"; exit 4 };
  ' "$LOCK" "${CARDS_LOCK_TIMEOUT:-30}" "$BASH" "$SELF" "$@"
}

set_queue_status() {
  local tmp
  tmp="$(mktemp "$DIR/.roadmap.XXXXXX")"
  CARD_ID="$1" CARD_STATUS="$2" awk '
    /^## Queue[ \t]*$/ { inq = 1 }
    /^## / && $0 !~ /^## Queue[ \t]*$/ { inq = 0 }
    inq && /^\|/ {
      n = split($0, c, "|")
      probe = c[2]; gsub(/^[ \t]+|[ \t]+$/, "", probe)
      if (probe == ENVIRON["CARD_ID"]) {
        c[6] = " " ENVIRON["CARD_STATUS"] " "
        line = c[1]
        for (i = 2; i <= n; i++) line = line "|" c[i]
        print line
        next
      }
    }
    { print }' "$ROADMAP" > "$tmp"
  mv "$tmp" "$ROADMAP"
}

journal() {
  local tmp
  grep -qE '^## Journal[[:space:]]*$' "$ROADMAP" || die "$ROADMAP has no '## Journal' section" 4
  tmp="$(mktemp "$DIR/.roadmap.XXXXXX")"
  JOURNAL_LINE="- $TODAY · $1 · $(one_line "$2")" awk '
    { print }
    /^## Journal[ \t]*$/ && !added { print ENVIRON["JOURNAL_LINE"]; added = 1 }' "$ROADMAP" > "$tmp"
  mv "$tmp" "$ROADMAP"
}

card_file() {
  local f
  for f in "$DIR/$1"-*.md; do
    [[ -f "$f" ]] && { printf '%s\n' "$f"; return 0; }
  done
  return 1
}

set_card_status() {
  local file tmp
  file="$(card_file "$1")" || return 0
  tmp="$(mktemp "$DIR/.card.XXXXXX")"
  CARD_STATUS="$2" awk '
    NR == 1 && /^---/ { fm = 1; print; next }
    fm && /^---/ { fm = 0; print; next }
    fm && /^status:/ { print "status: " ENVIRON["CARD_STATUS"]; next }
    { print }' "$file" > "$tmp"
  mv "$tmp" "$file"
}

transition() {
  local id="$1" status="$2" note="$3"
  set_queue_status "$id" "$status"
  set_card_status "$id" "$status"
  journal "$id" "$status${note:+ ($note)}"
}

# A status may only follow the ones listed for it, so a card cannot be marked
# done without having been claimed, which would bypass the dependency check.
allowed_from() {
  case "$1" in
    in-review) echo "claimed in-review" ;;
    done) echo "claimed in-review" ;;
    blocked) echo "claimed in-review" ;;
    todo) echo "claimed in-review blocked" ;;
    dropped) echo "todo claimed in-review blocked" ;;
  esac
}

claim_locked() {
  local id="$1" who="$2" status waiting
  status="$(status_of "$id")" || die "unknown card $id" 4
  [[ "$status" == "todo" ]] || die "$id is $status, not todo" 4
  waiting="$(unmet_deps "$id")"
  [[ -z "$waiting" ]] || die "$id waits on: $waiting" 4
  transition "$id" claimed "$who"
}

cmd_init() {
  mkdir -p "$DIR"
  [[ -f "$ROADMAP" ]] && { echo "$ROADMAP"; return; }
  # noclobber makes the redirection an exclusive create: a concurrent init that
  # lost the race fails here instead of overwriting a queue that has cards.
  ( set -o noclobber; cat > "$ROADMAP" ) 2>/dev/null <<'EOF' || true
# Roadmap - local card queue

Single source of truth for card order, dependencies and live status. Local and
gitignored: every session on this machine shares it. Change it only through
`scripts/cards.sh`; never edit a status by hand.

## Queue
| ID | Card | Epic | Depends on | Status | Notes |
|----|------|------|------------|--------|-------|

## Journal
EOF
  echo "$ROADMAP"
}

# Reads the value of option $1 from the remaining arguments, failing as a usage
# error when it is missing.
opt_value() { [[ $# -ge 2 ]] || die "option $1 needs a value"; printf '%s' "$2"; }

cmd_add() {
  [[ $# -ge 3 ]] || die "usage: cards.sh add <ID> <slug> <title> [--epic E] [--deps A,B] [--body-file F] [--note N]"
  local id="$1" slug="$2" title note="" epic="-" deps="-" body="" dep
  title="$(one_line "$3")"
  shift 3
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --epic) epic="$(opt_value "$@")"; shift 2 ;;
      --deps) deps="$(opt_value "$@")"; shift 2 ;;
      --body-file) body="$(opt_value "$@")"; shift 2 ;;
      --note) note="$(opt_value "$@")"; note="$(one_line "$note")"; shift 2 ;;
      *) die "unknown option $1" ;;
    esac
  done
  valid_id "$id" || die "card id must be alphanumeric, got '$id'"
  [[ "$slug" =~ ^[a-z0-9-]+$ ]] || die "slug must be kebab-case, got '$slug'"
  [[ -n "$title" ]] || die "title must not be empty"
  [[ -n "$epic" && -n "$deps" ]] || die "--epic and --deps must not be empty"
  [[ "$title$note$epic$deps" != *"|"* ]] || die "title, note, epic and deps must not contain '|'"
  [[ -z "$body" || -f "$body" ]] || die "body file not found: $body" 4
  need_roadmap
  status_of "$id" >/dev/null 2>&1 && die "card $id already exists" 4
  card_file "$id" >/dev/null && die "a file for card $id already exists" 4
  if [[ "$deps" != "-" ]]; then
    while IFS= read -r dep; do
      [[ -n "$dep" ]] || continue
      [[ "$dep" != "$id" ]] || die "card $id cannot depend on itself" 4
      status_of "$dep" >/dev/null 2>&1 || die "dependency $dep does not exist; add it first" 4
    done < <(printf '%s\n' "$deps" | tr ',' '\n' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
  fi
  local file="$DIR/$id-$slug.md" tmp
  {
    printf -- '---\nid: %s\ntitle: %s\nepic: %s\nstatus: todo\ndepends_on: [%s]\nbranch: card/%s-%s\n---\n\n# %s · %s\n\n' \
      "$id" "$title" "$epic" "$([[ "$deps" == "-" ]] && echo "" || echo "$deps")" "$id" "$slug" "$id" "$title"
    if [[ -n "$body" ]]; then cat "$body"; else
      printf '**Outcome**\n\n**Context**\n\n**Approach**\n\n**Acceptance criteria**\n\n**Implementation todos**\n\n**Definition of Done**\n\n**Code review (mandatory)**\n'
    fi
  } > "$file"
  tmp="$(mktemp "$DIR/.roadmap.XXXXXX")"
  QUEUE_ROW="| $id | $title | $epic | $deps | todo | $note |" awk '
    /^## Queue[ \t]*$/ { inq = 1 }
    inq && /^## / && $0 !~ /^## Queue[ \t]*$/ { if (!added) { print ENVIRON["QUEUE_ROW"]; print ""; added = 1 }; inq = 0 }
    inq && /^[ \t]*$/ { next }
    { print }
    END { if (!added) print ENVIRON["QUEUE_ROW"] }' "$ROADMAP" > "$tmp"
  mv "$tmp" "$ROADMAP"
  journal "$id" "created${note:+ ($note)}"
  echo "$file"
}

cmd_list() {
  need_roadmap
  queue_rows | awk -F'\t' '{ printf "%-8s %-10s deps:%-14s %s\n", $1, $5, $4, $2 }'
}

parse_pick_opts() {
  EPIC=""; WHO="${CARDS_SESSION:-session $$}"
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --epic) EPIC="$(opt_value "$@")"; shift 2 ;;
      --by) WHO="$(opt_value "$@")"; shift 2 ;;
      *) die "unknown option $1" ;;
    esac
  done
}

main() {
  local cmd="${1:-}" id status allowed from
  [[ -n "$cmd" ]] || die "usage: cards.sh <root|init|add|list|path|next|pick|claim|set|steal> ..."
  case "$cmd" in
    add|pick|claim|set|steal) need_roadmap; with_lock "$@" ;;
  esac
  shift
  case "$cmd" in
    root) echo "$DIR" ;;
    init) cmd_init ;;
    add) cmd_add "$@" ;;
    list) cmd_list ;;
    path)
      [[ $# -eq 1 ]] && valid_id "$1" || die "usage: cards.sh path <ID>"
      card_file "$1" || die "no card file for $1" 4
      ;;
    next)
      need_roadmap; parse_pick_opts "$@"
      next_eligible "$EPIC" || { echo "cards: nothing eligible" >&2; explain_nothing "$EPIC"; exit 3; }
      ;;
    pick)
      need_roadmap; parse_pick_opts "$@"
      id="$(next_eligible "$EPIC")" || { echo "cards: nothing eligible" >&2; explain_nothing "$EPIC"; exit 3; }
      claim_locked "$id" "$WHO"
      echo "$id"
      ;;
    claim)
      [[ $# -ge 1 ]] && valid_id "$1" || die "usage: cards.sh claim <ID> [--by WHO]"
      need_roadmap
      id="$1"; shift
      parse_pick_opts "$@"
      claim_locked "$id" "$WHO"
      echo "$id"
      ;;
    set)
      [[ $# -ge 2 ]] && valid_id "$1" || die "usage: cards.sh set <ID> <status> [note]"
      need_roadmap
      valid_status "$2" || die "unknown status '$2' (one of: $STATUSES)"
      [[ "$2" != "claimed" ]] || die "use 'claim' or 'pick' to claim a card" 4
      status="$(status_of "$1")" || die "unknown card $1" 4
      allowed=""
      for from in $(allowed_from "$2"); do [[ "$from" == "$status" ]] && allowed=1; done
      [[ -n "$allowed" ]] || die "$1 is $status; it cannot become $2 (allowed from: $(allowed_from "$2"))" 4
      transition "$1" "$2" "${3:-}"
      ;;
    steal)
      [[ $# -ge 1 ]] && valid_id "$1" || die "usage: cards.sh steal <ID> [--by WHO]"
      need_roadmap
      id="$1"; shift
      parse_pick_opts "$@"
      status="$(status_of "$id")" || die "unknown card $id" 4
      case "$status" in
        claimed|in-review|blocked) transition "$id" claimed "stolen from $status by $WHO" ;;
        *) die "$id is $status; only a claimed, in-review or blocked card can be stolen" 4 ;;
      esac
      echo "$id"
      ;;
    *) die "unknown command '$cmd'" ;;
  esac
}

main "$@"
