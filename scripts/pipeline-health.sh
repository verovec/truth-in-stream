#!/usr/bin/env bash
# NOTE: no `set -e`. This is a read-only aggregator: a section whose backing
# service is down (local stack, RDS, an AWS account guard) must degrade to a
# clear "unavailable" line and let the rest of the report print, never abort it.
set -uo pipefail

# One read-only snapshot of the whole ingestion pipeline's health, so an operator
# sees at a glance whether the loop is turning without running several commands
# across two hosts and the database by hand. It never mutates anything.
#
# Two clearly separated sections:
#   LOCAL  - the compose stack: corpus row counts and which fleet containers run.
#   CLOUD  - the on-demand EC2 hosts: instance states, per-queue and per-DLQ
#            backlog, and each source's last-successful-run recency.
#
# Every lookup degrades on its own: the local section says so when the stack (or
# just the database) is down; the cloud section says so when the account guard or
# a metric is unavailable (the metrics lambda is opt-in). Nothing here starts,
# stops, or writes.
#
# Configuration (all optional; sensible local defaults):
#   ENV / ENVIRONMENT  target environment for the cloud hosts (default dev - the
#                      ingestion hosts live in dev). PROJECT for the name prefix.
#   PIPELINE_DB_DSN    local Postgres DSN for the corpus counts (default the
#                      compose stack on localhost:5432).
#   INGEST_METRICS_NAMESPACE  CloudWatch namespace for queue Backlog
#                      (default TruthInStream/RabbitMQ); INGEST_BROKER_NAME the
#                      Broker dimension (default <project>-<env>).
#   PIPELINE_RUN_METRICS_NAMESPACE  namespace for the per-source RunSuccess metric
#                      (default TruthInStream/Ingestion).
#   INGEST_SOURCES_MANIFEST  connector registry (default the in-repo sources.json).

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Default the target to dev before ingestion-common.sh applies its prod default:
# the crawler/consumer hosts this report reads live in the dev account.
ENVIRONMENT="${ENVIRONMENT:-${ENV:-dev}}"

# shellcheck source=scripts/ingestion-common.sh
. "$SCRIPT_DIR/ingestion-common.sh"
# shellcheck source=scripts/aws-target-guard.sh
. "$SCRIPT_DIR/aws-target-guard.sh"

PIPELINE_DB_DSN="${PIPELINE_DB_DSN:-postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable}"
INGEST_METRICS_NAMESPACE="${INGEST_METRICS_NAMESPACE:-TruthInStream/RabbitMQ}"
INGEST_BROKER_NAME="${INGEST_BROKER_NAME:-${PROJECT}-${ENVIRONMENT}}"
PIPELINE_RUN_METRICS_NAMESPACE="${PIPELINE_RUN_METRICS_NAMESPACE:-TruthInStream/Ingestion}"
INGEST_SOURCES_MANIFEST="${INGEST_SOURCES_MANIFEST:-$SCRIPT_DIR/../stack/backend/internal/connector/sources.json}"
INGEST_COMPOSE_FILE_LOCAL="${INGEST_COMPOSE_FILE_LOCAL:-docker-compose.yml}"

# Window over which a source counts as "recently successful". A source with no
# RunSuccess datapoint in this span is flagged, which is exactly what the
# no-successful-run-in-24h alarm keys on; 30h leaves headroom over a daily cron.
RUN_RECENCY_WINDOW_HOURS="${RUN_RECENCY_WINDOW_HOURS:-30}"

hr() { printf '%s\n' "------------------------------------------------------------"; }

