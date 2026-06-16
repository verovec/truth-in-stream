#!/usr/bin/env bash
#
# Tests for scripts/deploy-ingestion.sh. Stubs the `aws` CLI so the producer
# task-definition roll, the worker fleet roll via the lifecycle deploy lambda,
# and the optional queue-version roll are exercised without an AWS account, a
# real ECS cluster, or a real lambda. `jq` is used for real.
#
# The pipeline has two producers (the dump `producer` and the crawl `wikicrawl`)
# and two worker fleets (`embedworker` and `crawlworker`); by default the deploy
# ships the one backend image to all four and skips any that are not provisioned.
# Run: ./scripts/deploy-ingestion.test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY="$SCRIPT_DIR/deploy-ingestion.sh"

TALLY="$(mktemp)"; : >"$TALLY"
TMPROOT="$(mktemp -d)"
cleanup() { rm -rf "$TMPROOT"; rm -f "$TALLY"; }
trap cleanup EXIT

ok()   { echo "  ok: $1"; echo PASS >>"$TALLY"; }
fail() { echo "  FAIL: $1" >&2; echo FAIL >>"$TALLY"; }
assert_contains()     { printf '%s' "$1" | grep -qF -- "$2" && ok "$3" || fail "$3 (missing: $2)"; }
assert_not_contains() { printf '%s' "$1" | grep -qF -- "$2" && fail "$3 (found: $2)" || ok "$3"; }

# A sandbox with a stubbed aws on PATH. The stub serves the calls the script
# makes (ecs describe-task-definition, ecs register-task-definition, lambda
# invoke) and logs every invocation so the test can assert on the arguments the
# script built. Behaviour is steered by exported env:
#   PRODUCER_MISSING        describe of the dump producer family fails
#   CRAWL_PRODUCER_MISSING  describe of the crawl producer (wikicrawl) family fails
#   WORKER_MISSING          describe of the embedworker family fails
#   CRAWLWORKER_MISSING     describe of the crawlworker family fails
#   LAMBDA_MISSING          lambda invoke fails with ResourceNotFoundException
#   LAMBDA_FUNCERROR        lambda invoke returns a FunctionError in its response
make_sandbox() {
  SANDBOX="$(mktemp -d "$TMPROOT/sb.XXXXXX")"; BIN="$SANDBOX/bin"; mkdir -p "$BIN"
  AWS_CALL_LOG="$SANDBOX/aws.log"; : >"$AWS_CALL_LOG"
  cat >"$BIN/aws" <<'AWS'
#!/usr/bin/env bash
echo "$*" >> "$AWS_CALL_LOG"
case "$1 $2" in
  "ecs describe-task-definition")
    # The family is the value after --task-definition.
    family=""
    while [[ $# -gt 0 ]]; do
      [[ "$1" == "--task-definition" ]] && { family="$2"; break; }
      shift
    done
    if [[ "$family" == *"-producer" && -n "${PRODUCER_MISSING:-}" ]]; then
      echo "An error occurred (ClientException): Unable to describe task definition." >&2
      exit 254
    fi
    if [[ "$family" == *"-wikicrawl" && -n "${CRAWL_PRODUCER_MISSING:-}" ]]; then
      echo "An error occurred (ClientException): Unable to describe task definition." >&2
      exit 254
    fi
    if [[ "$family" == *"-embedworker" && -n "${WORKER_MISSING:-}" ]]; then
      echo "An error occurred (ClientException): Unable to describe task definition." >&2
      exit 254
    fi
    if [[ "$family" == *"-crawlworker" && -n "${CRAWLWORKER_MISSING:-}" ]]; then
      echo "An error occurred (ClientException): Unable to describe task definition." >&2
      exit 254
    fi
    # A minimal but representative task definition the script edits with jq. The
    # container name echoes the family so a register call is attributable to it.
    cat <<JSON
{
  "taskDefinition": {
    "taskDefinitionArn": "arn:aws:ecs:eu-west-3:1:task-definition/${family}:7",
    "family": "${family}",
    "revision": 7,
    "status": "ACTIVE",
    "requiresAttributes": [{"name": "ecs.capability.execution-role-awslogs"}],
    "compatibilities": ["FARGATE"],
    "registeredAt": "2026-01-01T00:00:00Z",
    "registeredBy": "arn:aws:iam::1:role/x",
    "networkMode": "awsvpc",
    "cpu": "1024",
    "memory": "2048",
    "executionRoleArn": "arn:aws:iam::1:role/exec",
    "taskRoleArn": "arn:aws:iam::1:role/task",
    "requiresCompatibilities": ["FARGATE"],
    "containerDefinitions": [
      {
        "name": "${family}",
        "image": "old-registry/img:old",
        "essential": true,
        "environment": [
          {"name": "WIKI_CORPUS", "value": "wikipedia"},
          {"name": "RABBITMQ_QUEUE_VERSIONS", "value": "1"}
        ]
      }
    ]
  }
}
JSON
    ;;
  "ecs register-task-definition")
    echo '{"taskDefinition": {"taskDefinitionArn": "arn:aws:ecs:eu-west-3:1:task-definition/fam:8"}}'
    ;;
  "lambda invoke")
    if [[ -n "${LAMBDA_MISSING:-}" ]]; then
      echo "An error occurred (ResourceNotFoundException) when calling the Invoke operation: Function not found" >&2
      exit 254
    fi
    # The CLI writes the function response to the last argument (the outfile).
    outfile="${@: -1}"
    echo '{"ok": true}' >"$outfile"
    if [[ -n "${LAMBDA_FUNCERROR:-}" ]]; then
      echo '{"deploy error"}' >"$outfile"
      echo '{"StatusCode": 200, "FunctionError": "Unhandled", "ExecutedVersion": "$LATEST"}'
    else
      echo '{"StatusCode": 200, "ExecutedVersion": "$LATEST"}'
    fi
    ;;
  *)
    echo "unexpected aws call: $*" >&2; exit 99 ;;
