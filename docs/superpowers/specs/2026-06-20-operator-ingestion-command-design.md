# Operator-triggered ingestion via `/ingest`

Date: 2026-06-20
Status: Design approved, ready for implementation planning

## Summary

Ingestion in this project is on-demand, not scheduled: producers run as one-shot
Fargate tasks and the worker fleets sit at zero desired count between runs. Driving a
run today means stitching together `make worker-up`, `make ingest-run`, and
`make worker-down` by hand, knowing which fleet pairs with which producer, and trusting
that the shell is pointed at the right AWS account.

This spec adds a single Claude command, `/ingest <source>`, that drives a full managed
ingestion run against whatever AWS account the operator's CLI is currently authenticated
to. It guards the target account, scales the right worker fleet up, launches the source's
producer task, waits for the queue to drain, and scales the fleet back to zero. It creates
no infrastructure and deploys no images; it composes the scripts that already exist.

## Goals

- One operator command to run ingestion for any source, end to end, with no manual
  fleet/producer choreography.
- A hard guard against firing at the wrong AWS account, anchored to a local source of
  truth and an explicit confirmation step.
- A task-like lifecycle: fire it, it self-completes and tears its fleet back down to zero,
  so a run never leaves idle cost behind.
- Cover every ingestion source, failing fast and clearly when a source is not yet
  provisioned in the target account.

## Non-goals

- Creating or changing infrastructure. Terraform owns the task-definition families, the
  worker services, and the metrics lambda. This command only drives families and services
  Terraform has already published; it fails fast when one is absent.
- Deploying or rolling images. `scripts/deploy-ingestion.sh` (via the worker-lifecycle
  lambda) owns image rolls and task-set promotion. `/ingest` only moves replica counts and
  launches producer tasks against the current task definitions.
- Scheduling. Nothing self-triggers; every run is operator-initiated and human-gated by the
  confirmation step.
- Replacing the `make` targets. `make worker-up/down/status` and `make ingest-run` remain
  the lower-level primitives; `/ingest` is the managed operator surface composed on top.

## Background: what already exists

- `scripts/worker-fleet.sh <up|down|status> <fleet> [count]` scales a worker service via
  `aws ecs update-service`. Fleets: `embedworker`, `crawlworker`, `factcheckworker`,
  `scrutinsworker`. Steady-state desired count is zero.
- `scripts/run-ingest-task.sh <ingest> [-- override...]` launches a one-shot producer as
  `aws ecs run-task` against a task-definition family, polls until STOPPED, and reports the
  container exit code. Today it resolves `statsingest`, `wikisync`, and `wiki-populate`.
- `scripts/ingestion-common.sh` resolves `PROJECT`, `ENVIRONMENT`, `CLUSTER`, `SUBNETS`,
  `SECURITY_GROUP` from terraform outputs / SSM / env, never hard-coded, and provides
  `DRY_RUN=1` support that prints mutating calls instead of running them.
- `make worker-up/down/status` and `make ingest-run` wrap those scripts with `ENV=prod`
  defaults.
- Producers publish to RabbitMQ (Amazon MQ); worker fleets drain the queues. An optional
  `mqmetrics` lambda (`enable_metrics_lambda`) publishes per-queue depth to CloudWatch.

The gap this spec fills: there is no single operator entry point, no account guard, and no
managed "run until drained, then scale to zero" lifecycle.

## Components

### 1. Command surface: `.claude/commands/ingest.md`

A thin slash command. It parses arguments, runs the orchestrator script, and reports the
result. It holds no AWS logic of its own.

```
/ingest <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]
/ingest status [source]
/ingest down <source>
```

- `<source>`: one of `stats`, `wiki`, `wiki-delta`, `wiki-categories`, `factcheck`,
  `scrutins`.
- `count=N`: worker replica count for the run (default 2).
- `--keep-fleet`: skip the automatic scale-to-zero teardown at the end.
- `--yes`: proceed past the confirmation gate non-interactively, for a run the operator has
  already confirmed. Without it, the command presents the preflight summary and stops for
  confirmation.
- `-- producer-args...`: optional overrides forwarded to the producer task (e.g. a custom
  `wikisync` mode).
- `status` is read-only: it prints the caller identity, region, cluster, and each relevant
  fleet's desired/running counts plus queue depth when available.
- `down` scales a single fleet to zero (an explicit teardown if a `--keep-fleet` run was
  left up).

