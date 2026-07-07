# Terraform — truth-in-stream

Infrastructure as code, AWS region `eu-west-3`. Layout is directory-per-environment:
each of `dev/` and `prod/` is an independent root module with its own isolated state.

```
stack/terraform/
├── modules/
│   ├── vpc/             2-AZ VPC, public/private subnets, configurable NAT count, SGs
│   ├── ecs/             Fargate cluster (Container Insights) + log group
│   ├── ecr/             image repositories, scan-on-push, lifecycle policy
│   ├── iam/             GitHub OIDC provider, deploy role, ECS task roles
│   ├── rds/             PostgreSQL 17 (pgvector), credentials + DSN in Secrets Manager (optional, enable_rds)
│   ├── rabbitmq/        Amazon MQ for RabbitMQ broker + URL secret (embedding-job queue)
│   ├── acm/             us-east-1 public TLS certificate (DNS validation, cross-account records)
│   ├── alb/             public ALB; HTTP now, HTTPS once certificate_arn is set
│   ├── service/         task definition + target group + listener rule + service
│   ├── worker/          headless ECS worker service (embedding-worker fleet)
│   ├── migration/       one-shot Fargate task running golang-migrate
│   ├── scheduled-task/  EventBridge-scheduled one-shot Fargate task (wiki delta sync)
│   ├── s3/              media object storage bucket (presigned uploads)
│   └── s3-backup/       versioned, lifecycle-retained pg_dump backup bucket
├── dev/          dev root module  (state key: dev/terraform.tfstate)
├── prod/         prod root module (state key: prod/terraform.tfstate)
└── main-account/ main-account DNS root (state key: main-account/terraform.tfstate) — manual apply, CI-excluded
```

The `main-account/` root is the one exception to "directory-per-environment": it
is not an app environment but the cross-account DNS publisher. It targets the
**main account** (`<main-account-id>`) that owns the `jeminforme.fr` hosted zone,
creating the ACM validation records and apex/`www` CloudFront aliases from the
prod outputs. It is **applied by hand and excluded from CI** — see
`main-account/README.md`.

## Architecture

Backend (`:8080`) and frontend (`:3000`) run as Fargate services in private
subnets behind one public ALB. Path routing: `/api/*` and `/healthz` go to the
backend, everything else to the frontend. RDS PostgreSQL 17 (pgvector) lives in
private subnets, reachable only from the task security group. All credentials
flow through Secrets Manager; containers receive them as ECS secrets. CI
deploys via the GitHub OIDC deploy role: push images to ECR, run the migration
task, wait for exit 0, then force a new deployment of both services.

Dev: single NAT gateway, `db.t4g.micro`, no Multi-AZ, nothing protected.
Prod: a deliberately small, single-AZ **cost-baseline** (single NAT, single-AZ
RDS, single-instance MQ, right-sized SPOT-where-safe Fargate tasks) with backups,
deletion protection, and security posture unchanged — every reduction reversible
by a variable. Same modules, different variables. See the cost-baseline note
below for each choice and the scale-up order.

### Cost-baseline (right-sized prod)

Prod runs an early-production, single-region baseline that meets current needs
without over-provisioning. Every reduction below is reversible by **raising a
variable**, never by editing code, so HA is restored by changing values. Backups,
deletion protection, and the security posture are **not** traded for cost.

| Area | Baseline | Variable | HA / scale-up |
|---|---|---|---|
| NAT gateway | 1 (single-AZ egress) | `nat_gateway_count` | `2` for per-AZ HA egress |
| RDS | `db.t4g.small`, single-AZ | `rds_instance_class`, `rds_multi_az` | larger class, then `rds_multi_az = true` |
| RDS backups / deletion protection | 21-day backups, protected, final snapshot | (unchanged) | always on, independent of Multi-AZ |
| Amazon MQ | `SINGLE_INSTANCE` on `mq.t3.micro` | `mq_deployment_mode`, `mq_host_instance_type` | `CLUSTER_MULTI_AZ` on `mq.m5`/`mq.m7g` |
| Backend tasks | 1 task, 512 CPU / 1024 MiB, on-demand FARGATE | `backend_desired_count`, `backend_cpu`, `backend_memory` | raise count for serving redundancy; stays on-demand (live WebSocket sessions) |
| Frontend tasks | 1 task, 256 CPU / 512 MiB, FARGATE_SPOT | `frontend_desired_count`, `frontend_cpu`, `frontend_memory`, `frontend_use_spot` | raise count; `frontend_use_spot = false` for interruption-free rollouts |
| CloudWatch logs | 30-day finite retention | `log_retention_days` | raise for longer audit windows |

