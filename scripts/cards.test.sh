#!/usr/bin/env bash
#
# Tests for scripts/cards.sh, the local card queue. Each test runs against a
# throwaway queue directory (CARDS_DIR), so nothing touches a real queue and no
# git repository is needed. Run: ./scripts/cards.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CARDS="$SCRIPT_DIR/cards.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_eq()           { [ "$1" = "$2" ] && ok "$3" || fail "$3 (want '$2', got '$1')"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A fresh queue directory, exported so every cards.sh call in the test uses it.
sandbox() {
  CARDS_DIR="$(mktemp -d "$TMPROOT/q.XXXXXX")"
  export CARDS_DIR
  export CARDS_TODAY="2026-01-02"
  bash "$CARDS" init >/dev/null
}

# The serial chain A1 -> A2 -> A3 of epic A, plus an independent card B1 of epic B.
seed_chain() {
  bash "$CARDS" add A1 first "First link" --epic A >/dev/null
  bash "$CARDS" add A2 second "Second link" --epic A --deps A1 >/dev/null
  bash "$CARDS" add A3 third "Third link" --epic A --deps "A1,A2" >/dev/null
  bash "$CARDS" add B1 solo "Independent card" --epic B >/dev/null
}

status() { bash "$CARDS" list | awk -v id="$1" '$1 == id { print $2 }'; }

# Starts a background process that takes the queue lock the same way cards.sh
# does and holds it, and returns once the lock is really held. Sets HOLDER to its
# pid. The test kills it; the kernel then releases the lock.
hold_lock() {
  rm -f "$CARDS_DIR/held"
  perl -e '
    use Fcntl qw(:flock);
    open(my $fh, ">>", $ARGV[0]) or die;
    flock($fh, LOCK_EX) or die;
    open(my $m, ">", $ARGV[1]) or die; close($m);
    sleep 60;
  ' "$CARDS_DIR/.lock" "$CARDS_DIR/held" &
  HOLDER=$!
  local tries=0
  until [ -f "$CARDS_DIR/held" ]; do
    tries=$((tries + 1)); [ "$tries" -lt 100 ] || return 1
    sleep 0.1
  done
}

echo "TEST: init creates an empty queue and is idempotent"
(
  sandbox
  [ -f "$CARDS_DIR/ROADMAP.md" ] && ok "roadmap file created" || fail "roadmap file created"
  assert_eq "$(bash "$CARDS" list)" "" "a new queue lists no card"
  bash "$CARDS" add A1 first "First link" >/dev/null
  bash "$CARDS" init >/dev/null
  assert_eq "$(status A1)" "todo" "a second init keeps existing cards"
)

echo "TEST: add writes the card file, the queue row and a journal line"
(
  sandbox
  out="$(bash "$CARDS" add A1 first "First link" --epic A --note "seed")"
  assert_eq "$out" "$CARDS_DIR/A1-first.md" "prints the card file path"
  card="$(cat "$CARDS_DIR/A1-first.md")"
  assert_contains "$card" "status: todo" "card frontmatter starts todo"
  assert_contains "$card" "branch: card/A1-first" "card frontmatter names its branch"
  assert_contains "$card" "**Acceptance criteria**" "card body uses the section template"
  assert_contains "$(cat "$CARDS_DIR/ROADMAP.md")" "- 2026-01-02 · A1 · created (seed)" "journal records the creation"
  bash "$CARDS" add A1 again "Duplicate" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a duplicate id is refused"
  printf 'custom body\n' > "$CARDS_DIR/body.txt"
  bash "$CARDS" add A2 second "Second" --body-file "$CARDS_DIR/body.txt" >/dev/null
  assert_contains "$(cat "$CARDS_DIR/A2-second.md")" "custom body" "a body file replaces the template"
)

echo "TEST: next follows queue order and waits for dependencies"
(
  sandbox; seed_chain
  assert_eq "$(bash "$CARDS" next)" "A1" "the top todo card with no dependency comes first"
  bash "$CARDS" claim A1 >/dev/null
  assert_eq "$(bash "$CARDS" next)" "B1" "a card blocked by a claimed dependency is skipped"
  bash "$CARDS" set A1 in-review "PR #1"
  assert_eq "$(bash "$CARDS" next)" "B1" "a dependency in review does not unblock its dependent"
  bash "$CARDS" set A1 done "PR #1"
  assert_eq "$(bash "$CARDS" next)" "A2" "a done dependency unblocks the next link"
  bash "$CARDS" claim A2 >/dev/null
  bash "$CARDS" set A2 done "PR #2"
  assert_eq "$(bash "$CARDS" next)" "A3" "a card with two dependencies waits for both"
)

