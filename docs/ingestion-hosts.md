# Ingestion hosts (on-demand cloud crawl/consume)

Operator runbook for the `/crawler` and `/consumer` commands, which run the ingestion
pipeline on two stop/start-able EC2 hosts instead of the always-on local Docker stack.
The hosts are off (or absent) by default and turn off between runs, so idle cost is only
their EBS volumes.

- Script: [`scripts/ingest-host.sh`](../scripts/ingest-host.sh) (orchestrator, all AWS calls)
- Host env materializer: [`scripts/ingest-fetch-env.sh`](../scripts/ingest-fetch-env.sh)
- Cloud compose: [`docker-compose.ingest.yml`](../docker-compose.ingest.yml)
- Commands: [`/crawler`](../.claude/commands/crawler.md), [`/consumer`](../.claude/commands/consumer.md)
- Hosts (Terraform): `stack/terraform/modules/ingestion-host`, instantiated in
  `stack/terraform/dev` behind `enable_ingestion_hosts` (default off).

## Model

Two SSM-only hosts (no inbound, no public IP, no SSH), resolved by Name tag exactly as the
bastion tunnels are:

- `truth-in-stream-<env>-crawler-host` runs the **producers** (one-shot: fill a queue, exit).
- `truth-in-stream-<env>-consumer-host` runs the **workers** (long-running: drain a queue).

The operator commands open an SSM connection through the `verovec-dev` profile, start the
host if stopped, run one Docker service via `aws ssm send-command` (`AWS-RunShellScript`)
against `docker-compose.ingest.yml`, stream the output, and can stop the host afterwards.
Every service runs the prebuilt backend ECR image (`truth-in-stream-<env>-backend`, immutable
`sha-<short>` tag; `IMAGE_TAG` selects it, default `latest`) by its compiled entrypoint -
never `go run`. The host materializes its env from Secrets Manager first
(`ingest-fetch-env.sh`), so no secret is ever passed through the SSM command or logged.

Container logs stream to CloudWatch Logs under `/truth-in-stream/<env>/ingest/*` (the exact
prefix the host instance profile is scoped to write); each producer run's stdout/stderr is
also mirrored back to the operator over SSM.

## Source map

| Source | Producer (`/crawler`) | Queue | Worker (`/consumer`) | Required producer env |
|--------|-----------------------|-------|----------------------|-----------------------|
| `wikipedia` | `wikicrawl` | `crawl.chunks` | `crawlworker` | `CRAWL_CATEGORIES` |
| `stats` | `statsingest` | `embedding.jobs` | `embedworker` | (none) |
| `factcheck` | `factcheckcrawl` | `factcheck.claims` | `factcheckworker` | `FACTCHECK_QUERIES` |
| `scrutins` | `scrutinscrawl` | `scrutins.votes` | `scrutinsworker` | (none) |

Required producer env is **non-secret** config the operator sets in the shell; the command
forwards only these (never an API key). API keys come from Secrets Manager on the host.

## Secrets consumed (Secrets Manager)

The host instance profile is scoped to exactly its role's secret ARNs; `ingest-fetch-env.sh`
fetches that role's set:

- Both roles: `truth-in-stream/<env>/rabbitmq/url` -> `RABBITMQ_URL` (broker, AMQPS 5671),
  `truth-in-stream/<env>/rds/dsn` -> `DATABASE_URL` (RDS 5432).
- Crawler host: `truth-in-stream/<env>/app/checkworthy-api-key` -> `CHECKWORTHY_API_KEY`,
  `truth-in-stream/<env>/app/factcheck-api-key` -> `FACTCHECK_API_KEY`.
- Consumer host: `truth-in-stream/<env>/app/embedding-api-key` -> `EMBEDDING_API_KEY`.

## Usage

```bash
# Fill a queue (start the crawler host, run the producer, stop the host when done):
ENVIRONMENT=dev CRAWL_CATEGORIES="Category:Retraites en France" /crawler wikipedia --stop-after

# Drain a queue (start the consumer host, bring the worker up to drain into the DB):
ENVIRONMENT=dev /consumer wikipedia            # up (default)
ENVIRONMENT=dev /consumer wikipedia status     # watch state + backlog
ENVIRONMENT=dev /consumer wikipedia down        # stop the host once the queue empties
```

- Sub-actions: `up` (default), `down` (stop the role's host), `status` (state + queue depth).
- `--stop-after` (crawler) stops the host once the producer run completes.
- A crawler producer is one-shot (`docker compose run --rm`); a consumer worker is detached
  (`docker compose up -d`) and keeps billing until the host is stopped - `down` it after the
  drain.
- `DRY_RUN=1` drives the whole path (guard, resolve, start, send-command) without touching
  AWS, printing each mutating call - use it to rehearse.

The hosts currently exist only in `dev`, so pass `ENVIRONMENT=dev` (the scripts default to
`prod` for consistency with the other ingestion tooling). `make crawler` / `make consumer`
mirror the commands (`SOURCE=`, `ACTION=`, `ENV=`).

## Cost-control lifecycle

Start on demand, run one source, stop. Stopping the host is safe at any instant: the workers
nack in-flight batches with requeue on SIGTERM within a 120s grace window (matching the ECS
`stop_timeout`), so no work is lost - see [Resilience semantics](ingestion-pipeline.md#5-resilience-semantics).
Between runs the host bills only its EBS volume.

## Prerequisites (human-gated, deferred to the operator)

1. **Fill the dev account id.** `deploy/targets.json`'s `dev.account_id` is the placeholder
   `000000000000`, so the reused account guard (`scripts/aws-target-guard.sh`) refuses every
   `dev` run until it is replaced with the real dev AWS account id. The command surfaces the
   expected-vs-actual mismatch; fix the file rather than bypassing the guard.
2. **Provision the hosts.** They live behind `enable_ingestion_hosts` (default off). Apply is
   human-gated: `terraform apply -var enable_ingestion_hosts=true` in `stack/terraform/dev`.
   Until then the command reports "no host found ... enable_ingestion_hosts is off or not
   applied" and does nothing.
3. **Populate the secrets.** The `app/*` secrets are created empty by Terraform and set out of
   band (`aws secretsmanager put-secret-value`); the broker URL and RDS DSN are published by
   their modules. A secret the host cannot read fails the run loudly, naming the variable.
4. **Repo reachable from the host.** The host clones the repo (git, installed at boot) to get
   `docker-compose.ingest.yml` and `ingest-fetch-env.sh`; `INGEST_REPO_URL` (default the local
   `origin`) and `INGEST_REPO_REF` (default `main`) select it.
