# Infrastructure

Operator tooling targets AWS through one SSO profile, region `eu-west-3`, with directory-per-env
roots under `stack/terraform`. State lives in the S3 bucket `truth-in-stream-tfstate` (native S3
locking). Deploys stay human-gated.

```bash
./scripts/bootstrap-tfstate.sh                          # once, before the first init
cd stack/terraform/dev && terraform init && terraform plan
```

See [`stack/terraform/README.md`](../stack/terraform/README.md) for the SSO setup, the CI/CD roles and
pre-apply IAM guard, and the per-variable toggles.

## Environments

- **dev** (`stack/terraform/dev`) provisions no RDS by default (`enable_rds = false`); the database is
  developed locally with Docker Compose.
- **prod** (`stack/terraform/prod`) is the live environment and provisions the full edge, RDS, and
  observability described below. It is reachable at <https://jeminforme.fr>.

## Production edge

The app is served at <https://jeminforme.fr> (apex and `www`) through CloudFront in front of a private
ALB; the ALB has no public DNS name and is reachable only from CloudFront.

```
  Browser
     |  HTTPS (jeminforme.fr / www)
     v
  CloudFront  ---- WAF web ACL (managed rules + per-IP rate limit)
     |  VPC origin, HTTP, private subnets
     v
  Internal ALB  (security group admits only the CloudFront origin-facing prefix list)
     |
     v
  ECS services  (frontend, backend)
```

- **CloudFront** terminates TLS and forwards every request dynamically (no caching of the app or
  `/api/*`); the WAFv2 web ACL (scope `CLOUDFRONT`, provisioned in `us-east-1`) applies AWS managed
  rule groups plus a per-IP rate limit, and its decision logs redact the `Authorization` and `Cookie`
  headers.
- **ALB** is internal, lives in the private subnets, and its security group admits only CloudFront's
  origin-facing prefix list, so the load balancer is never reachable from the public internet.
- **TLS + DNS** the ACM certificate (`jeminforme.fr` + `www`, DNS-validated) is created in `us-east-1`
  for CloudFront. The authoritative hosted zone stays in a separate main account, so a dedicated
  `stack/terraform/main-account` root writes the ACM validation records and the apex/`www` alias
  records to CloudFront cross-account. It is applied by hand and excluded from CI:
  `make tf-main-account-plan` / `make tf-main-account-apply`.