echo "TEST: next reports why nothing is eligible"
(
  sandbox; seed_chain
  bash "$CARDS" claim A1 >/dev/null
  bash "$CARDS" claim B1 >/dev/null
  out="$(bash "$CARDS" next 2>&1)"; rc=$?
  assert_eq "$rc" "3" "exit 3 when nothing is eligible"
  assert_contains "$out" "A2 todo, waits on: A1(claimed)" "names the blocking dependency and its status"
  assert_contains "$out" "A1 claimed" "lists the cards in flight"
)

echo "TEST: pick claims the next card and records it everywhere"
(
  sandbox; seed_chain
  id="$(bash "$CARDS" pick --by "session one")"
  assert_eq "$id" "A1" "pick returns the next eligible card"
  assert_eq "$(status A1)" "claimed" "queue row is claimed"
  assert_contains "$(cat "$CARDS_DIR/A1-first.md")" "status: claimed" "card frontmatter is claimed"
  assert_contains "$(cat "$CARDS_DIR/ROADMAP.md")" "- 2026-01-02 · A1 · claimed (session one)" "journal names the claimant"
  assert_eq "$(bash "$CARDS" pick)" "B1" "a second pick takes the next eligible card"
  bash "$CARDS" pick >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "3" "a third pick finds nothing eligible"
)

echo "TEST: pick --epic stays inside the epic"
(
  sandbox; seed_chain
  assert_eq "$(bash "$CARDS" pick --epic B)" "B1" "takes the epic's card although another epic is ahead"
  assert_eq "$(status A1)" "todo" "leaves the other epic untouched"
  bash "$CARDS" pick --epic B >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "3" "reports nothing eligible once the epic is drained"
)

echo "TEST: claim refuses a card that is not eligible"
(
  sandbox; seed_chain
  out="$(bash "$CARDS" claim A2 2>&1)"; rc=$?
  assert_eq "$rc" "4" "a card with an unmet dependency is refused"
  assert_contains "$out" "A2 waits on: A1(todo)" "the refusal names the dependency"
  assert_eq "$(status A2)" "todo" "a refused claim changes nothing"
  bash "$CARDS" claim A1 >/dev/null
  bash "$CARDS" claim A1 >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a card already claimed is refused"
  bash "$CARDS" claim ZZ >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "an unknown card is refused"
)

echo "TEST: set moves a card through its statuses"
(
  sandbox; seed_chain
  bash "$CARDS" claim A1 >/dev/null
  bash "$CARDS" set A1 in-review "PR #7"
  assert_eq "$(status A1)" "in-review" "queue row follows"
  assert_contains "$(cat "$CARDS_DIR/A1-first.md")" "status: in-review" "card frontmatter follows"
  bash "$CARDS" set A1 done "PR #7 · next: reuse the parser"
  assert_contains "$(cat "$CARDS_DIR/ROADMAP.md")" "- 2026-01-02 · A1 · done (PR #7 · next: reuse the parser)" "journal keeps the hand-over line"
  first="$(grep -n '^- ' "$CARDS_DIR/ROADMAP.md" | head -1)"
  assert_contains "$first" "A1 · done" "the journal is newest first"
  bash "$CARDS" set A1 finished >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "an unknown status is rejected"
  bash "$CARDS" set A2 done "skipped the claim" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a todo card cannot be marked done"
  assert_eq "$(status A2)" "todo" "a refused transition changes nothing"
  bash "$CARDS" set A1 in-review "reopened" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a done card cannot go back to review"
  bash "$CARDS" set B1 claimed >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "claiming through set is refused"
  bash "$CARDS" claim B1 >/dev/null
  bash "$CARDS" set B1 todo "released"
  assert_eq "$(bash "$CARDS" next)" "A2" "a released card rejoins the queue behind earlier eligible work"
)

echo "TEST: a status change leaves the other rows and the card body intact"
(
  sandbox; seed_chain
  before="$(bash "$CARDS" list | grep -v '^A1 ')"
  body_before="$(sed -n '/^# /,$p' "$CARDS_DIR/A1-first.md")"
  bash "$CARDS" claim A1 >/dev/null
  bash "$CARDS" set A1 blocked "missing fixture"
  assert_eq "$(bash "$CARDS" list | grep -v '^A1 ')" "$before" "other queue rows are unchanged"
  assert_eq "$(sed -n '/^# /,$p' "$CARDS_DIR/A1-first.md")" "$body_before" "the card body is unchanged"
  assert_contains "$(bash "$CARDS" list)" "Third link" "titles survive the rewrite"
)

