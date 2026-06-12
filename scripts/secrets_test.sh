#!/usr/bin/env bash
#
# Tests for scripts/secrets.sh. Stubs the AWS CLI and the editor so the secret
# workflow is exercised end to end without touching AWS or a real terminal.
# Run: ./scripts/secrets_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SECRETS="$SCRIPT_DIR/secrets.sh"

OLD_VALUE="voyage-OLD-key-XYZ"
NEW_VALUE="voyage-NEW-key-ABC"

# Tests run in subshells for isolation, so tally results through a file the
# parent can read back. Sandboxes live under a parent-owned root the EXIT trap
# can clean up (a subshell's own SANDBOX var never reaches the parent).
TALLY="$(mktemp)"
: >"$TALLY"
TMPROOT="$(mktemp -d)"

fail() {
  echo "  FAIL: $1" >&2
  echo "FAIL" >>"$TALLY"
}
ok() {
  echo "  ok: $1"
  echo "PASS" >>"$TALLY"
}

assert_contains() { # haystack needle desc
  if printf '%s' "$1" | grep -qF -- "$2"; then ok "$3"; else fail "$3 (missing: $2)"; fi
}
assert_not_contains() { # haystack needle desc
  if printf '%s' "$1" | grep -qF -- "$2"; then fail "$3 (found: $2)"; else ok "$3"; fi
}

# Build an isolated bin/ with a fake aws and a stub editor, prepended to PATH.
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"
  BIN="$SANDBOX/bin"
  mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws-calls.log"
  : >"$AWS_CALL_LOG"

  local val_json
  val_json="$(printf '%s' "$OLD_VALUE" | python3 -c 'import sys,json;print(json.dumps(sys.stdin.read()))')"

  cat >"$BIN/aws" <<AWS
#!/usr/bin/env bash
echo "\$*" >> "$AWS_CALL_LOG"
args="\$*"
case "\$args" in
  *"sts get-caller-identity"*)
    [[ "\${FAKE_STS_FAIL:-}" == "1" ]] && exit 1
    echo '{"Account":"123456789012"}'; exit 0;;
  *"sso login"*) exit 0;;
  *"secretsmanager list-secrets"*)
    echo '{"SecretList":[{"Name":"truth-in-stream/dev/app/embedding-api-key"},{"Name":"truth-in-stream/dev/app/transcription-api-key"}]}'
    exit 0;;
  *"secretsmanager get-secret-value"*)
    printf '{"VersionId":"OUTGOING-1111","SecretString":%s}' '$val_json'; exit 0;;
  *"secretsmanager put-secret-value"*)
    echo '{"VersionId":"NEW-2222","VersionStages":["AWSCURRENT"]}'; exit 0;;
  *"secretsmanager update-secret-version-stage"*)
    echo '{}'; exit 0;;
esac
exit 0
AWS
  chmod +x "$BIN/aws"

  cat >"$BIN/stubedit" <<'EDIT'
#!/usr/bin/env bash
f="$1"
[[ "${STUB_EDIT_NOOP:-}" == "1" ]] && exit 0
printf '%s' "${STUB_EDIT_TO:-changed}" > "$f"
EDIT
  chmod +x "$BIN/stubedit"

  export PATH="$BIN:$PATH"
  export EDITOR="stubedit"
  export AWS_CALL_LOG
}

cleanup() {
  rm -rf "$TMPROOT"
  rm -f "$TALLY"
}
trap cleanup EXIT

# A failed assertion records to the tally rather than aborting; let every test
# run, then decide pass/fail from the tally at the end.
set +e

echo "TEST: dev happy path edits, pushes, and labels the outgoing version"
(
  make_sandbox
  export STUB_EDIT_TO="$NEW_VALUE"
  out="$(printf '1\ny\n' | "$SECRETS" dev 2>/dev/null)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "list-secrets" "lists secrets"
  assert_contains "$log" "truth-in-stream/dev/" "filters on the dev prefix"
  assert_contains "$log" "get-secret-value" "fetches the selected secret"
  assert_contains "$log" "put-secret-value" "pushes a new value"
  assert_contains "$log" "secret-string file://" "pushes via file:// (no value in argv)"
  assert_contains "$log" "update-secret-version-stage" "labels the outgoing version"
  assert_contains "$log" "move-to-version-id OUTGOING-1111" "labels the recorded outgoing version id"
  assert_contains "$log" "version-stage v-" "uses a timestamped version stage"
  assert_not_contains "$log" "$NEW_VALUE" "new secret value never appears in any aws argv"
  assert_not_contains "$log" "$OLD_VALUE" "old secret value never appears in any aws argv"
  assert_not_contains "$out" "$NEW_VALUE" "new secret value never printed to stdout"
  assert_not_contains "$out" "$OLD_VALUE" "old secret value never printed to stdout"
)

echo "TEST: an unchanged edit pushes nothing"
(
  make_sandbox
  export STUB_EDIT_NOOP=1
  out="$(printf '1\ny\n' | "$SECRETS" dev 2>/dev/null)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$out" "No changes" "reports no changes"
  assert_not_contains "$log" "put-secret-value" "does not push when nothing changed"
)

echo "TEST: declining the diff confirmation pushes nothing"
(
  make_sandbox
  export STUB_EDIT_TO="$NEW_VALUE"
  out="$(printf '1\nn\n' | "$SECRETS" dev 2>/dev/null || true)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "put-secret-value" "does not push when the operator declines"
)

echo "TEST: an expired SSO session triggers sso login"
(
  make_sandbox
  export FAKE_STS_FAIL=1
  export STUB_EDIT_TO="$NEW_VALUE"
  out="$(printf '1\ny\n' | "$SECRETS" dev 2>/dev/null)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "sso login" "logs in when the session is expired"
  assert_contains "$log" "put-secret-value" "still completes the roll after login"
)

echo "TEST: prod requires an explicit typed confirmation"
(
  make_sandbox
  export STUB_EDIT_TO="$NEW_VALUE"
  # Wrong confirmation aborts.
  out="$(printf '1\ny\nnope\n' | "$SECRETS" prod 2>/dev/null || true)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "put-secret-value" "prod aborts on a wrong confirmation"
)

echo "TEST: prod proceeds when the env name is typed"
(
  make_sandbox
  export STUB_EDIT_TO="$NEW_VALUE"
  out="$(printf '1\ny\nprod\n' | "$SECRETS" prod 2>/dev/null)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "put-secret-value" "prod proceeds when confirmed"
)

echo "TEST: a non-numeric menu choice is rejected cleanly"
(
  make_sandbox
  export STUB_EDIT_TO="$NEW_VALUE"
  err="$(printf 'q\n' | "$SECRETS" dev 2>&1 >/dev/null)"
  rc=$?
  [[ $rc -ne 0 ]] && ok "rejects a non-numeric menu choice" || fail "rejects a non-numeric menu choice"
  assert_contains "$err" "Invalid selection" "reports a clean error, not a shell crash"
  assert_not_contains "$err" "unbound variable" "does not abort on arithmetic before the guard"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "put-secret-value" "non-numeric choice pushes nothing"
) || true

echo "TEST: a bad environment argument is rejected"
(
  make_sandbox
  if printf '\n' | "$SECRETS" staging >/dev/null 2>&1; then
    fail "rejects an unknown environment"
  else
    ok "rejects an unknown environment"
  fi
)

PASS="$(grep -c '^PASS$' "$TALLY" || true)"
FAIL="$(grep -c '^FAIL$' "$TALLY" || true)"
echo ""
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