# corpus_counts DSN: print the five corpus tallies as one tab-separated row
# (claims, evidence embedded, evidence un-embedded, political_claims,
# voting_records), or nothing on any failure so the caller degrades cleanly.
corpus_counts() {
  local dsn="$1"
  command -v psql >/dev/null 2>&1 || return 1
  local sql row
  sql="SELECT
      (SELECT count(*) FROM claims),
      (SELECT count(*) FROM evidence_chunks WHERE embedding IS NOT NULL),
      (SELECT count(*) FROM evidence_chunks WHERE embedding IS NULL),
      (SELECT count(*) FROM political_claims),
      (SELECT count(*) FROM voting_records);"
  row="$(psql "$dsn" -Atq -F $'\t' -c "$sql" 2>/dev/null)" || return 1
  [[ -n "$row" ]] || return 1
  printf '%s' "$row"
}

# local_section: corpus counts from the local Postgres and the running fleet
# containers. Every lookup degrades to a note when its backing service is absent.
local_section() {
  echo "LOCAL PIPELINE (compose stack)"
  hr

  local row
  if row="$(corpus_counts "$PIPELINE_DB_DSN")"; then
    local claims embedded unembedded political voting
    IFS=$'\t' read -r claims embedded unembedded political voting <<<"$row"
    echo "corpus (local database):"
    printf '  %-22s %s\n' "claims" "$claims"
    printf '  %-22s %s embedded, %s un-embedded\n' "evidence_chunks" "$embedded" "$unembedded"
    printf '  %-22s %s\n' "political_claims" "$political"
    printf '  %-22s %s\n' "voting_records" "$voting"
  else
    echo "corpus (local database): unavailable - is the stack up? try 'make up' (or set PIPELINE_DB_DSN)"
  fi

  echo
  if command -v docker >/dev/null 2>&1; then
    local running
    running="$(docker compose -f "$INGEST_COMPOSE_FILE_LOCAL" ps --status running --services 2>/dev/null || true)"
    if [[ -z "$running" ]]; then
      echo "fleet containers: none running (a plain 'make up' starts no worker; see 'make fleet-up')"
    else
      local sched="stopped"
      grep -qx "scheduler" <<<"$running" && sched="running"
      echo "scheduler: ${sched}"
      local workers
      workers="$(grep -E 'worker$' <<<"$running" | paste -sd' ' - || true)"
      echo "workers running: ${workers:-none}"
    fi
  else
    echo "fleet containers: unavailable - docker not on PATH"
  fi
}

# latest_metric NAMESPACE JSON_QUERY SELECTOR: run a read-only get-metric-data and
# echo the selected scalar, or "None" when there is no data or the call fails.
metric_data() {
  local start now query="$1" selector="$2"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  start="$(date -u -d "${3} ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-"${4}" +%Y-%m-%dT%H:%M:%SZ)"
  aws cloudwatch get-metric-data \
    --start-time "$start" --end-time "$now" \
    --metric-data-queries "$query" \
    --query "$selector" --output text 2>/dev/null || echo "None"
}

# backlog_for QUEUE_BASE: latest Backlog datapoint for a queue base (the main
# queue or its .dlq), or "None". Same metric shape the /consumer status reads.
backlog_for() {
  local base="$1" query
  query="$(jq -cn \
    --arg ns "$INGEST_METRICS_NAMESPACE" --arg broker "$INGEST_BROKER_NAME" --arg base "$base" \
    '[{Id:"d", MetricStat:{Metric:{Namespace:$ns, MetricName:"Backlog", Dimensions:[{Name:"Broker",Value:$broker},{Name:"QueueBase",Value:$base}]}, Period:60, Stat:"Maximum"}, ReturnData:true}]')"
  metric_data "$query" 'MetricDataResults[0].Values[-1]' '10 minutes' '10M'
}

# last_success_for SOURCE: the timestamp of the most recent RunSuccess datapoint
# for a source over the recency window, or "None" when the metric has no data
# (the run metric is opt-in, so absence is reported, not an error).
last_success_for() {
  local source="$1" query
  query="$(jq -cn \
    --arg ns "$PIPELINE_RUN_METRICS_NAMESPACE" --arg source "$source" \
    '[{Id:"r", MetricStat:{Metric:{Namespace:$ns, MetricName:"RunSuccess", Dimensions:[{Name:"Source",Value:$source}]}, Period:3600, Stat:"Sum"}, ReturnData:true}]')"
  metric_data "$query" 'MetricDataResults[0].Timestamps[-1]' "${RUN_RECENCY_WINDOW_HOURS} hours" "${RUN_RECENCY_WINDOW_HOURS}H"
}