**SPOT safety.** Only the stateless frontend runs on FARGATE_SPOT. The backend
terminates live transcription WebSocket sessions, so a SPOT reclamation would drop
in-flight sessions; it stays on on-demand FARGATE. The cluster keeps both
`FARGATE` and `FARGATE_SPOT` registered, so a service opts into SPOT through its
own `capacity_provider_strategy` (the `service` module's
`capacity_provider_strategy` input) and the rest inherit the on-demand default.

**Scale-up order as load grows** (cheapest, highest-impact first):

1. **Backend redundancy** — `backend_desired_count = 2` (removes the single-task
   serving SPOF; cheapest availability win).
2. **NAT HA** — `nat_gateway_count = 2` (removes the single-AZ egress SPOF).
3. **RDS** — scale `rds_instance_class` for CPU/memory pressure, then
   `rds_multi_az = true` for failover durability.
4. **Frontend redundancy** — `frontend_desired_count = 2`, and
   `frontend_use_spot = false` if SPOT interruptions affect rollouts.
5. **MQ HA** — `mq_deployment_mode = CLUSTER_MULTI_AZ` with an `mq.m5`/`mq.m7g`
   `mq_host_instance_type`, once the worker fleet drives sustained queue traffic.

### Optional RDS (`enable_rds`)

The database is developed locally for now, so **dev provisions no RDS by
default** (`enable_rds = false`). With RDS off, the DB-dependent consumers gate
themselves off too — the migration task and the embedding-worker service are not
created, and the backend service is deployed without a `DATABASE_URL` secret — so
a fresh `terraform apply` has no dangling references. Prod defaults
`enable_rds = true`, so production is unchanged and the managed database is
provisioned. Flip `enable_rds = true` in dev (via `terraform.tfvars` or `-var`)
to bring the managed database online there.

### Application secrets

Two kinds of secret live in Secrets Manager, owned by two different sources, and
they must stay separate:

- **Terraform-owned**: the database DSN (`rds` module) and the Amazon MQ URL
  (`rabbitmq` module). Terraform generates these values and writes them, so they
  must **never** be pushed from `.env` — doing so would fight terraform and cause
  drift. The task definitions consume them by ARN (`DATABASE_URL`, `RABBITMQ_URL`).
- **App-key secrets**: the application's runtime API keys and operator auth.
  Terraform declares the *containers* (the `aws_secretsmanager_secret` resources
  in `prod/main.tf`) with **no value**; the operator fills the values from the
  local `.env` out of band. The execution-role policy (the `iam` module's
  `secret_arns`) grants `secretsmanager:GetSecretValue` on exactly these ARNs, so
  the ECS execution role injects them at task start. The default AWS-managed key
  (`aws/secretsmanager`) needs no `kms:Decrypt` grant; add one only if a
  customer-managed key is ever configured.

`make push-secrets ENV=prod` (`scripts/push-secrets.sh`) reads the local `.env`
and writes the **allowlisted** keys to `truth-in-stream/<env>/app/<kebab-name>`
idempotently — it checks each secret with `describe-secret`, then `create-secret`
if absent or `put-secret-value` if present, so a re-run never duplicates. A value
never passes through a shell argument, a log, or stdout: each is written to a
`chmod 600` temp file and handed to the CLI as `--secret-string file://…`, then
shredded. An unset or empty key is skipped, not pushed empty. Prod asks you to
type `prod` to confirm.

The allowlist (the set the script pushes; it is the source of truth and must
match the `aws_secretsmanager_secret` resources and task-definition `secrets`
wiring in `prod/main.tf`):