`ENVIRONMENT` defaults to `prod` (matching the existing targets) and is overridable.

### 2. Account guard: `scripts/aws-target-guard.sh`

Sourced by the orchestrator and by `status`. Enforces that the live AWS session points at
the intended account before any mutation.

The expected account id must come from a local, committed source of truth, never from the
account being targeted: reading the expected id from SSM in that same account would be
circular, because a wrong account would certify itself. The source of truth is a committed
`deploy/targets.json`:

```json
{
  "dev":  { "account_id": "000000000000", "region": "eu-west-3" },
  "prod": { "account_id": "000000000000", "region": "eu-west-3" }
}
```

AWS account IDs are identifiers, not secrets, so committing them is acceptable and is what
makes the guard trustworthy. (The real values are filled in at implementation time.)

The guard:

1. Calls `aws sts get-caller-identity` to read the live account id and caller ARN. A failure
   here (no/expired credentials) aborts with a clear "not authenticated" message.
2. Resolves region and ECS cluster via `ingestion-common.sh`.
3. Loads `targets.json[ENVIRONMENT]`. If the live account id does not equal the expected id,
   it refuses: prints expected vs. actual and exits non-zero. No mutation occurs.
4. On a match, emits a preflight summary (environment, account id, caller ARN, region,
   cluster, source, fleet, replica count, producer family) for the confirmation gate.

Two-phase use: `aws-target-guard.sh --check` performs steps 1-4 read-only and prints the
summary; the orchestrator only proceeds to mutate when invoked with `--yes`.

### 3. Orchestrator: `scripts/ingest-run.sh`

Owns the managed lifecycle. It composes `worker-fleet.sh` and `run-ingest-task.sh`; it does
not reimplement their AWS calls. It maps each source to its fleet, producer family,
producer override, and required environment:

| source           | fleet             | producer family   | override            | required env                                            |
|------------------|-------------------|-------------------|---------------------|---------------------------------------------------------|
| `stats`          | `embedworker`     | `statsingest`     | (none)              | optional `INSEE_API_KEY`                                |
| `wiki`           | `embedworker`     | `wikisync`        | `-mode=bulk`        | (none)                                                  |
| `wiki-delta`     | `embedworker`     | `wikisync`        | `-mode=delta`       | (none)                                                  |
| `wiki-categories`| `crawlworker`     | `wikicrawl`       | (none)              | `CRAWL_CATEGORIES` (+ `CHECKWORTHY_API_KEY` if gate on) |
| `factcheck`      | `factcheckworker` | `factcheckcrawl`  | (none)              | `FACTCHECK_API_KEY`, `FACTCHECK_QUERIES`                |
| `scrutins`       | `scrutinsworker`  | `scrutinscrawl`   | (none)              | (none)                                                  |

Lifecycle for a run:

1. Guard. Run the account guard. Refuse on mismatch; present the preflight summary and stop
   unless `--yes`.
2. Validate. Check the source's required env vars are present; fail fast with the exact
   missing names if not.
3. Preflight family/service. Confirm the producer family and worker service exist in the
   target account; if not, fail fast naming what is missing (covers the foundation-only
   `factcheck`/`scrutins` fleets and gated `wikisync`/`wikicrawl` families). No partial run.
4. Fleet up. `worker-fleet.sh up <fleet> <count>`.
5. Run producer. `run-ingest-task.sh <producer> [-- override...]`; wait for STOPPED, check
   the container exit code. A non-zero exit aborts (proceeds to teardown).
6. Drain. Wait for the source's queue to drain (see Drain detection).
7. Teardown. `worker-fleet.sh down <fleet>`, unless `--keep-fleet`.

A shell `trap` runs the teardown on any error, signal, or early exit (unless `--keep-fleet`),
so an aborted or failed run never leaves a fleet billing idle. Teardown is idempotent
(scaling an already-zero fleet to zero is a no-op). The script honours `DRY_RUN=1` end to
end through the helpers it calls, so the full path is exercisable without infra or
credentials.

This requires extending `run-ingest-task.sh`'s `resolve_ingest` to know the `wikicrawl`,
`factcheckcrawl`, and `scrutinscrawl` families in addition to the three it resolves today.
That extension is part of this work.

### 4. Drain detection

