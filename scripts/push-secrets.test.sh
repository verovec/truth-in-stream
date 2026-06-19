#!/usr/bin/env bash
#
# Tests for scripts/push-secrets.sh. Stubs the AWS CLI and feeds a synthetic .env
# so the bulk-push workflow is exercised end to end without touching AWS. The
# central guarantee under test: a secret value never reaches an aws argv, stdout,
# stderr, or the call log; only secret NAMES and the file:// reference appear.
# Run: ./scripts/push-secrets_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PUSH="$SCRIPT_DIR/push-secrets.sh"

# Distinct, greppable sentinel values per allowlisted key, plus values for keys
# that must be filtered out (terraform-owned and non-allowlisted).
V_EMBED="voyage-SENTINEL-embed"
V_TRANSCRIBE="assemblyai-SENTINEL-transcribe"
V_AUTH_EMAIL="operator-SENTINEL@example.test"
V_AUTH_HASH="argon2id-SENTINEL-hash"
V_SESSION="session-SENTINEL-secret-aaaaaaaaaaaaaaaaaaaaaaaa"
V_DEEPSEEK="deepseek-SENTINEL-key"
V_GEMINI="gemini-SENTINEL-key"
V_SLACK="https://hooks.slack.test/SENTINEL-webhook"
V_DSN="postgres://SENTINEL-must-not-push"
V_RABBIT="amqps://SENTINEL-must-not-push"
V_NONLISTED="SENTINEL-non-allowlisted-value"

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

# Build an isolated bin/ with a fake aws and a synthetic .env, prepended to PATH.
# DESCRIBE_MISSING controls whether describe-secret reports the secret as absent
# (drives the create vs put branch). The fake records every invocation AND the
# content of any file:// referenced file, so a test can prove the pushed value
# was the right one without that value ever being an argv.
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"
  BIN="$SANDBOX/bin"
  mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws-calls.log"
  PUT_VALUES_LOG="$SANDBOX/put-values.log"
  : >"$AWS_CALL_LOG"
  : >"$PUT_VALUES_LOG"
  ENV_FILE="$SANDBOX/.env"

  cat >"$ENV_FILE" <<ENV
# a comment line and a blank line below are ignored
EMBEDDING_API_KEY=$V_EMBED

TRANSCRIPTION_API_KEY=$V_TRANSCRIBE
AUTH_EMAIL=$V_AUTH_EMAIL
AUTH_PASSWORD_HASH=$V_AUTH_HASH
SESSION_SECRET=$V_SESSION
DEEPSEEK_API_KEY=$V_DEEPSEEK
GEMINI_API_KEY=$V_GEMINI
SLACK_WEBHOOK_URL=$V_SLACK
# terraform-owned: must NOT be pushed
DATABASE_URL=$V_DSN
RABBITMQ_URL=$V_RABBIT
# not on the allowlist: must NOT be pushed
WIKI_CORPUS=$V_NONLISTED
ENV

  cat >"$BIN/aws" <<AWS
#!/usr/bin/env bash
echo "\$*" >> "$AWS_CALL_LOG"
args="\$*"
# Record the content behind any --secret-string file://PATH so a test can assert
# the right value was pushed without the value ever being an argv.
prev=""
for a in "\$@"; do
  case "\$prev" in
    --secret-string)
      case "\$a" in file://*) cat "\${a#file://}" >> "$PUT_VALUES_LOG"; echo >> "$PUT_VALUES_LOG" ;; esac ;;
  esac
  prev="\$a"
done
case "\$args" in
  *"sts get-caller-identity"*)
    [[ "\${FAKE_STS_FAIL:-}" == "1" ]] && exit 1
    echo '{"Account":"123456789012"}'; exit 0;;
  *"sso login"*) exit 0;;
  *"secretsmanager describe-secret"*)
    [[ "\${DESCRIBE_ERROR:-}" == "1" ]] && exit 255
    [[ "\${DESCRIBE_MISSING:-}" == "1" ]] && exit 254
    echo 'arn:aws:secretsmanager:eu-west-3:123456789012:secret:x'; exit 0;;
  *"secretsmanager create-secret"*) echo '{"ARN":"arn:x","VersionId":"NEW"}'; exit 0;;
  *"secretsmanager put-secret-value"*) echo '{"VersionId":"NEW"}'; exit 0;;