| `.env` key | secret name (`…/<env>/app/`) | consumed by | required? |
|---|---|---|---|
| `EMBEDDING_API_KEY` | `embedding-api-key` | backend, embed/crawl workers, wiki-sync | yes |
| `TRANSCRIPTION_API_KEY` | `transcription-api-key` | backend | yes |
| `AUTH_EMAIL` | `auth-email` | backend | yes |
| `AUTH_PASSWORD_HASH` | `auth-password-hash` | backend | yes |
| `SESSION_SECRET` | `session-secret` | backend | yes |
| `DEEPSEEK_API_KEY` | `deepseek-api-key` | backend (LLM gate) | optional |
| `GEMINI_API_KEY` | `gemini-api-key` | backend (LLM gate) | optional |
| `SLACK_WEBHOOK_URL` | `slack-webhook-url` | backend (run alerts) | optional |

`DATABASE_URL` and `RABBITMQ_URL` are intentionally **absent** from this list:
they are terraform-owned. To add a new app secret, add the `.env` key to the
`ALLOWLIST` in `scripts/push-secrets.sh`, declare the matching
`aws_secretsmanager_secret` resource in `prod/main.tf`, append its ARN to the
`iam` module's `secret_arns`, and wire it into the consuming task def's
`secrets` — all in the same change, so the allowlist and the task defs never
drift apart.

### One-time load of the embedded local DB into RDS (`enable_bastion`)

RDS is in private subnets with no public access, so the already-embedded local
Postgres database (claims vectors + `wiki_chunks` `halfvec` embeddings) is loaded
into it once over an SSM tunnel through the hardened bastion — no SSH, no public
IP, no public RDS, Session Manager only. The bastion is gated off by default
(`enable_bastion = false`): bring it up for the load, then take it back down.

**Vector fidelity.** The load is a `pg_dump --format=custom` dump replayed by
`pg_restore`, both in **text** format. `pg_dump` emits `COPY … TO` in text
(`halfvec` serialized as `[v1,…,vN]`) and `pg_restore` replays it via `COPY …
FROM` in text. The pgx **binary** `CopyFrom` path that corrupts `halfvec`
(phantom rows) is never used, so embeddings round-trip exactly — the same
text-COPY guarantee the `stack/backend/internal/dbbackup` round-trip test covers.

Runbook (run from the repo root, with a valid prod SSO session and the local
`postgres` service up so the dump runs inside it):

```sh
# 1. Bring the bastion up (human-gated apply).
cd stack/terraform/prod
terraform apply -var enable_bastion=true        # adds the SSM bastion + RDS:5432 ingress

# 2. Create the schema in RDS first (the migration task runs golang-migrate).
#    Dispatch deploy-backend (it builds the migrate image and applies migrations
#    when enable_rds = true), or run the migrate task directly with aws ecs run-task.

# 3. Open the tunnel (keep this terminal open).
make db-tunnel ENV=prod                          # localhost:5432 -> private RDS

# 4. In a second terminal, load the local embedded DB over the tunnel.
make db-push ENV=prod                             # pg_dump (local) | pg_restore (RDS), text COPY

# 5. Verify the load: row counts and a sample vector search against the tunnel.
PGPASSWORD=… psql "host=localhost port=5432 sslmode=require dbname=truthinstream user=…" \
  -c 'select count(*) from claims;' \
  -c 'select count(*) from wiki_chunks;' \
  -c 'select id from wiki_chunks order by embedding <=> (select embedding from wiki_chunks limit 1) limit 5;'

# 6. Tear the bastion back down once the load is verified.
terraform apply -var enable_bastion=false
```

`make db-push` reads the RDS credentials from the `…/prod/rds/dsn` secret at
runtime and passes the password to the restore container by environment-variable
**name only** (`-e PGPASSWORD`), so no secret ever appears in an argv or a log;
nothing is committed. `PORT=` overrides the local port on both `db-tunnel` and
`db-push` (use the same value for both), and `FILE=` reuses an existing dump. The
helpers are unit-tested with stubbed `aws`/`docker` CLIs in the `db-tunnel-script`
and `db-push-script` `pr.yml` jobs.

### Ingestion monitoring (`enable_metrics_lambda`)

Amazon MQ for RabbitMQ exposes almost nothing per queue, so there is no native
way to watch backlog or throughput per queue. With `enable_metrics_lambda = true`
the `metrics-lambda` module provisions a small Go lambda (`provided.al2023`,
arm64) that runs on an EventBridge Scheduler tick (default every minute), polls
the broker's RabbitMQ management API over HTTPS, and republishes per-queue stats
as custom CloudWatch metrics in the `TruthInStream/RabbitMQ` namespace. The
`monitoring` module builds the **ingestion dashboard** from those metrics.