echo "TEST: steal takes over a card held by another session"
(
  sandbox; seed_chain
  bash "$CARDS" steal A1 >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a todo card cannot be stolen"
  bash "$CARDS" claim A1 --by "dead session" >/dev/null
  assert_eq "$(bash "$CARDS" steal A1 --by "session two")" "A1" "a claimed card is taken over"
  assert_contains "$(cat "$CARDS_DIR/ROADMAP.md")" "A1 · claimed (stolen from claimed by session two)" "journal records the steal"
)

echo "TEST: simultaneous picks never hand the same card twice"
(
  sandbox
  for n in 1 2 3 4 5 6; do bash "$CARDS" add "C$n" "card-$n" "Card $n" >/dev/null; done
  out="$CARDS_DIR/picks"; mkdir "$out"
  pids=""
  for n in 1 2 3 4 5 6 7 8 9 10 11 12; do
    ( bash "$CARDS" pick --by "racer $n" >"$out/$n.id" 2>/dev/null; echo $? >"$out/$n.rc" ) &
    pids="$pids $!"
  done
  for p in $pids; do wait "$p"; done
  won="$(cat "$out"/*.id | sort)"
  assert_eq "$(printf '%s\n' "$won" | grep -c .)" "6" "six picks succeed for six cards"
  assert_eq "$(printf '%s\n' "$won" | sort -u | grep -c .)" "6" "every winner holds a different card"
  assert_eq "$(cat "$out"/*.rc | grep -c '^3$')" "6" "the six losers report nothing eligible"
  assert_eq "$(bash "$CARDS" list | awk '$2 == "claimed"' | grep -c .)" "6" "every card ends claimed exactly once"
  assert_eq "$(grep -c ' · claimed (racer ' "$CARDS_DIR/ROADMAP.md")" "6" "the journal holds one claim per card"
  bash "$CARDS" pick >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "3" "the lock is free afterwards: a new pick answers at once"
)

echo "TEST: a session that holds the lock makes the others wait, then time out"
(
  sandbox; seed_chain
  hold_lock || fail "could not take the lock for the test"
  CARDS_LOCK_TIMEOUT=1 bash "$CARDS" pick >"$CARDS_DIR/out" 2>&1; rc=$?
  assert_eq "$rc" "5" "exit 5 when the queue stays locked"
  assert_contains "$(cat "$CARDS_DIR/out")" "queue is locked" "the refusal says the queue is locked"
  assert_eq "$(status A1)" "todo" "nothing is claimed while another session holds the lock"
  assert_eq "$(bash "$CARDS" next)" "A1" "reading the queue does not need the lock"
  kill "$HOLDER" 2>/dev/null; wait "$HOLDER" 2>/dev/null
)

echo "TEST: a holder killed outright frees the queue, with nothing to recover"
(
  sandbox; seed_chain
  hold_lock || fail "could not take the lock for the test"
  kill -9 "$HOLDER" 2>/dev/null; wait "$HOLDER" 2>/dev/null
  CARDS_LOCK_TIMEOUT=2 bash "$CARDS" pick >"$CARDS_DIR/out" 2>&1; rc=$?
  assert_eq "$rc" "0" "pick succeeds right after the holder is killed"
  assert_eq "$(cat "$CARDS_DIR/out")" "A1" "and takes the next card"
)

echo "TEST: a waiting pick proceeds as soon as the holder lets go"
(
  sandbox; seed_chain
  hold_lock || fail "could not take the lock for the test"
  ( sleep 2; kill "$HOLDER" 2>/dev/null ) &
  started=$SECONDS
  CARDS_LOCK_TIMEOUT=20 bash "$CARDS" pick >"$CARDS_DIR/out" 2>&1; rc=$?
  waited=$((SECONDS - started))
  wait
  [ "$waited" -ge 1 ] && ok "the pick really waited for the holder" || fail "the pick really waited for the holder (waited ${waited}s)"
  assert_eq "$rc" "0" "the waiter gets the lock once it is released"
  assert_eq "$(cat "$CARDS_DIR/out")" "A1" "and claims the card it waited for"
)

echo "TEST: free text cannot break the queue or the journal"
(
  sandbox; seed_chain
  bash "$CARDS" claim A1 >/dev/null
  bash "$CARDS" set A1 in-review "PR #3"
  bash "$CARDS" set A1 done 'PR #3 · next: path C:\new\table & a\nb'
  journal_line="$(grep -F 'A1 · done' "$CARDS_DIR/ROADMAP.md")"
  assert_contains "$journal_line" 'C:\new\table & a\nb' "backslashes and ampersands are kept verbatim"
  assert_eq "$(grep -c 'A1 · done' "$CARDS_DIR/ROADMAP.md")" "1" "an escape sequence does not split the journal line"
  bash "$CARDS" claim A2 >/dev/null
  bash "$CARDS" set A2 blocked "$(printf 'first line\nsecond line')"
  assert_contains "$(grep -F 'A2 · blocked' "$CARDS_DIR/ROADMAP.md")" "first line second line" "a line break in a note is folded"
  bash "$CARDS" add N1 note-card "Note card" --note 'a\nb' >/dev/null
  assert_eq "$(status N1)" "todo" "a note with a backslash keeps its row on one line"
  assert_eq "$(bash "$CARDS" list | grep -c .)" "5" "the queue still has one row per card"
)

