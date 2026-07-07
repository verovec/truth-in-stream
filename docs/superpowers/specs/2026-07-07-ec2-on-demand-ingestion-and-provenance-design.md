# EC2 on-demand ingestion hosts + `/crawler` operator command + fact-check source provenance

Date: 2026-07-07
Status: Design draft, awaiting operator (user) approval before implementation planning

## Summary

Today the ingestion pipeline is well-built at the application layer but runs on **ECS
Fargate** in the cloud: worker fleets are ECS services under an EXTERNAL deployment
controller (the `worker-lifecycle` lambda) held at desired-count zero, producers are
one-shot `run-task` Fargate tasks, and the delivered `/ingest <source>` command (VER-154)
choreographs `worker-fleet.sh` (`ecs update-service`) + `run-ingest-task.sh` (`ecs
run-task`) + a CloudWatch drain wait.

This epic **re-platforms that cloud ingestion onto two stop/start-able EC2 instances** that
run the existing `docker-compose` services directly inside the VPC, driven by a
`/crawler <source>` (and `/consumer <source>`) Claude command that opens an SSM connection
through the operator's `verovec-dev` profile. The application pipeline (crawlers, queues,
workers, embedding, storage, the fact-checker) is **not rewritten** - it already implements
the crawler -> RabbitMQ -> consumer -> embed -> store shape the operator described. The work
is: verify/harden the "turn off anytime" safety, stand up the two EC2 hosts in dev with a
real RDS, ship a compose override + operator command that targets them over SSM, retire the
Fargate ingestion machinery it replaces, and add human-readable source provenance (normal
label + a `DEBUG_FACT_CHECK` detail mode) to fact-check results.

The application-layer decisions were confirmed with the operator: **replace Fargate with
EC2**, **in dev with RDS enabled**, **`/crawler` covers the four ingestable sources**
(`"media stats"` = the existing official-stats source), and **include the provenance
feature** in this epic.

## What already exists (do not rebuild)

Four crawler -> queue -> consumer pipelines run on one shared, versioned, durable RabbitMQ
transport (`internal/queue`), with publisher confirms, per-job attempt budgets,
requeue-on-shutdown, and idempotent upserts:

| Source (`/crawler` arg) | Producer (crawler) | Queue | Consumer (worker) | Table |
|---|---|---|---|---|
| `wikipedia` (dump) | `cmd/wikisync -mode=bulk` | `embedding.jobs` | `cmd/embedworker` | `wiki_chunks` |
| `wikipedia` (categories) | `cmd/wikicrawl` | `crawl.chunks` | `cmd/crawlworker` | `wiki_chunks` |
| `stats` (INSEE/Eurostat/Interior) | `cmd/statsingest` | `embedding.jobs` | `cmd/embedworker` | `wiki_chunks` |
| `factcheck` (Google Fact Check API) | `cmd/factcheckcrawl` | `factcheck.claims` | `cmd/factcheckworker` | `political_claims` |
| `scrutins` (Assemblee) | `cmd/scrutinscrawl` | `scrutins.votes` | `cmd/scrutinsworker` | `voting_records` |

`internal/source/{stats,voting,press,websearch}` also serve some of these live at query
time (Brave press/web search are live-only, no ingestion). `docker-compose.yml` already
defines every producer and worker behind profiles (`tools`, `wiki`, `factcheck`,
`scrutins`) plus an always-on `scheduler`. The `/ingest` account guard
(`scripts/aws-target-guard.sh` + `deploy/targets.json`, comparing `sts get-caller-identity`
against a committed expected account id) is reused verbatim.

## Goals

- Two EC2 hosts in the dev VPC - a **crawler host** and a **consumer host** - that the
  operator can start and stop at will, with **no message loss or corruption** across a stop
  at any moment (the "turn off anytime for FinOps" requirement).
- A `/crawler <source>` command that, via SSM over `verovec-dev`, ensures the crawler host
  is up and runs that source's producer; and a `/consumer <source>` command that does the
  same for the source's worker fleet - plus `up`/`down`/`status` sub-actions.
- The corpus lands in a real dev **RDS** so the fact-checker can read what was ingested.
- Retire the Fargate-specific ingestion machinery the EC2 model replaces, leaving one
  ingestion path to maintain.
- Fact-check results show a **human-readable source** in normal mode and, under a
  `DEBUG_FACT_CHECK` flag, **where each verdict's evidence came from** (passage text,
  `evidence_id`, source URL, score).

## Non-goals

- Rewriting the producers/workers/queues/embedding/fact-checker. The application pipeline is
  reused as-is (aside from any small safety fix the audit surfaces).
- Running anything 24/7. Both hosts are stopped between runs; nothing self-schedules. (The
  compose `scheduler` service is not deployed to the hosts in this epic.)
- Deploying to prod or running `terraform apply`. All infra changes are delivered as
  reviewed Terraform + a clean `plan`; **`apply` stays human-gated**, so the live EC2 e2e is
  an operator step after merge.
- A new "media stats" source. Confirmed to mean the existing official-stats source.

## Architecture