Build the lambda binary before applying (the module zips it):

```sh
cd stack/backend && make lambda-mqmetrics   # -> build/mqmetrics/bootstrap
```

Published metrics, dimensioned by `Broker` and `Queue` (the full versioned name):

- `Backlog` — messages waiting in the queue (Count).
- `ConsumerCount` — consumers attached (Count).
- `PublishRate` — publish rate (Count/Second).

The same three are summed across every active version into a **version-stripped
rollup** under a `QueueBase` dimension (e.g. `embedding.jobs`), so a dashboard
widget keeps working as the active queue version rolls (`embedding.jobs.v1` →
`embedding.jobs.v2`).

Dashboard widgets (`<project>-<env>-ingestion`):

- **Queue backlog / publish rate / consumers by version** — `SEARCH` expressions
  over the namespace, so a new versioned queue appears automatically without a
  terraform change.
- **Backlog rollup** — the stable version-stripped backlog.
- **Worker running tasks** and **worker CPU / memory** — the embedding-worker ECS
  service next to the queue, so scaling behaviour is legible. Omitted when the
  worker is not provisioned.

The lambda's execution role is least-privilege: `secretsmanager:GetSecretValue`
scoped to the broker URL secret, and `cloudwatch:PutMetricData` scoped by a
namespace condition. Its security group is egress-only and is added to the
broker's `management_allowed_security_group_ids` — a separate allow-list from the
AMQPS data-plane one, so the management API stays closed to the application tasks.

### Worker autoscaling and rollout (`enable_worker_lifecycle`)

The embedding-worker fleet runs under an **EXTERNAL deployment controller**:
terraform provisions only the ECS service shell (no task definition, no desired
count it fights over), and the `worker-lifecycle` lambda owns scale and rollout.
With `enable_worker_lifecycle = true` the module provisions one Go lambda binary
(`provided.al2023`, arm64) behind three functions selected by `LIFECYCLE_HANDLER`:

- **scale** (EventBridge tick, default every minute) — reads the newest versioned
  queue's backlog from the management API and sets each service's desired count to
  `ceil(backlog / ratio)`, clamped to `[min, max]`, moving at most one exponential
  step per tick (double up, halve down) and honoring a per-service cooldown.
  `max = 0` disables a service and forces desired count to zero.
- **cleanup** (EventBridge tick) — retires superseded task sets. A different-version
  task set is deleted only once its version's queues have fully drained *and* the
  PRIMARY has served past `max_age_hours`; a same-version replacement after a short
  min-age; a zero-task "zombie" after its min-age. Nothing is retired while the
  PRIMARY is still coming up, so a roll never drops the fleet below capacity.
- **deploy** (invoked by the deploy workflow, no schedule) — registers a new task
  definition revision with the new image, creates a task set on the service's
  network (the PRIMARY's, or the configured bootstrap subnets/SGs on the first
  deploy), and promotes it to PRIMARY. The old PRIMARY becomes a non-PRIMARY task
  set that **cleanup** retires once its queues drain, so a version roll never drops
  in-flight work.

Build the lambda binary before applying (the module zips it):

```sh
cd stack/backend && make lambda-workerlifecycle   # -> build/workerlifecycle/bootstrap
```

The per-service scaling policy lives in **Parameter Store**
(`/<project>/<env>/worker-lifecycle/scaling-config`), read at cold start, because
the full map can exceed the lambda env-var limit. Tune it via the
`worker_lifecycle_scaling_config` variable; its default keeps `embedworker` at
`max = 0` (off) so the fleet stays at zero until the workers move onto ECS — raise
that service's `max` to enable autoscaling. The lambda's execution role is
least-privilege: ECS scale/task-set actions scoped to the one cluster,
`iam:PassRole` for the worker task roles only, the broker-secret read, and the
scaling-config parameter read. Its egress-only security group joins the broker's
`management_allowed_security_group_ids` like the metrics lambda's.

## Monitoring + alerting (`modules/observability`, prod)