echo "TEST: dependencies are validated when a card is added"
(
  sandbox; seed_chain
  bash "$CARDS" add X1 self "Self" --deps X1 >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a card cannot depend on itself"
  bash "$CARDS" add X2 ghost "Ghost" --deps ZZ >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a dependency that does not exist is refused"
  bash "$CARDS" add X3 empty "Empty" --deps "" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "an empty dependency list is a usage error"
  bash "$CARDS" add X4 noepic "No epic" --epic >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "an option without its value is a usage error"
  assert_eq "$(bash "$CARDS" list | grep -c '^X')" "0" "no refused card reaches the queue"
  bash "$CARDS" add X5 spaced "Spaced deps" --deps "A1, B1" >/dev/null
  bash "$CARDS" claim A1 >/dev/null; bash "$CARDS" set A1 done "PR #1"
  out="$(bash "$CARDS" claim X5 2>&1)"
  assert_contains "$out" "X5 waits on: B1(todo)" "a dependency list with spaces is read correctly"
)

echo "TEST: ids that share a prefix stay separate"
(
  sandbox
  bash "$CARDS" add G1 one "Card one" >/dev/null
  bash "$CARDS" add G10 ten "Card ten" >/dev/null
  bash "$CARDS" claim G10 >/dev/null
  assert_eq "$(status G1)" "todo" "claiming G10 leaves G1 alone"
  assert_eq "$(bash "$CARDS" path G1)" "$CARDS_DIR/G1-one.md" "path G1 does not resolve to G10's file"
  assert_contains "$(cat "$CARDS_DIR/G1-one.md")" "status: todo" "G1's file is untouched"
  assert_contains "$(cat "$CARDS_DIR/G10-ten.md")" "status: claimed" "G10's file is claimed"
)

echo "TEST: a queue with an empty cell still orders cards correctly"
(
  sandbox
  bash "$CARDS" add H1 one "Card one" >/dev/null
  printf '| H2 | Hand written |  |  | todo |  |\n' > "$CARDS_DIR/row"
  awk -v f="$CARDS_DIR/row" '/^## Journal/ { while ((getline l < f) > 0) print l; print "" } { print }' "$CARDS_DIR/ROADMAP.md" > "$CARDS_DIR/r.tmp"
  mv "$CARDS_DIR/r.tmp" "$CARDS_DIR/ROADMAP.md"
  bash "$CARDS" claim H1 >/dev/null
  assert_eq "$(bash "$CARDS" next)" "H2" "a hand-written row with empty cells is eligible"
)

echo "TEST: usage errors"
(
  sandbox
  bash "$CARDS" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "no command is a usage error"
  bash "$CARDS" add "bad id" slug "Title" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "a card id with a space is rejected"
  bash "$CARDS" add A1 slug "Has | pipe" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "a title that would break the table is rejected"
  bash "$CARDS" pick --epic >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "pick --epic without a value is a usage error"
  bash "$CARDS" path "A*" >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "a path argument that is not an id is rejected"
  CARDS_DIR="$TMPROOT/absent" bash "$CARDS" list >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "a missing queue is reported, not created"
  CARDS_DIR="$TMPROOT/absent" bash "$CARDS" pick >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "4" "pick on a missing queue is refused"
  [ -e "$TMPROOT/absent" ] && fail "pick leaves no directory or lock file behind" || ok "pick leaves no directory or lock file behind"
  CARDS_LOCK_TIMEOUT=soon bash "$CARDS" pick >/dev/null 2>&1; rc=$?
  assert_eq "$rc" "2" "a lock timeout that is not a number is a usage error"
)

# EXPECTED pins the number of assertions: a test block that aborts early would
# otherwise drop its assertions and still report zero failures.
EXPECTED=88
PASS="$(grep -c PASS "$TALLY" || true)"
FAIL="$(grep -c FAIL "$TALLY" || true)"
echo "cards.test.sh: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
[ "$PASS" -eq "$EXPECTED" ] || { echo "cards.test.sh: expected $EXPECTED assertions, ran $PASS" >&2; exit 1; }