```
  Operator laptop (aws sso, profile verovec-dev, region eu-west-3)
     |  /crawler wikipedia   /consumer wikipedia
     v
  scripts/ingest-host.sh  (start instance -> SSM send-command -> docker compose -> [stop])
     |  aws ec2 start/stop-instances ; aws ssm send-command / start-session
     v
  crawler host (EC2, private subnet)            consumer host (EC2, private subnet)
   docker compose -f compose.ingest.yml          docker compose -f compose.ingest.yml
     run wikicrawl | statsingest |                 up embedworker | crawlworker |
         factcheckcrawl | scrutinscrawl               factcheckworker | scrutinsworker
             |  AMQPS 5671 (publish)                        |  AMQPS 5671 (consume) + 5432 (write)
             v                                              v
       Amazon MQ RabbitMQ (private) <-- durable queues --> RDS Postgres + pgvector (dev)
```

- **Two EC2 instances** (AL2023, SSM agent, IMDSv2, no public IP, private subnets), cloned
  from the `bastion` module pattern but with: a larger instance type, Docker + compose +
  git via user-data, an instance profile granting SSM core + `secretsmanager:GetSecretValue`
  on the specific secret ARNs + ECR pull + CloudWatch Logs, and a security group added to the
  broker's `allowed_security_group_ids` (5671) and the RDS SG's ingress (5432). Tagged
  `truth-in-stream-dev-crawler-host` / `-consumer-host` so scripts resolve them by Name, as
  the tunnels resolve the bastion today.
- **RDS in dev** (`enable_rds = true` for dev, or a scoped toggle) so the consumer host has a
  cloud database to write and the backend can read the ingested corpus. `apply` human-gated.
- **`docker-compose.ingest.yml`** (new): the cloud counterpart of the local compose. It drops
  `postgres`/`rabbitmq`/`minio`, points `RABBITMQ_URL`/`DATABASE_URL`/`EMBEDDING_API_KEY`/etc.
  at the managed endpoints via an env file the host materializes from Secrets Manager, and
  runs the **prebuilt ECR image** (the immutable `sha-<short>` tag the deploy already builds)
  with each service's compiled binary entrypoint (`/wikicrawl`, `/embedworker`, ...) rather
  than `go run` off bind-mounted source. Git-clone-and-build on the host is documented as a
  fallback.
- **Operator command over SSM.** `/crawler <source>` and `/consumer <source>` are thin
  commands that forward to `scripts/ingest-host.sh`, which: reuses the account guard, starts
  the target instance if stopped (`ec2 start-instances` + wait running + wait SSM online),
  runs the compose command non-interactively via `aws ssm send-command`
  (`AWS-RunShellScript`) streaming output, and - for the crawler (a one-shot producer) -
  can stop the host when the run exits. The consumer is long-running: `/consumer <source>`
  brings the worker up; `/consumer <source> down` (or `/crawler ... --stop`) stops the host
  when the queue is drained. `status` reports instance state + queue depth.

### "Turn off anytime" safety (the FinOps requirement)

Stopping a host = `docker compose down` (SIGTERM to the containers) then the EC2 stops. The
guarantee that no data is lost or corrupted across a stop at any instant rests on properties
the pipeline already claims; the audit card **verifies each and fixes any gap**:

1. Queues are **durable** and messages **persistent** (survive a broker restart) - verify
   `delivery_mode=persistent` on publish in `internal/queue`.
2. Consumers **ack only after the DB write commits**, so a mid-write stop redelivers, never
   drops.
3. On SIGTERM the worker **Nacks in-flight deliveries with requeue** (attempt not
   incremented) and exits - verify the container `stop_grace_period` is long enough for the
   largest in-flight batch to finish or requeue (Docker's default 10s may be too short;
   set it explicitly).
4. Writes are **idempotent upserts**, so an at-least-once redelivery rewrites the same row.
5. Producers are **idempotent and re-runnable**, so stopping a crawler mid-run and re-running
   later double-ingests nothing.

## Epic breakdown (cards)

Six cards. `apply` of any Terraform stays human-gated; each card's e2e is the strongest
check achievable without an apply (validate/plan, shell/unit tests, compose config
validation, command dry-run), with the live AWS run called out as the operator's post-merge
step.

- **Card A - Audit & harden ingestion for safe stop/restart.** Review the four pipelines +
  producers; verify the five safety properties above; fix any gap (message persistence,
  `stop_grace_period`, prefetch/ack ordering). Deliverable: a short assurance note in
  `docs/ingestion-pipeline.md` + code fixes + tests. *Depends on: none. Go.*
- **Card B - Terraform: dev RDS + two EC2 ingestion hosts.** New `ingestion-host` module
  (clone of `bastion` with the instance profile, SG, and user-data above); wire a crawler
  host + consumer host in `stack/terraform/dev` behind `enable_ingestion_hosts`; enable dev
  RDS; broker + RDS SG ingress for the host SG; `apply`-permissions manifest updated.
  *Depends on: none (parallel with A). Terraform.*