esac
AWS
  chmod +x "$BIN/aws"
  export PATH="$BIN:$PATH" AWS_CALL_LOG \
    PRODUCER_MISSING="${PRODUCER_MISSING:-}" \
    CRAWL_PRODUCER_MISSING="${CRAWL_PRODUCER_MISSING:-}" \
    WORKER_MISSING="${WORKER_MISSING:-}" \
    CRAWLWORKER_MISSING="${CRAWLWORKER_MISSING:-}" \
    LAMBDA_MISSING="${LAMBDA_MISSING:-}" \
    LAMBDA_FUNCERROR="${LAMBDA_FUNCERROR:-}"
}

BACKEND_IMAGE="123.dkr.ecr.eu-west-3.amazonaws.com/truth-in-stream-dev-backend:sha-abc1234"

echo "TEST: image-only deploy rolls both producers and both worker fleets"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 on the happy path" || fail "exit 0 on the happy path (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" "describe-task-definition --task-definition truth-in-stream-dev-producer" "describes the dump producer task family"
  assert_contains "$log" "describe-task-definition --task-definition truth-in-stream-dev-wikicrawl" "describes the crawl producer task family"
  assert_contains "$log" "register-task-definition" "registers a new producer revision"
  assert_contains "$log" "$BACKEND_IMAGE" "pins the producer revision to the deployed image"
  assert_contains "$log" "lambda invoke" "invokes the worker-lifecycle deploy lambda"
  assert_contains "$log" "truth-in-stream-dev-workerlifecycle-deploy" "targets the deploy lambda by convention name"
  assert_contains "$log" '"image":"'"$BACKEND_IMAGE"'"' "passes the deployed image in the lambda payload"
  assert_contains "$log" '"services":["embedworker","crawlworker"]' "rolls both the embedworker and crawlworker services"
)

echo "TEST: image-only deploy does not change RABBITMQ_QUEUE_VERSIONS"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" make_sandbox
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" bash "$DEPLOY" >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  # The producer revisions keep the existing version; no worker family is
  # re-registered (the lambda copies the live revision unchanged). The worker
  # families therefore appear only in the lambda payload, never in a register call.
  assert_not_contains "$log" '"value":"1,2"' "leaves the queue versions untouched"
  reg_with_worker="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -cE "embedworker|crawlworker" || true)"
  [[ "$reg_with_worker" -eq 0 ]] && ok "does not re-register a worker family without a version roll" || fail "re-registered a worker family unexpectedly"
)

echo "TEST: queue-version roll stamps both producers and both workers"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" QUEUE_VERSIONS="1,2" make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" QUEUE_VERSIONS="1,2" bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 with a version roll" || fail "exit 0 with a version roll (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" '"name":"RABBITMQ_QUEUE_VERSIONS","value":"1,2"' "stamps the new queue versions"
  reg_with_embed="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -c "embedworker" || true)"
  [[ "$reg_with_embed" -ge 1 ]] && ok "re-registers the embedworker family so the lambda copies the rolled version" || fail "did not re-register the embedworker family for the version roll"
  reg_with_crawl="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -c "crawlworker" || true)"
  [[ "$reg_with_crawl" -ge 1 ]] && ok "re-registers the crawlworker family so the lambda copies the rolled version" || fail "did not re-register the crawlworker family for the version roll"
  assert_contains "$log" "lambda invoke" "still rolls the worker fleet via the lambda"
)

echo "TEST: a missing dump producer task definition is skipped, not fatal"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" PRODUCER_MISSING=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" PRODUCER_MISSING=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when the dump producer is absent" || fail "exit 0 when the dump producer is absent (got $rc)"
  assert_contains "$out" "truth-in-stream-dev-producer" "explains the dump producer was skipped"
  log="$(cat "$AWS_CALL_LOG")"
  reg_with_producer="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -c '"family":"truth-in-stream-dev-producer"' || true)"
  [[ "$reg_with_producer" -eq 0 ]] && ok "does not register a dump producer revision when absent" || fail "registered a dump producer revision despite absence"
  reg_with_crawl="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -c '"family":"truth-in-stream-dev-wikicrawl"' || true)"
  [[ "$reg_with_crawl" -ge 1 ]] && ok "still deploys the crawl producer" || fail "did not deploy the crawl producer"
  assert_contains "$log" "lambda invoke" "still rolls the worker fleet"
)

