# scheduled-task

EventBridge Scheduler invoking a one-shot Fargate task on a cron. Mirrors the
`migration` module's task-definition pattern and adds a dedicated schedule
group, the schedule, and the IAM role Scheduler assumes to call `ecs:RunTask`
(scoped to this task family and cluster, with `iam:PassRole` limited to the
task's two roles; the trust policy is pinned to the module-owned group ARN).

Used for two jobs, each instantiated in both env roots behind its own enable
flag that defaults to `false` so plans stay no-op until a human flips it:

- the weekly Wikipedia delta sync (`wikisync -mode=delta`), behind
  `enable_wiki_sync`;
- the nightly database backup (`dbbackup`), behind `enable_db_backup` (see
  [Scheduled database backup](#scheduled-database-backup)).

## Usage

See the `wiki_sync` block in `dev/main.tf` or `prod/main.tf` for the live
instantiation. The module's own surface:

```hcl
module "nightly_job" {
  source = "../modules/scheduled-task"

  project     = local.project
  environment = var.environment
  name        = "nightly-job"

  image       = "<image uri>"
  entry_point = ["/binary"] # [] keeps the image entrypoint
  command     = ["-flag"]   # [] keeps the image default

  schedule_expression = "cron(0 3 ? * SUN *)"
  cpu                 = 1024
  memory              = 4096

  # plus: environment_variables, secrets, cluster_arn, subnet_ids,
  # security_group_ids, task_execution_role_arn, task_role_arn, log_group_name
}
```

Prerequisites before enabling:

- The backend image ships a `/wikisync` binary (the roots' `wiki_sync` blocks
  set `entry_point` to override the image's `/server` default).
- The execution role can read every secret passed in `secrets` (the `iam`
  module's `secret_arns` already covers the DSN and `embedding-api-key`
  secrets both roots pass).

Known limitation: invocation retries are bounded (see `retry_policy` in
`main.tf`) and there is no dead letter queue. Add a DLQ and an alarm when an
alerting stack exists.

## Scheduled database backup

The `db_backup` block in `dev/main.tf` / `prod/main.tf` runs `pg_dump` against
RDS on a cron and uploads the custom-format archive to the `s3-backup` bucket,
so a snapshot exists without anyone running `make backup`. Scheduled and manual
dumps share the `db-backups/<db>-<timestamp>.dump` key convention, so
`make restore` consumes either unchanged.

Enable and tune it with the env roots' variables:

| Variable | Default | Meaning |
|---|---|---|
| `enable_db_backup` | `false` | Create the schedule. Flipping it is a no-op on running services. |
| `db_backup_schedule` | `cron(0 4 * * ? *)` | EventBridge Scheduler expression, UTC. Default is daily at 04:00. (6 fields: `cron(min hour day-of-month month day-of-week year)`; use `?` in one of the day fields.) |
| `db_backup_cpu` / `db_backup_memory` | `512` / `1024` | Fargate task size. |

Prerequisites before enabling:

- The `backup` ECR repository holds an image built from
  `stack/backend/Dockerfile.backup` (a static `dbbackup` binary plus the
  PostgreSQL 17 client; the client major must match or exceed the RDS server).
  The image's `ENTRYPOINT` is the binary, so the block passes empty
  `entry_point`/`command`.
- The task role can write the backup bucket: the `iam` module grants
  `s3:PutObject` when `db_backup_bucket_arn` is set, which both roots wire from
  the `db_backup_storage` module. The DSN is injected from Secrets Manager
  (already in the execution role's `secret_arns`).

## RDS sizing for the Wikipedia corpus

`WIKI_CORPUS` decides the working set. Vectors are `halfvec(1024)` (2 KB
each) under an HNSW index; queries need the index resident in memory to stay
fast, so size the instance to the corpus before flipping it.

| Corpus | Articles | Working set | Instance | Storage | Notes |
|---|---|---|---|---|---|
| `simplewiki` (dev default) | ~250k | < 1 GB | current `db.t4g.micro` | current 20 GB gp3 | Fits as-is; no change needed |
| `enwiki` lead sections (prod target) | ~7M | ~22 GB (chunks + vectors + HNSW) | `db.r7g.2xlarge` (8 vCPU, 64 GiB) | 500 GB gp3 | Index plus hot rows fit in memory with headroom for Postgres itself |

To move prod to `enwiki`: set `instance_class`, `allocated_storage`, and
`max_allocated_storage` (the autoscaling ceiling, which must also rise to
cover 500 GB) on the `rds` module, then `wiki_corpus = "enwiki"`. All three
are already `rds` module variables; the one genuinely new resource is below.

HNSW build memory: bulk index builds are dramatically faster when the graph
fits in `maintenance_work_mem`. The RDS default (capped at 1 GiB) forces a
slow on-disk build for enwiki; raise it to 8-16 GiB via a DB parameter group
for the build window. The `rds` module does not yet manage a custom parameter
group, so the enwiki migration must add one - plan that as part of the
migration, not a variable flip.

### Monthly cost, eu-west-3, on-demand (approximate, 2026)

| Item | Single-AZ | Multi-AZ |
|---|---|---|
| `db.t4g.micro` (dev today) | ~$13 | - |
| `db.t4g.small` (prod today) | ~$26 | ~$53 |
| `db.r7g.2xlarge` (enwiki) | ~$760 | ~$1,520 |
| 500 GB gp3 storage | ~$66 | ~$132 |
| Weekly wikisync Fargate run (1 vCPU / 4 GB, ~2 h) | < $1 | - |

Trade-offs: the enwiki corpus multiplies database cost roughly 20x and is the
dominant line item; the scheduled task itself is noise. Reserved instances cut
the r7g cost ~35-55% once the size is proven. Staying multi-AZ doubles the
instance cost but not the storage decision risk; a single-AZ enwiki with
21-day backups is a defensible first step while retrieval quality is being
evaluated. Verify current prices before applying; these are estimates for
planning, not quotes.