- **Card C - Cloud compose override + operator SSM command.** `docker-compose.ingest.yml`;
  host bootstrap/`fetch-env` script (Secrets Manager -> env file); `scripts/ingest-host.sh`
  (start/stop instance + SSM `send-command` runner, DRY_RUN-testable, account-guarded);
  `.claude/commands/crawler.md` + `.claude/commands/consumer.md`; a new ingestion-ops skill
  documenting the model + source->service map; `mayday` route; make targets. *Depends on: A,
  B. Scripts/command/infra.*
- **Card D - Retire the Fargate ingestion path.** Remove/disable the `worker` +
  `worker-lifecycle` modules and the producer/crawl-producer scheduled-tasks for ingestion;
  retire `worker-fleet.sh` / `run-ingest-task.sh` / `ingest-run.sh` / the `/ingest` command
  (or repoint `/ingest` to compose `/crawler`+`/consumer`); drop/adjust `deploy-workers.yml`'s
  ingestion mode; rewrite `docs/ingestion-pipeline.md` section 13 and `docs/infrastructure.md`
  for the EC2 model. Fargate ingestion is currently **off by default** in both envs, so this
  is code + docs cleanup, not a live teardown. *Depends on: C proven. Terraform/scripts/docs.*
- **Card E - Fact-check source label (normal mode).** Backend helper mapping the winning
  citation's `evidence_id` kind (`wiki:`/`insee:`/`eurostat:`/`voting:`/`attribution:`/
  `websearch:`/`curated`) to a human label; add it to the claim result frame; frontend renders
  "Source: INSEE" on the verdict chip. *Depends on: none (parallel). Backend + frontend.*
- **Card F - `DEBUG_FACT_CHECK` detail mode.** An env-gated debug mode (mirroring the existing
  `DEBUG_WIKI_SEARCH` pattern) that surfaces each claim's full evidence list - passage text,
  `evidence_id`, source URL, similarity/contribution score - with a frontend debug panel that
  expands the already-present `matches[]`. *Depends on: E. Backend + frontend.*

Parallelizable now: A, B, E (and F after E). C after A+B. D last.

## Data flow (a `/crawler wikipedia` run)

```
/crawler wikipedia
  -> scripts/ingest-host.sh crawler wikipedia
     -> aws-target-guard.sh --check        (sts identity vs deploy/targets.json[dev]) -> confirm
     -> aws ec2 start-instances <crawler-host>       (if stopped) ; wait running ; wait SSM online
     -> aws ssm send-command AWS-RunShellScript:
          "cd /opt/ingest && ./fetch-env.sh && docker compose -f docker-compose.ingest.yml \
             run --rm wikicrawl"            (publishes chunk jobs to crawl.chunks on Amazon MQ)
     -> stream command output ; report exit code
     -> [optional] aws ec2 stop-instances <crawler-host>
# then: /consumer wikipedia  brings up crawlworker on the consumer host to drain into RDS.
```

## Testing & verification

- **Card A:** Go table tests for the safety properties (persistent publish, ack-after-write,
  requeue-on-shutdown); `go test -race ./...` green.
- **Cards B/D (Terraform):** `terraform fmt`/`validate` + a clean `plan` in dev; the
  `apply`-permissions guard passes. No `apply`.
- **Card C:** `scripts/ingest-host.test.sh` in the existing `*.test.sh` style with a stubbed
  `aws` on PATH and `DRY_RUN=1` (start/stop, send-command shape, account-guard refusal, source
  map); `docker compose -f docker-compose.ingest.yml config` validates; the commands dry-run.
- **Cards E/F:** Go verifier tests for the label helper; Vitest for the chip + debug panel.
- **Live e2e (operator, post-merge, human-gated):** `terraform apply -var
  enable_ingestion_hosts=true -var enable_rds=true` in dev, then `/crawler wikipedia` +
  `/consumer wikipedia`, watch the queue drain into RDS, `make wiki-verify`, stop both hosts.

## Risks & mitigations

- **Can't fully e2e without an apply.** Mitigated by strong offline checks (validate/plan,
  DRY_RUN shell tests, compose config) and an explicit operator e2e runbook in the docs card.
- **EC2 is less managed than Fargate** (patching, Docker-on-host). Accepted per the operator's
  decision; mitigated by AL2023 + SSM-only access (no inbound, no SSH) and image-pull (no build
  drift).
- **Two hosts both left running cost more than Fargate-at-zero.** Mitigated by the command's
  optional auto-stop and a `status` surface; the audit guarantees a stop is always safe.
- **Overlap with the delivered `/ingest` (VER-154).** Resolved in Card D by retiring or
  repointing it so there is one operator surface, not two.

## Open questions (for operator review)

1. **Command surface:** two commands `/crawler` + `/consumer` (matches your wording and the
   two-host split), vs. reworking `/ingest <source>` into a single managed EC2 lifecycle. The
   spec assumes the former with `/ingest` retired/repointed in Card D.
2. **Auto-stop default:** should `/crawler` stop its host automatically when the producer
   exits, and `/consumer` require an explicit `down`? The spec assumes yes.
3. **Instance sizing:** default `t3.small` (crawler) / `t3.medium` (consumer), tunable via a
   variable - acceptable?
