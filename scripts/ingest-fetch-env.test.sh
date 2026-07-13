#!/usr/bin/env bash
#
# Tests for scripts/ingest-fetch-env.sh, the host-side env materializer. Stubs the
# `aws` CLI so the per-role secret map, the atomic 0600 write, the value-never-
# logged guarantee, and the fail-loud-on-missing-secret path are exercised without
# an AWS account or real Secrets Manager. Run: ./scripts/ingest-fetch-env.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FETCH="$SCRIPT_DIR/ingest-fetch-env.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws that returns a distinctive TOPSECRET marker value
# per secret id, so a test can assert the value lands in the written env file yet
# never appears in the command's stdout/stderr. SECRET_FAIL makes get-secret-value
# error for a chosen secret suffix (simulating a scoped-out or empty secret).
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  ENV_OUT="$SANDBOX/.env"
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "secretsmanager get-secret-value")
    id=""
    while [[ $# -gt 0 ]]; do [[ "$1" == "--secret-id" ]] && { id="$2"; break; }; shift; done
    if [[ -n "${SECRET_FAIL:-}" && "$id" == *"${SECRET_FAIL}"* ]]; then
      echo "An error occurred (AccessDeniedException)" >&2; exit 255; fi
    echo "TOPSECRET-value-for-${id##*/}" ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    SECRET_FAIL="${SECRET_FAIL:-}" \
    PROJECT="${PROJECT:-truth-in-stream}" \
    AWS_REGION="${AWS_REGION:-eu-west-3}" \
    INGEST_ENV_FILE="$ENV_OUT" \
    DRY_RUN="${DRY_RUN:-}"
}

echo "TEST: crawler role writes the broker, RDS DSN, and producer-side keys - not the embedding key"
(
  make_sandbox
  out="$(bash "$FETCH" crawler dev 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 for the crawler role" || fail "exit 0 for the crawler role (got $rc)"
  env="$(cat "$ENV_OUT")"
  assert_contains "$env" "RABBITMQ_URL=" "writes the broker URL"
  assert_contains "$env" "DATABASE_URL=" "writes the RDS DSN"
  assert_contains "$env" "CHECKWORTHY_API_KEY=" "writes the checkworthy key"
  assert_contains "$env" "FACTCHECK_API_KEY=" "writes the factcheck key"
  assert_not_contains "$env" "EMBEDDING_API_KEY=" "does not write the consumer-only embedding key"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "truth-in-stream/dev/rabbitmq/url" "reads the broker secret id"
  assert_contains "$log" "truth-in-stream/dev/app/factcheck-api-key" "reads the factcheck secret id"
)

echo "TEST: consumer role writes the broker, RDS DSN, and the embedding key - not the producer keys"
(
  make_sandbox
  bash "$FETCH" consumer dev >/dev/null 2>&1
  env="$(cat "$ENV_OUT")"
  assert_contains "$env" "RABBITMQ_URL=" "writes the broker URL"
  assert_contains "$env" "DATABASE_URL=" "writes the RDS DSN"
  assert_contains "$env" "EMBEDDING_API_KEY=" "writes the embedding key"
  assert_not_contains "$env" "CHECKWORTHY_API_KEY=" "does not write the crawler-only checkworthy key"
  assert_not_contains "$env" "FACTCHECK_API_KEY=" "does not write the crawler-only factcheck key"
)

echo "TEST: tvcapture role writes the client secret and Slack webhook - not the broker, DSN, or worker keys"
(
  make_sandbox
  bash "$FETCH" tvcapture dev >/dev/null 2>&1
  env="$(cat "$ENV_OUT")"
  assert_contains "$env" "TV_CAPTURE_CLIENT_SECRET=" "writes the tv-capture client secret"
  assert_contains "$env" "SLACK_WEBHOOK_URL=" "writes the Slack webhook"
  assert_not_contains "$env" "RABBITMQ_URL=" "does not write the broker URL (tvcapture uses no broker)"
  assert_not_contains "$env" "DATABASE_URL=" "does not write the RDS DSN (tvcapture uses no database)"
  assert_not_contains "$env" "EMBEDDING_API_KEY=" "does not write the embedding key"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "truth-in-stream/dev/app/tv-capture-client-secret" "reads the tv-capture client secret id"
  assert_contains "$log" "truth-in-stream/dev/app/slack-webhook-url" "reads the Slack webhook secret id"
)

echo "TEST: the secret value lands in the env file but never in the command output"
(
  make_sandbox
  out="$(bash "$FETCH" consumer dev 2>&1)"
  assert_contains "$(cat "$ENV_OUT")" "TOPSECRET-value-for-" "the secret value is written to the env file"
  assert_not_contains "$out" "TOPSECRET" "the secret value is never printed to stdout/stderr"
  assert_contains "$out" "wrote EMBEDDING_API_KEY" "logs the variable name only"
)

echo "TEST: the env file is written 0600"
(
  make_sandbox
  bash "$FETCH" crawler dev >/dev/null 2>&1
  mode="$(stat -c '%a' "$ENV_OUT" 2>/dev/null || stat -f '%Lp' "$ENV_OUT")"
  [[ "$mode" == "600" ]] && ok "env file mode is 600" || fail "env file mode is 600 (got $mode)"
)

echo "TEST: a secret the profile cannot read fails loudly, naming the variable, without a value"
(
  SECRET_FAIL="factcheck-api-key" make_sandbox
  out="$(bash "$FETCH" crawler dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when a secret cannot be read" || fail "non-zero exit when a secret cannot be read (got $rc)"
  assert_contains "$out" "FACTCHECK_API_KEY" "names the variable that failed"
  assert_not_contains "$out" "TOPSECRET" "no secret value is printed on failure"
)

echo "TEST: DRY_RUN lists the mappings without calling aws or writing the file"
(
  DRY_RUN=1 make_sandbox
  out="$(bash "$FETCH" crawler dev 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 under DRY_RUN" || fail "exit 0 under DRY_RUN (got $rc)"
  assert_contains "$out" "RABBITMQ_URL <- truth-in-stream/dev/rabbitmq/url" "lists a mapping"
  [[ ! -s "$AWS_CALL_LOG" ]] && ok "DRY_RUN calls no aws" || fail "DRY_RUN calls no aws (log: $(cat "$AWS_CALL_LOG"))"
  [[ ! -e "$ENV_OUT" ]] && ok "DRY_RUN writes no env file" || fail "DRY_RUN writes no env file"
)

echo "TEST: an unknown role is rejected"
(
  make_sandbox
  out="$(bash "$FETCH" bogus dev 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on unknown role" || fail "non-zero exit on unknown role (got $rc)"
  assert_contains "$out" "unknown role" "names the bad role"
)

passes=$(grep -c PASS "$TALLY"); fails=$(grep -c FAIL "$TALLY")
echo "ingest-fetch-env.test.sh: $passes passed, $fails failed"
[[ "$fails" -eq 0 ]]
