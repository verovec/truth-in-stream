---
description: Bring a source's cloud worker up on the consumer ingestion host over SSM to drain its queue (start, status, stop)
---

Drive one source's worker on the consumer EC2 host: confirm the target AWS account, start
the host if it is stopped, and bring that source's worker up in Docker over SSM to drain
its queue into the database; watch progress with `status` and stop the host with `down`
when the queue empties. This command is a thin wrapper - it parses arguments and forwards
to `scripts/ingest-host.sh consumer ...`, which owns the guard, EC2 start/stop, and every
`aws ssm send-command`. Do NOT run `aws`, `ec2`, or `ssm` directly here.

## Usage

```
/consumer <source> [up|down|status]
```

- `<source>`: one of `wikipedia`, `stats`, `factcheck`, `scrutins`.
- `up` (default): start the consumer host if stopped and bring the source's worker up
  (detached) to drain the queue.
- `down`: stop the consumer host (all its sources) for cost control.
- `status`: read-only host instance state + queue depth (watch the drain).

`ENVIRONMENT` selects the account (the ingestion hosts currently live in `dev`, so pass
`ENVIRONMENT=dev`; it defaults to `prod`). `DRY_RUN=1` drives the whole path without
touching AWS. The workers need nothing from the operator: their broker URL, RDS DSN, and
embedding key are read from Secrets Manager on the host.

## What to do

1. Parse the argument string into `<source>` plus an optional action (`up`/`down`/`status`).
   Pass everything through verbatim - the script validates the source and action itself.

2. Forward to the script from the workspace root:
   - `ENVIRONMENT=<env> scripts/ingest-host.sh consumer <source> [up|down|status]`

   Use the env the operator named (the hosts are in `dev` today).

3. Report what the script emits: the preflight summary (environment, account, region), the
   host state and whether it was started, that the worker came up (or the `status`
   state+backlog), or a refusal / fail-fast (wrong account - print expected vs actual;
   absent host - the `enable_ingestion_hosts` / account-id prerequisite).

## Guardrails

- The consumer worker keeps running and billing until the host is stopped: after the queue
  drains (watch `/consumer <source> status`), run `/consumer <source> down` to cap cost.
- The dev account guard refuses until `deploy/targets.json`'s `dev.account_id` placeholder
  is replaced with the real dev account id; surface that message rather than working around it.
- Never echo or log a secret value; account ids and ARNs are fine to show. This command
  creates no infrastructure; if the host is absent, report the
  `terraform apply -var enable_ingestion_hosts=true` prerequisite (human-gated); do not apply it.