Prod wires an always-on `observability` module that closes the production-readiness
loop: every service already logs to CloudWatch with **finite retention** (the shared
ECS task log group in `modules/ecs`, the WAF decision log group in `modules/waf`, and
each lambda's own group, all on `log_retention_days`), so this module adds the
monitoring and alerting half — CloudWatch alarms routed to an SNS topic and a small
Slack forwarder.

Alarms (thresholds are all variable-driven to keep paging tuned and avoid spam):

- **ALB 5xx** — `HTTPCode_ELB_5XX_Count` (load-balancer faults) over the window.
- **Unhealthy targets** — `UnHealthyHostCount` per service target group (backend,
  frontend), so a failing service pages even while the other stays healthy.
- **ECS running tasks** — `RunningTaskCount` below the desired floor per service
  (a crash or restart loop), `treat_missing_data = breaching` so a vanished service
  fires.
- **RDS health** — sustained `CPUUtilization` and a `FreeStorageSpace` floor.
- **Amazon MQ health** — sustained broker `SystemCpuUtilization`.
- **WAF blocked spike** — `BlockedRequests` above the steady-state block rate.

Each alarm publishes to the regional `<project>-<env>-alerts` SNS topic, whose only
subscriber is a **Slack forwarder Lambda** (`python3.13`, single source file, no build
step) that reads the incoming-webhook URL from Secrets Manager
(`<project>/<env>/app/slack-webhook-url`, the same secret the crawl alerts reuse —
never committed) and posts the alarm state change to Slack. The CLOUDFRONT-scoped WAF
publishes its metrics in **us-east-1**, and a CloudWatch alarm can only act on an SNS
topic in its own region, so the module also stands up a us-east-1 alerts topic and a
second copy of the forwarder for the WAF alarm. The forwarder's execution role is
least-privilege: `secretsmanager:GetSecretValue` scoped to the one webhook secret plus
the managed basic-execution policy for its own logs.

A `<project>-<env>-health` CloudWatch dashboard (toggle with `create_dashboard`)
summarises the key signals — ALB requests/5xx, unhealthy targets, running tasks per
service, RDS, MQ, and WAF allowed-vs-blocked.

Set the webhook value out of band before alerts can deliver (the secret container
exists, the value does not):

```sh
make push-secrets ENV=prod   # pushes SLACK_WEBHOOK_URL from .env, among others
```

## AWS SSO profile

All operator tooling targets the account through one AWS SSO profile, named
`truth-in-stream-dev`. Configure it once per machine:

```sh
aws configure sso --profile truth-in-stream-dev
#   SSO start URL : <your IAM Identity Center start URL>
#   SSO region    : eu-west-3
#   Account       : <dev account id>
#   Default region: eu-west-3
```

Then `aws sso login --profile truth-in-stream-dev` opens a session. The bootstrap
script keys off this profile (`AWS_PROFILE`, default `truth-in-stream-dev`) and
runs `aws sso login` for you if the session has expired.

## Remote state

State lives in the S3 bucket `truth-in-stream-tfstate` with native S3 locking
(`use_lockfile = true`, no DynamoDB table — the `dynamodb_table` backend argument
is deprecated). The bucket must exist before `init`.

### One-time bootstrap (run once, out of band)

```sh
./scripts/bootstrap-tfstate.sh
```

Idempotent and safe to re-run: it creates the state bucket only when missing,
then asserts versioning (required for native locking), default AES256 encryption,
and a full public-access block on every run. Override `STATE_BUCKET`,
`AWS_REGION`, or `AWS_PROFILE` via the environment if needed.

## First deploy of an environment

1. `cd dev && terraform init && terraform apply` — creates the runtime. With the
   default `enable_rds = false` no database is provisioned, and the migration
   task and embedding worker are not created; the backend/frontend services flap
   until images exist, which is expected. Set `enable_rds = true` first if you
   want the managed database and migration path in dev.
2. Set the GitHub Actions **repository variables** the per-service deploy
   workflows read (Settings, Secrets and variables, Actions, Variables tab; a
   role ARN and these identifiers are not secrets):
   - `AWS_DEPLOY_ROLE_ARN` from the `deploy_role_arn` output.
   - `AWS_REGION`, e.g. `eu-west-3`.
   - `DEPLOY_PROJECT`, the project slug, e.g. `truth-in-stream`.
   - `DEPLOY_ENVIRONMENT`, `dev` or `prod`.
3. Put values into the app secrets (terraform creates the containers empty on
   purpose; tasks cannot start without them). One command pushes the allowlisted
   keys from the local `.env` — see [Application secrets](#application-secrets)
   below. Pass the environment you are deploying (`ENV` defaults to `prod`, so a
   **dev** first-deploy must pass `ENV=dev`); a prod push asks you to type `prod`
   to confirm:
   ```sh
   make push-secrets ENV=dev    # or ENV=prod for production
   ```
4. Dispatch the per-service deploy workflows (`deploy-backend`,
   `deploy-frontend`, `deploy-workers`, `deploy-backup`) from the Actions tab.
   Each is `workflow_dispatch`-only and deploys one service: it builds and scans
   the image, pushes an immutable `sha-<short>` tag plus `latest` to ECR, then
   rolls that service and waits for stability (`deploy-backend` also builds the
   migrate image and applies migrations when `enable_rds = true`; `deploy-backup`
   is image-only, with no roll). There is no auto-deploy on merge.
5. Open the `app_url` output.

## Adding HTTPS later

Create a hosted zone + ACM certificate, then set `certificate_arn` on the
`alb` module call. The HTTP listener switches to a 301 redirect and services
attach to the HTTPS listener automatically.

## Cross-account ACM validation

The public certificate for `jeminforme.fr` is issued by the **app account**
(`<app-account-id>`), but the authoritative hosted zone for the domain stays in the
**main account** (`<main-account-id>`, zone `Z0839748310ZNBMJ0HI90`). The registrar
already delegates to that zone, so no nameserver change is needed and the app
account never creates a hosted zone.

The `prod` root requests the certificate in `us-east-1` (required for
CloudFront) via `modules/acm` and exposes its validation records as outputs —
but it does **not** create the records, because the zone is in another account:

1. The app account's `terraform apply` requests the certificate. It is created
   `PENDING_VALIDATION`; this does not block the apply (there is no
   `aws_acm_certificate_validation` resource that would wait on issuance).
2. The records to create are published as outputs:
   - `certificate_arn` — the certificate ARN (also consumed by CloudFront).
   - `certificate_domain_validation_options` — a map keyed by domain of the
     `{ name, type, value }` CNAME validation records. Nothing here is secret.
3. The dedicated **main-account** terraform root (`main-account/`, applied by
   hand, CI-excluded) reads those outputs (remote state or tfvars) and creates
   one CNAME per record in `Z0839748310ZNBMJ0HI90`. It also creates the apex/`www`
   CloudFront alias records. The operator runs `make tf-main-account-apply`; see
   `main-account/README.md` for the full apply runbook and order.
4. Once the records resolve, ACM validates and the certificate moves to
   `ISSUED`. Only then can CloudFront serve HTTPS with it.

Read the outputs with:

```sh
cd prod
terraform output certificate_domain_validation_options
```

## CloudFront in front of the internal ALB (prod)

Prod serves the app only through CloudFront over HTTPS. The ALB is `internal`
(private subnets, no public DNS) and its security group accepts ingress only
from the CloudFront origin-facing managed prefix list; CloudFront reaches it
over a **VPC origin** (PrivateLink), so the ALB is not publicly resolvable.
`modules/cloudfront` defines a default behavior (frontend) and an `/api/*`
behavior (backend), both dynamic and never cached (CachingDisabled cache policy
+ AllViewer origin-request policy forward all headers/cookies/query). Media
stays on direct presigned S3 URLs and is not fronted here.

The distribution exposes `cloudfront_domain_name` and `cloudfront_hosted_zone_id`
(the main-account root creates the apex/`www` alias records against those — see
VER-140) and `cloudfront_distribution_id` / `cloudfront_distribution_arn` (the
WAFv2 web ACL associates against those — see VER-131). This card creates no
alias records and no WAF association.

**Flipping an existing public ALB to internal is a replacement.** `internal` is
a `ForceNew` attribute on `aws_lb`, so changing an already-applied internet-facing
ALB to internal destroys and recreates it. The prod ALB sets
`deletion_protection = true`, and `DeleteLoadBalancer` fails while protection is
on. On the one apply that introduces this change against a pre-existing public
ALB, first disable deletion protection out of band (or in a prior apply), let the
replacement happen, then it is re-enabled. A fresh environment that has never
applied a public ALB creates the internal ALB directly with no replacement.

## Usage

Always run from inside an environment directory:

```sh
cd dev
terraform init
terraform plan
terraform apply
```

## CI

`.github/workflows/terraform.yml` runs fmt/validate/plan on PRs and plan+apply for
`dev` on merge to `main`. CI authenticates to AWS via GitHub OIDC; set the
`AWS_ROLE_ARN` repository secret to an IAM role whose trust policy is scoped to this
repo. The state-bucket bootstrap script and the pre-apply IAM guard are unit-tested
with a stubbed `aws` CLI in the `pr.yml` `bootstrap-script` and
`iam-apply-guard-script` jobs. The per-service application deploy workflows use
the separate, narrower `AWS_DEPLOY_ROLE_ARN` (a repository variable) created by
the `iam` module. See `.github/workflows/_terraform.yml`, the reusable
`.github/workflows/_deploy.yml`, and its per-service callers `deploy-backend.yml`,
`deploy-frontend.yml`, and `deploy-workers.yml`.

## CI/CD roles and the pre-apply IAM guard

Two CI roles, each least-privilege and scoped per concern:

- **Apply role** (`AWS_ROLE_ARN`) — assumed by `terraform.yml` to plan/apply.
  Bootstrapped out of band (it cannot be managed by the terraform it runs without
  a chicken-and-egg), so its policy lives next to the bootstrap procedure, not in
  a module it would have to create before it can act.
- **Deploy role** (`AWS_DEPLOY_ROLE_ARN`) — created by `modules/iam`, narrow:
  ECR push, ECS deploy, run the migrate task, read the deploy SSM parameters. Its
  trust is pinned to `repo:<org/repo>:ref:refs/heads/main` — PR branches and forks
  can never assume it. Resource-scoped, no blanket `*` actions (the only `*`
  resources are `ecr:GetAuthorizationToken` and the `ecs:Describe*` calls AWS
  itself requires at account scope).

### The required-actions manifest

`modules/apply-permissions` declares, grouped by resource area, the IAM actions the
apply role must hold to provision an environment, and each env root surfaces them
as the `apply_required_actions` output. This is the single source of truth that
stays in sync with what terraform provisions: **when a card adds a resource area,
it appends that area's actions to the matching block in
`modules/apply-permissions/main.tf` in the same change.** `include_rds` /
`include_scheduled_tasks` track the env's gated flags so the manifest only demands
permissions for resources the current plan actually creates.

### The chicken-and-egg guard

The apply role cannot grant itself permissions it lacks, so the first apply that
introduces a new resource type would otherwise fail halfway. To catch that up
front, `_terraform.yml` runs `scripts/iam-apply-guard.sh` between `terraform plan`
and `terraform apply`: it reads `apply_required_actions` from the plan and checks
each against the apply role with `aws iam simulate-principal-policy`. If any action
is denied, CI fails **before** applying and prints the missing actions and the one
manual command to run:

```sh
# A change needs new permissions the apply role lacks. Grant them to the apply
# role with elevated credentials, then re-run CI:
cd stack/terraform/dev
terraform apply
```

When the change needs no new permissions, the guard passes and the normal
auto-apply proceeds — no false positives. It runs on every plan that has AWS
credentials (PRs and `main`), so a missing permission surfaces before merge, not
only at the apply step. The apply role must itself hold
`iam:SimulatePrincipalPolicy` and `iam:GetRole` for the guard to run (declared in
the manifest); if it does not, the guard says so explicitly.

The guard assumes the environment is already bootstrapped — the apply role exists
and can simulate its own policy. The first-ever apply of a fresh account creates
that role and is the out-of-band bootstrap run with elevated credentials (see
[First deploy of an environment](#first-deploy-of-an-environment)); the guard
gates the automated applies that follow.

The manifest is the maintained source of truth, not an automatically-derived one:
no static list can be proven complete without applying, so when a real apply
surfaces a missing action, add it to the matching block — the guard then catches
that gap on the next change instead of failing mid-apply. The guard is exercised
offline (stubbed `aws`, fixture plans) by `scripts/iam-apply-guard.test.sh`, run
in CI as the `iam-apply-guard-script` job.
