---
description: Run a source's cloud producer on the crawler ingestion host over SSM (start it if stopped, fill the queue, stop it after)
---

Drive one source's producer on the crawler EC2 host: confirm the target AWS account,
start the host if it is stopped, run that source's crawler in Docker over SSM to fill
its queue, stream the output, and surface the container exit code; stop the host when
the crawl finishes. This command is a thin wrapper - it parses arguments and forwards to
`scripts/ingest-host.sh crawler ...`, which owns the guard, EC2 start/stop, and every
`aws ssm send-command`. Do NOT run `aws`, `ec2`, or `ssm` directly here.

## Usage

```
/crawler <source> [up|down|status] [--stop-after]
```

- `<source>`: one of `wikipedia`, `stats`, `factcheck`, `scrutins`.
- `up` (default): start the crawler host if stopped and run the source's producer.
- `down`: stop the crawler host (all its sources) for cost control.
- `status`: read-only host instance state + queue depth.
- `--stop-after`: stop the host once the producer run completes.

`ENVIRONMENT` selects the account (the ingestion hosts currently live in `dev`, so pass
`ENVIRONMENT=dev`; it defaults to `prod`). `DRY_RUN=1` drives the whole path without
touching AWS. A source's non-secret producer config (e.g. `CRAWL_CATEGORIES` for
`wikipedia`, `FACTCHECK_QUERIES` for `factcheck`) is read from the environment and
forwarded; API keys are never forwarded - the host reads them from Secrets Manager.

## What to do

1. Parse the argument string into `<source>` plus an optional action (`up`/`down`/`status`)
   and `--stop-after`. Pass everything through verbatim - the script validates the source,
   action, and required producer env itself.

2. Forward to the script from the workspace root:
   - `ENVIRONMENT=<env> scripts/ingest-host.sh crawler <source> [up|down|status] [--stop-after]`

   Use the env the operator named (the hosts are in `dev` today).

3. Report what the script emits: the preflight summary (environment, account, region), the
   host state and whether it was started, the streamed producer output and its exit code,
   or a refusal / fail-fast (wrong account - print expected vs actual; missing producer
   env - name it; absent host - the `enable_ingestion_hosts` / account-id prerequisite).

## Guardrails

- Running `/crawler` against a real account starts and runs paid infrastructure. Relay the
  preflight summary. Never echo or log a secret value; account ids and ARNs are fine to show.
- The dev account guard refuses until `deploy/targets.json`'s `dev.account_id` placeholder
  is replaced with the real dev account id; surface that message rather than working around it.
- This command creates no infrastructure. If the host is absent, report the
  `terraform apply -var enable_ingestion_hosts=true` prerequisite (human-gated); do not apply it.