esac
exit 0
AWS
  chmod +x "$BIN/aws"

  export PATH="$BIN:$PATH"
  export AWS_CALL_LOG PUT_VALUES_LOG ENV_FILE
  # Point the script at the synthetic .env and a no-op SSO profile.
  export PUSH_SECRETS_ENV_FILE="$ENV_FILE"
}

cleanup() {
  rm -rf "$TMPROOT"
  rm -f "$TALLY"
}
trap cleanup EXIT

# All sentinel values, for a blanket "no value ever leaked" sweep.
ALL_VALUES=("$V_EMBED" "$V_TRANSCRIBE" "$V_AUTH_EMAIL" "$V_AUTH_HASH" "$V_SESSION" "$V_DEEPSEEK" "$V_GEMINI" "$V_SLACK" "$V_DSN" "$V_RABBIT" "$V_NONLISTED")

assert_no_value_in() { # text desc
  local text="$1" desc="$2" v leaked=""
  for v in "${ALL_VALUES[@]}"; do
    if printf '%s' "$text" | grep -qF -- "$v"; then leaked="$v"; break; fi
  done
  if [[ -n "$leaked" ]]; then fail "$desc (leaked: $leaked)"; else ok "$desc"; fi
}

set +e

echo "TEST: existing secrets are updated via put-secret-value with file:// (no value in argv)"
(
  make_sandbox
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "describe-secret" "checks existence first"
  assert_contains "$log" "put-secret-value" "updates an existing secret"
  assert_not_contains "$log" "create-secret" "does not create when the secret exists"
  assert_contains "$log" "truth-in-stream/prod/app/embedding-api-key" "targets the prod app prefix"
  assert_contains "$log" "secret-string file://" "passes the value via file:// (never argv)"
  assert_no_value_in "$log" "no secret value appears in any aws argv"
  assert_no_value_in "$out" "no secret value printed to stdout/stderr"
)

echo "TEST: missing secrets are created (idempotent create vs put branch)"
(
  make_sandbox
  export DESCRIBE_MISSING=1
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "create-secret" "creates a secret that does not exist"
  assert_not_contains "$log" "put-secret-value" "does not put-value when creating"
  assert_contains "$log" "secret-string file://" "creates with the value via file://"
  assert_no_value_in "$log" "no secret value appears in any aws argv on create"
)

echo "TEST: terraform-owned and non-allowlisted keys are never pushed"
(
  make_sandbox
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  vals="$(cat "$PUT_VALUES_LOG")"
  assert_not_contains "$log" "database-url" "DATABASE_URL is not pushed (terraform-owned)"
  assert_not_contains "$log" "rabbitmq-url" "RABBITMQ_URL is not pushed (terraform-owned)"
  assert_not_contains "$log" "wiki-corpus" "non-allowlisted key is not pushed"
  assert_not_contains "$vals" "$V_DSN" "DSN value never reaches a pushed file"
  assert_not_contains "$vals" "$V_RABBIT" "MQ value never reaches a pushed file"
  assert_not_contains "$vals" "$V_NONLISTED" "non-allowlisted value never reaches a pushed file"
)

echo "TEST: every allowlisted key present in .env is pushed exactly once"
(
  make_sandbox
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  for name in embedding-api-key transcription-api-key auth-email auth-password-hash \
    session-secret deepseek-api-key gemini-api-key slack-webhook-url; do
    count="$(grep -cF "truth-in-stream/prod/app/$name" <(grep 'put-secret-value' <<<"$log"))"
    if [[ "$count" -eq 1 ]]; then ok "pushes $name once"; else fail "pushes $name once (got $count)"; fi
  done
)