echo "TEST: a missing crawl producer task definition is skipped, not fatal"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" CRAWL_PRODUCER_MISSING=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" CRAWL_PRODUCER_MISSING=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when the crawl producer is absent" || fail "exit 0 when the crawl producer is absent (got $rc)"
  assert_contains "$out" "truth-in-stream-dev-wikicrawl" "explains the crawl producer was skipped"
  log="$(cat "$AWS_CALL_LOG")"
  reg_with_crawl="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -c '"family":"truth-in-stream-dev-wikicrawl"' || true)"
  [[ "$reg_with_crawl" -eq 0 ]] && ok "does not register a crawl producer revision when absent" || fail "registered a crawl producer revision despite absence"
  reg_with_producer="$(grep "register-task-definition" "$AWS_CALL_LOG" | grep -c '"family":"truth-in-stream-dev-producer"' || true)"
  [[ "$reg_with_producer" -ge 1 ]] && ok "still deploys the dump producer" || fail "did not deploy the dump producer"
)

echo "TEST: a missing embedworker fleet is dropped from the payload, not fatal"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" WORKER_MISSING=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" WORKER_MISSING=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when embedworker is absent" || fail "exit 0 when embedworker is absent (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" '"services":["crawlworker"]' "rolls only the provisioned crawlworker"
  assert_not_contains "$log" '"services":["embedworker","crawlworker"]' "drops the absent embedworker from the payload"
)

echo "TEST: a missing crawlworker fleet is dropped from the payload, not fatal"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" CRAWLWORKER_MISSING=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" CRAWLWORKER_MISSING=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when crawlworker is absent" || fail "exit 0 when crawlworker is absent (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" '"services":["embedworker"]' "rolls only the provisioned embedworker"
)

echo "TEST: when no worker fleet is provisioned the lambda roll is skipped, not fatal"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" WORKER_MISSING=1 CRAWLWORKER_MISSING=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" WORKER_MISSING=1 CRAWLWORKER_MISSING=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when no worker fleet is provisioned" || fail "exit 0 when no worker fleet is provisioned (got $rc)"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "lambda invoke" "does not invoke the deploy lambda with an empty service list"
)

echo "TEST: a missing deploy lambda is skipped, not fatal"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" LAMBDA_MISSING=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" LAMBDA_MISSING=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -eq 0 ]] && ok "exit 0 when the deploy lambda is absent" || fail "exit 0 when the deploy lambda is absent (got $rc)"
  assert_contains "$out" "worker-lifecycle" "explains the worker roll was skipped"
)

echo "TEST: a lambda FunctionError fails the deploy"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" LAMBDA_FUNCERROR=1 make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" LAMBDA_FUNCERROR=1 bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit on a lambda FunctionError" || fail "non-zero exit on a lambda FunctionError (got $rc)"
  assert_contains "$out" "FunctionError" "reports the function error"
)

echo "TEST: custom producer and worker service lists are honoured"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" \
    PRODUCER_SERVICES="producer" WORKER_SERVICES="embedworker,fastworker" make_sandbox
  PROJECT=truth-in-stream ENVIRONMENT=dev IMAGE="$BACKEND_IMAGE" \
    PRODUCER_SERVICES="producer" WORKER_SERVICES="embedworker,fastworker" bash "$DEPLOY" >/dev/null 2>&1
  log="$(cat "$AWS_CALL_LOG")"
  assert_contains "$log" '"services":["embedworker","fastworker"]' "passes every requested worker service"
  assert_not_contains "$log" "describe-task-definition --task-definition truth-in-stream-dev-wikicrawl" "does not touch the crawl producer when not requested"
)

echo "TEST: a missing IMAGE is rejected before any AWS call"
(
  PROJECT=truth-in-stream ENVIRONMENT=dev make_sandbox
  out="$(PROJECT=truth-in-stream ENVIRONMENT=dev bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when IMAGE is unset" || fail "non-zero exit when IMAGE is unset (got $rc)"
  assert_contains "$out" "IMAGE" "names the missing variable"
  log="$(cat "$AWS_CALL_LOG")"
  assert_not_contains "$log" "describe-task-definition" "fails before any AWS call"
)

echo "TEST: a missing PROJECT/ENVIRONMENT is rejected"
(
  IMAGE="$BACKEND_IMAGE" make_sandbox
  out="$(IMAGE="$BACKEND_IMAGE" bash "$DEPLOY" 2>&1)"; rc=$?
  [[ $rc -ne 0 ]] && ok "non-zero exit when PROJECT is unset" || fail "non-zero exit when PROJECT is unset (got $rc)"
  assert_contains "$out" "PROJECT" "names the missing variable"
)

PASS="$(grep -c PASS "$TALLY" || true)"; FAIL="$(grep -c FAIL "$TALLY" || true)"
echo ""; echo "PASS=$PASS FAIL=$FAIL"
[[ "$FAIL" -eq 0 ]]
