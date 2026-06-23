---
description: Run a managed on-demand ingestion for one source against the current AWS account, with a wrong-account guard
---

Drive a full, managed ingestion run for one source: confirm the target AWS account,
scale the source's worker fleet up, run its producer task, wait for the queue to drain,
then scale the fleet back to zero. This command is a thin wrapper - it parses arguments
and forwards to `scripts/ingest-run.sh`, which owns the guard, the lifecycle, and every
AWS call. Do NOT run `aws`, `worker-fleet.sh`, or `run-ingest-task.sh` directly here; the
orchestrator composes them and guarantees teardown via a trap.

## Usage

```
/ingest <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]
/ingest status [source]
/ingest down <source>
```

- `<source>`: one of `stats`, `wiki`, `wiki-delta`, `wiki-categories`, `factcheck`, `scrutins`.
- `count=N`: worker replica count for the run (default 2).
- `--keep-fleet`: skip the automatic scale-to-zero teardown at the end.
- `--yes`: proceed past the confirmation gate (the orchestrator prints the preflight
  summary and stops without it).
- `-- producer-args...`: optional overrides forwarded to the producer task.
- `status [source]` is read-only: caller identity, region, cluster, the fleet's
  desired/running counts, and queue depth when available (defaults to `stats`).
- `down <source>` scales a single fleet to zero (explicit teardown after `--keep-fleet`).

`ENVIRONMENT` defaults to `prod`; override it in the environment when targeting another
account. `DRY_RUN=1` drives the whole path without touching AWS.

## What to do

1. Parse the argument string into a subcommand (`status` / `down`) or a run (`<source>`
   plus its flags). Pass everything through verbatim - the orchestrator validates the
   source, count, and flags itself, so do not reinterpret or rename them.

2. Forward to the orchestrator from the workspace root:
   - Run: `ENVIRONMENT=<env> scripts/ingest-run.sh <source> [count=N] [--keep-fleet] [--yes] [-- producer-args...]`
   - Status: `ENVIRONMENT=<env> scripts/ingest-run.sh status [source]`
   - Down: `ENVIRONMENT=<env> scripts/ingest-run.sh down <source>`

   Use the env the operator named, defaulting to `prod`.

3. The orchestrator prints the preflight summary (environment, account, caller ARN,
   region, cluster, source, fleet, count, producer) and, without `--yes`, stops for
   confirmation. Relay that summary and ask the operator to confirm; on confirmation,
   re-run with `--yes` appended. With `--yes` already present, it proceeds.

4. Report the result the orchestrator emits: a clean run (drained, fleet at zero), a
   refusal (wrong account - print expected vs actual), a fail-fast (missing env, or an
   absent producer family / worker service - name what is missing), a drain timeout
   (fleet left up, watch `/ingest status` and run `/ingest down`), or the metrics-absent
   degrade (producer ran, manual teardown advised).

## Guardrails

- Running `/ingest` against `prod` is a production-affecting action. The orchestrator's
  account guard plus the `--yes` confirmation gate the action; never bypass the
  confirmation by auto-passing `--yes` on the operator's behalf - surface the preflight
  summary and let them confirm.
- Never echo or log a secret value (the orchestrator only checks that required env is
  present). Account ids and ARNs are identifiers, not secrets, and are fine to show.
- This command creates no infrastructure and deploys no images; it only moves replica
  counts and launches producer tasks against task definitions Terraform already published.