echo "TEST: the correct value is pushed for each key (via the file content, not argv)"
(
  make_sandbox
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  vals="$(cat "$PUT_VALUES_LOG")"
  assert_contains "$vals" "$V_EMBED" "embedding value reaches the pushed file"
  assert_contains "$vals" "$V_SLACK" "slack value reaches the pushed file"
  assert_contains "$vals" "$V_SESSION" "session value reaches the pushed file"
)

echo "TEST: an allowlisted key absent from .env is skipped, not pushed empty"
(
  make_sandbox
  # Drop SLACK_WEBHOOK_URL from the env file entirely.
  grep -v '^SLACK_WEBHOOK_URL=' "$ENV_FILE" >"$ENV_FILE.tmp" && mv "$ENV_FILE.tmp" "$ENV_FILE"
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "slack-webhook-url" "absent allowlisted key is skipped"
  assert_contains "$out" "skip" "reports the skip"
  assert_contains "$log" "embedding-api-key" "still pushes the keys that are present"
)

echo "TEST: an empty value in .env is skipped (would push an empty secret otherwise)"
(
  make_sandbox
  sed -i 's/^SLACK_WEBHOOK_URL=.*/SLACK_WEBHOOK_URL=/' "$ENV_FILE"
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "slack-webhook-url" "empty allowlisted value is skipped"
)

echo "TEST: prod requires an explicit typed confirmation"
(
  make_sandbox
  out="$(printf 'nope\n' | "$PUSH" prod 2>&1 || true)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "put-secret-value" "prod aborts on a wrong confirmation"
  assert_not_contains "$log" "create-secret" "prod creates nothing on a wrong confirmation"
)

echo "TEST: a missing .env fails cleanly without calling aws to push"
(
  make_sandbox
  rm -f "$ENV_FILE"
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1 || true)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "put-secret-value" "no push without a .env"
  assert_contains "$out" ".env" "reports the missing .env"
)

echo "TEST: an unknown environment argument is rejected"
(
  make_sandbox
  if printf '\n' | "$PUSH" staging >/dev/null 2>&1; then
    fail "rejects an unknown environment"
  else
    ok "rejects an unknown environment"
  fi
)

echo "TEST: an expired SSO session triggers sso login then proceeds"
(
  make_sandbox
  export FAKE_STS_FAIL=1
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "sso login" "logs in when the session is expired"
  assert_contains "$log" "put-secret-value" "still completes the push after login"
)

echo "TEST: a real describe-secret error aborts and never falls through to create-secret"
(
  make_sandbox
  export DESCRIBE_ERROR=1
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1 || true)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "create-secret" "a non-404 describe error does not create"
  assert_not_contains "$log" "put-secret-value" "a non-404 describe error does not put"
  assert_contains "$out" "describe-secret" "reports the describe failure"
)

echo "TEST: a CRLF .env does not push a value with a trailing carriage return"
(
  make_sandbox
  # Rewrite the env file with CRLF line endings.
  sed 's/$/\r/' "$ENV_FILE" >"$ENV_FILE.crlf" && mv "$ENV_FILE.crlf" "$ENV_FILE"
  out="$(printf 'prod\n' | "$PUSH" prod 2>&1)"
  vals="$(cat "$PUT_VALUES_LOG")"
  # The recorded file content for the embedding key must be the exact value with
  # no trailing CR (printf %q would show $'...\r' if a CR survived).
  pushed_embed="$(grep -F "$V_EMBED" <<<"$vals" | head -n1)"
  if printf '%s' "$pushed_embed" | grep -q $'\r'; then
    fail "strips the trailing CR from a CRLF .env value"
  else
    ok "strips the trailing CR from a CRLF .env value"
  fi
)

PASS="$(grep -c '^PASS$' "$TALLY" || true)"
FAIL="$(grep -c '^FAIL$' "$TALLY" || true)"
echo ""
echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