Drain is detected from the per-queue depth metric the `mqmetrics` lambda publishes to
CloudWatch. The orchestrator polls the metric for the source's queue until it reads near
zero and holds there for K consecutive polls (debounce against a transient empty read
mid-run), bounded by a timeout (`INGEST_DRAIN_TIMEOUT`, default generous enough for a bulk
run) and a poll interval (`INGEST_DRAIN_POLL_INTERVAL`). Reaching the timeout reports the
run as "producer succeeded, queue not confirmed drained" and, by default, leaves the fleet
up and tells the operator to check `/ingest status` so work in flight is not cut off.

If the metrics lambda is disabled (`enable_metrics_lambda=false`), drain cannot be observed.
The command degrades rather than fails: it confirms the producer exited 0, skips the
drain-wait, and instructs the operator to watch `/ingest status` and run `/ingest down`
when the queue empties. This keeps the command usable in environments where the metric is
not wired, at the cost of automatic teardown for those sources.

### 5. Configuration

- `deploy/targets.json` (new, committed): `{env -> {account_id, region}}`. The guard's local
  source of truth for the expected account. Account IDs are not secrets.
- No other new configuration. Everything else (cluster, subnets, security group, queue
  names) resolves through `ingestion-common.sh` from terraform outputs / SSM / env as today.

## Data flow

```
operator (AWS CLI authenticated to target account)
  -> /ingest <source>                         (.claude/commands/ingest.md)
     -> scripts/ingest-run.sh
        -> scripts/aws-target-guard.sh --check
           - sts get-caller-identity (live account)
           - deploy/targets.json     (expected account)   --> refuse on mismatch
           - preflight summary --------------------------> operator confirms (--yes)
        -> worker-fleet.sh up <fleet> <count>             (ecs update-service)
        -> run-ingest-task.sh <producer> [-- override]    (ecs run-task, wait, exit code)
        -> poll CloudWatch queue depth until drained      (mqmetrics metric)
        -> worker-fleet.sh down <fleet>                   (ecs update-service 0)  [trap-guarded]
```

## Error handling

- Not authenticated / expired credentials: abort before any mutation with a clear message.
- Wrong account (live != expected): refuse, print both ids, exit non-zero, no mutation.
- Missing required env for the source: fail fast naming the missing variables.
- Producer family or worker service absent in the target account: fail fast naming what is
  missing; no fleet is scaled up.
- Producer task exits non-zero, or stops before producing an exit code (image pull, capacity,
  IAM): report the exit code or the task stop reason, then run teardown.
- Drain timeout: report "not confirmed drained", default to leaving the fleet up, point the
  operator at `/ingest status`.
- Any error or interrupt mid-run: the teardown trap scales the fleet to zero (unless
  `--keep-fleet`) so no idle cost is left behind.

## Testing

Shell tests in the existing `scripts/*.test.sh` style, run with `DRY_RUN=1` and a stubbed
`aws` on PATH so nothing touches real infra or needs credentials:

- `aws-target-guard.test.sh`: matching account passes and emits the summary; mismatched
  account refuses (non-zero, no mutation); missing/failed `sts` aborts; missing region or
  cluster aborts; `targets.json` missing an environment aborts.
- `ingest-run.test.sh`: each source maps to the correct fleet, producer family, and
  override; required-env validation fails fast with the right names; the teardown trap fires
  and scales the fleet to zero when the producer step fails; `--keep-fleet` skips teardown;
  `--yes` bypasses the confirmation stop while the default path stops for confirmation;
  an absent family/service fails before any scale-up.
- Drain polling: reaches zero and returns; times out and reports without auto-teardown by
  default; metrics-absent path skips the wait and instructs manual teardown.
- Extended `run-ingest-task.test.sh`: the three new families (`wikicrawl`, `factcheckcrawl`,
  `scrutinscrawl`) resolve and build the expected `run-task` call.

## Dependencies and sequencing

- Terraform must publish the `wikicrawl`, `factcheckcrawl`, and `scrutinscrawl` producer
  families and the `factcheckworker` / `scrutinsworker` services in the target account before
  those sources run for real. Until then `/ingest factcheck|scrutins|wiki-categories` fails
  fast at the preflight step by design. The stats and wiki paths work against the families
  that exist today.
- Automatic drain detection depends on `enable_metrics_lambda=true`. Without it those sources
  fall back to the manual-teardown path.
- No production-affecting action is taken as part of delivering this work; running `/ingest`
  against prod remains an operator decision gated by the confirmation step.

## Open questions

None. The expected-account source (`deploy/targets.json`) and drain-detection mechanism
(CloudWatch metric with graceful fallback) were chosen during design review.