- **Media** is served as direct presigned S3 GET URLs and is not fronted by CloudFront.
- **Keycloak** the production identity provider is self-hosted by this terraform: an ECS Fargate
  service (module `keycloak`) behind the internal ALB, reached from the browser at
  `https://login.jeminforme.fr` through the same CloudFront distribution (a host-header ALB rule
  routes `login.` to it). It runs in Keycloak production mode with edge TLS termination
  (`KC_PROXY_HEADERS=xforwarded`, `KC_HOSTNAME=https://login.jeminforme.fr`) and stores its realm and
  users in a dedicated `keycloak` database on the shared RDS instance, created by an idempotent
  one-shot bootstrap task the release runs before Keycloak rolls. Gated by `enable_keycloak`
  (default on; requires RDS). See [Configuration -> Local Keycloak](configuration.md#local-keycloak-identity-provider)
  for the realm contract shared with local dev.

## Production database

Prod provisions RDS PostgreSQL 17 with `pgvector` (`enable_rds = true`, `db.t4g.small`, single-AZ,
gp3 storage, automated backups and deletion protection). It is private to the VPC and reached by ECS
tasks over a security group; the `DATABASE_URL` is held in Secrets Manager and consumed by ARN.

The already-embedded local database is loaded into RDS once over an SSM tunnel through a bastion
(`enable_bastion`, kept off except during the load):

```bash
make db-tunnel ENV=prod        # SSM port-forward to the private RDS on 5432 (keep running)
make db-push    ENV=prod       # in a second shell: load the dump (vectors via text COPY, halfvec-safe)
```

- **Database backups** - the DB holds expensive-to-recompute embeddings, so it is dumped with
  `pg_dump -Fc` and restored without re-embedding (`halfvec` round-trips byte-for-byte). Manual:
  `make backup` / `make restore` (set `DB_BACKUP_BUCKET`). Scheduled: a Fargate cron task gated by
  `enable_db_backup`. See [`modules/scheduled-task`](../stack/terraform/modules/scheduled-task/README.md).
- **Secrets** - Terraform creates the secret containers; values are set out of band with
  `make push-secrets ENV=prod` (no value ever passes through an argv, log, or chat). ECS consumes
  secrets by ARN, so a roll needs no task-definition re-pin.

## Analysis cache

Production backs the analysis cache with an ElastiCache Valkey 8.0 cluster (`enable_valkey`, on by
default in prod). It is a single-node replication group (`cache.t4g.micro`, no replica) in the
private subnets, reachable only from the backend task security group on port `6379` with in-transit
TLS. Terraform injects its endpoint into the backend as `REDIS_URL` (a `rediss://` URL), enabling the
instant-replay path described in
[Configuration -> Analysis cache](configuration.md#analysis-cache-instant-replay). The node is an
ephemeral accelerator with no durable state and no failover: on a node failure the app simply falls
back to re-analysis. Provisioned in terraform only; `terraform apply` stays human-gated.

## Observability

Logs, alarms, and Slack alerting are provisioned for prod (`stack/terraform/modules/observability`).

- **Logs** - the backend logs structured JSON (`slog`) to stdout; ECS ships container logs to
  CloudWatch under `/ecs/truth-in-stream-<env>`, retained for `log_retention_days` (default 30). The
  WAF and the Slack-forwarder lambda log to their own groups under the same retention.
- **Alarms** - CloudWatch alarms cover ALB 5xx, per-service unhealthy hosts, backend/frontend
  running-task counts, RDS CPU and free storage, Amazon MQ CPU, and a WAF blocked-request spike. They
  publish to an SNS `alerts` topic (a second copy lives in `us-east-1` for the CloudFront-scoped WAF
  metrics). A `truth-in-stream-<env>-health` CloudWatch dashboard (created by default) summarizes the
  same signals.
- **Slack alerting** - SNS fans out to a Slack-forwarder lambda that reads the webhook URL from
  Secrets Manager and posts the alarm notifications to Slack. The ingestion crawlers post run
  start/finish/failure notices to the same Slack webhook (`SLACK_WEBHOOK_URL`); both are a silent
  no-op when it is unset.
- **Health checks** - `/healthz` is the only unauthenticated backend route and backs the ALB target
  health check; the frontend target group checks `/login`.

## Cloud ingestion

There is one cloud ingestion model: two on-demand **EC2 hosts** (a crawler host running the
producers, a consumer host running the workers) run the backend image's producer/worker containers
directly in the VPC, driven by the `/crawler` and `/consumer` commands over SSM. The hosts live
behind `enable_ingestion_hosts` (default off) in `stack/terraform/dev` and are stopped between runs,
so idle cost is only their EBS volumes. See the operator runbook
[`docs/ingestion-hosts.md`](ingestion-hosts.md) and the pipeline summary
[`ingestion-pipeline.md`](ingestion-pipeline.md#13-cloud--production-pipeline). Provisioning is
human-gated (`terraform apply -var enable_ingestion_hosts=true`).

## Deploys (human-gated)

A production deploy is always a deliberate human action. There are two entrypoints, and both keep
the human gate: an ordinary merge to `main` never deploys.

**Tag release (`release.yml`).** Pushing a semver tag matching `v*` whose commit is on `main` rolls
the prod services automatically, in order (`needs:`): **guard -> backend -> {keycloak, frontend}**.
`backend` builds, Trivy-scans, pushes, runs migrations, and rolls; then `keycloak` (runs its
idempotent DB-bootstrap task, then rolls) and `frontend` roll in parallel (frontend does not depend
on keycloak, so a keycloak issue never blocks the frontend roll). Each service waits for
`services-stable`. Cutting the tag is the deliberate release gesture and stands in for the dispatch
approval.

```bash
git checkout main && git pull
git tag v1.4.0           # semver tag on a main commit
git push origin v1.4.0   # release.yml -> roll backend, keycloak, frontend
```

A tag whose commit is **not** on `main` (e.g. cut from a side branch) fails fast in the guard job
and deploys nothing. The roll pins each service to a fresh task-definition revision referencing the
build's immutable `sha-<7>` image (not the moving `latest` tag, which is not advanced on a tag ref),
so the release is deterministic and a later unrelated `latest` push cannot drift prod. Cloud
ingestion is out of scope for a tag release: the EC2 ingestion hosts pull the current backend image
and are driven on demand by `/crawler`/`/consumer`. Backup is image-only. The keycloak job assumes
`enable_keycloak=true` (the default); if you run Keycloak out of band, drop that job.

The tag rolls **services only**; it does not run `terraform apply`. Infrastructure changes to
`stack/terraform/prod` (and standing the stack up the first time) are a separate, deliberate human
apply, kept out of the tag path on purpose: the apply role's OIDC trust is ref-scoped (not
tag/environment), and applying would revert the ECS services off their pinned `sha-<7>` task
definitions back to `:latest`. So the flow is: apply prod (once, and again when infra changes), then
tag to roll the images.

```bash
cd stack/terraform/prod && terraform init && terraform apply   # deliberate, human-run
```

**Manual dispatch (`deploy-*.yml`).** The per-service `workflow_dispatch` workflows
(`deploy-backend`, `deploy-frontend`, `deploy-keycloak`, `deploy-backup`) remain for ad-hoc
single-service rolls and **rollbacks**: to roll back, dispatch the relevant `deploy-*` workflow from
the last-good commit (or re-push that commit's tag). See [Development -> CI](development.md#ci).

The tag is the gate: an ordinary merge to `main` never deploys, and no `terraform apply` (prod or
main-account) ever runs without a deliberate human apply. Repo variables `AWS_REGION`,
`DEPLOY_PROJECT`, `DEPLOY_ENVIRONMENT=prod`, and `AWS_DEPLOY_ROLE_ARN` drive the deploy jobs, which
bind to the `production` GitHub Environment.