# host_state ROLE: the instance state of the role's host by Name tag, or a note.
host_state() {
  local name="${PROJECT}-${ENVIRONMENT}-$1-host" out state
  out="$(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=${name}" \
      "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[0].Instances[0].State.Name' --output text 2>/dev/null)" || out=""
  state="$out"
  if [[ -z "$state" || "$state" == "None" ]]; then
    echo "absent (enable_ingestion_hosts off, or wrong account)"
  else
    echo "$state"
  fi
}

# cloud_body: the cloud report proper, run only once the account guard resolves.
cloud_body() {
  echo "account ${GUARD_ACCOUNT} (${GUARD_REGION}), env ${ENVIRONMENT}"
  echo
  echo "hosts:"
  printf '  %-10s %s\n' "crawler" "$(host_state crawler)"
  printf '  %-10s %s\n' "consumer" "$(host_state consumer)"

  echo
  if [[ ! -r "$INGEST_SOURCES_MANIFEST" ]]; then
    echo "queues/runs: source manifest unreadable (${INGEST_SOURCES_MANIFEST})"
    return 0
  fi

  echo "queues (backlog / DLQ):"
  local name queue
  while IFS=$'\t' read -r name queue; do
    [[ -n "$name" ]] || continue
    local depth dlq
    depth="$(backlog_for "$queue")"
    dlq="$(backlog_for "${queue}.dlq")"
    [[ -z "$depth" || "$depth" == "None" ]] && depth="n/a"
    [[ -z "$dlq" || "$dlq" == "None" ]] && dlq="n/a"
    printf '  %-12s %-16s backlog=%s dlq=%s\n' "$name" "$queue" "$depth" "$dlq"
  done < <(jq -r '.sources[] | [.name, .queue] | @tsv' "$INGEST_SOURCES_MANIFEST")

  echo
  echo "last successful run (per source, ${RUN_RECENCY_WINDOW_HOURS}h window):"
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    local ts
    ts="$(last_success_for "$name")"
    if [[ -z "$ts" || "$ts" == "None" ]]; then
      printf '  %-12s %s\n' "$name" "no success in window (or run metric unavailable)"
    else
      printf '  %-12s %s\n' "$name" "$ts"
    fi
  done < <(jq -r '.sources[].name' "$INGEST_SOURCES_MANIFEST")

  echo
  echo "corpus (cloud RDS): open 'make db-tunnel' then re-run with PIPELINE_DB_DSN pointed at the tunnel to count"
}

# cloud_section: guard the account first. The guard exits on failure (no
# targets.json, missing credentials, wrong account), so run it in a subshell and
# degrade the whole cloud section to one note rather than aborting the report.
cloud_section() {
  echo "CLOUD PIPELINE (on-demand EC2 hosts)"
  hr
  local resolved
  if resolved="$(guard_resolve >/dev/null 2>&1 && printf '%s\t%s' "$GUARD_ACCOUNT" "$GUARD_REGION")"; then
    GUARD_ACCOUNT="${resolved%%$'\t'*}"
    GUARD_REGION="${resolved##*$'\t'}"
    cloud_body
  else
    echo "unavailable - the account guard did not resolve."
    echo "  need deploy/targets.json (copy deploy/targets.example.json and fill the ${ENVIRONMENT} account id)"
    echo "  and valid AWS credentials for that account. This section is read-only; nothing was changed."
  fi
}

main() {
  ig_require_cmd jq
  echo "truth-in-stream ingestion pipeline health"
  echo
  local_section
  echo
  cloud_section
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
